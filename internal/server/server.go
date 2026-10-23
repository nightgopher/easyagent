package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/hwj123hwj/easyagent/internal/app"
	"github.com/hwj123hwj/easyagent/internal/scheduler"
	"github.com/hwj123hwj/easyagent/internal/web"
	"github.com/hwj123hwj/easyagent/sdk/agent"
	"github.com/hwj123hwj/easyagent/sdk/ai"
	"github.com/hwj123hwj/easyagent/sdk/runtime"
	"github.com/hwj123hwj/easyagent/sdk/slashcmd"
)

// Version is the server build version. Set by main.go via SetVersion().
var Version = "dev"

// SetVersion updates the server's build version (called from main).
func (s *Server) SetVersion(v string) {
	Version = v
}

// Server provides HTTP REST + SSE endpoints for the agent.
// It routes requests to AgentSessions via the App's SessionRegistry.
type Server struct {
	activity          activityGate
	runs              *runRegistry
	workspaceSnapshot func(string) (map[string]fileState, []string) // nil uses the default scanner
	marketJobs        *marketJobRegistry
	queueMu           sync.Mutex
	queues            map[string]*messageQueue
	ctx               context.Context
	cancel            context.CancelFunc
	app               *app.App
	slashCmds         *slashcmd.Registry
	externalTools     []agent.ExternalToolDef
	toolMu            sync.Mutex
	extraRoutes       *http.ServeMux // optional extra routes (e.g. music audio proxy)
	apiKey            string         // if non-empty, requires Bearer token auth on all endpoints
	terminalEnabled   bool
	terminalSlots     chan struct{}
	baseURL           string                                          // 本机自呼地址（ListenAndServe 时归一化），进程内飞书桥接回调核心 API 用
	localBridge       feishuBridge                                    // 非 systemd 托管主机上随 serve 运行的进程内飞书桥接
	localBridgeRunner func(ctx context.Context, baseURL string) error // 测试注入；nil 用真实实现

	authFails      authLimiter // 错误令牌按来源 IP 限流
	allowNoAuth    bool        // EA_ALLOW_NO_AUTH=1：未配 key 时完全开放（调试用）
	allowedOrigins []string    // EA_ALLOWED_ORIGINS：显式 CORS 白名单；空 = 不返回 CORS 头
}

// SetExtraRoutes sets an additional ServeMux to be merged into the server's routes.
func (s *Server) SetExtraRoutes(mux *http.ServeMux) {
	s.extraRoutes = mux
}

// SetAPIKey enables Bearer token authentication on all REST endpoints.
// If set, all requests must include "Authorization: Bearer <apiKey>" header.
// The /health endpoint is always accessible without auth (for health checks).
func (s *Server) SetAPIKey(key string) {
	s.apiKey = key
}

// authMiddleware validates access on all REST endpoints.
// 详见 auth.go 顶部的访问控制模型说明。/health 始终开放。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health check endpoint (always open for monitoring)
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		if !s.authorized(r) {
			if s.apiKey != "" {
				status := s.denyStatus(w, r)
				msg := "unauthorized: set Authorization: Bearer <EA_API_KEY>"
				if status == http.StatusTooManyRequests {
					msg = "too many failed authentication attempts; retry later"
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(ErrorResponse{Error: msg})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(ErrorResponse{
				Error: "unauthorized: non-loopback access requires EA_API_KEY " +
					"(or EA_ALLOW_NO_AUTH=1 for open access)",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ChatRequest is the request body for chat endpoints.
type ChatRequest struct {
	Inputs    promptInputs `json:"inputs,omitempty"`
	Prompt    string       `json:"prompt"`
	SessionID string       `json:"session_id,omitempty"`
	RequestID string       `json:"request_id,omitempty"`
}

// ChatResponse is the response for non-streaming chat.
type ChatResponse struct {
	Text      string        `json:"text"`
	ToolCalls []ai.ToolCall `json:"tool_calls,omitempty"`
	SessionID string        `json:"session_id,omitempty"`
}

// SessionResponse is the response for session metadata.
type SessionResponse struct {
	ID           string `json:"id"`
	CreatedAt    int64  `json:"created_at"`
	MessageCount int    `json:"message_count"`
}

// ErrorResponse is the standard error response.
type ErrorResponse struct {
	Error string `json:"error"`
}

// New creates a new Server backed by the given App and slash command registry.
// It also wires the LoopManager's trigger resolver so /loop can inject prompts.
func New(application *app.App, slashCmds *slashcmd.Registry) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{app: application, slashCmds: slashCmds, ctx: ctx, cancel: cancel, runs: newRunRegistry(),
		terminalEnabled: os.Getenv("EA_ENABLE_TERMINAL") == "1", terminalSlots: make(chan struct{}, 8),
		allowedOrigins: envAllowedOrigins(), allowNoAuth: envAllowNoAuth()}
	// Install jobs must live for the whole server lifetime; Handler() is
	// rebuilt per request in tests, so create the registry exactly once here.
	if application.SkillMarket() != nil {
		srv.marketJobs = newMarketJobRegistry(srv.ctx, application)
	}
	srv.restoreRunReceipts()

	// Wire loop trigger: when a /loop fires, inject the prompt into the target session
	if application.LoopManager() != nil {
		application.LoopManager().SetTriggerResolver(func(sessionID string) scheduler.TriggerFunc {
			return func(ctx context.Context, prompt string) error {
				return srv.injectLoopPrompt(ctx, sessionID, prompt)
			}
		})
	}

	return srv
}

// injectLoopPrompt injects a prompt into a session as a background agent turn.
// Used by the /loop scheduler to fire recurring prompts.
func (s *Server) injectLoopPrompt(ctx context.Context, sessionID, prompt string) error {
	run, _, err := s.startRun(sessionID, prompt, "")
	if err != nil {
		return err
	}
	_, err = s.waitRun(ctx, run)
	return err
}

// Handler returns the HTTP handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	// REST API routes with middleware
	restMux := http.NewServeMux()
	restMux.HandleFunc("GET /health", s.health)
	restMux.HandleFunc("/admin/deploy", s.deploymentControl)
	restMux.HandleFunc("POST /chat", s.chat)
	restMux.HandleFunc("POST /chat/stream", s.chatStream)
	restMux.HandleFunc("GET /sessions", s.listSessions)
	restMux.HandleFunc("POST /sessions", s.createSession)
	restMux.HandleFunc("POST /sessions/{id}/fork", s.forkSession)
	restMux.HandleFunc("GET /sessions/{id}/attachments/{attID}/raw", s.getAttachmentRaw)
	restMux.HandleFunc("GET /usage/summary", s.usageSummary)
	restMux.HandleFunc("GET /computer/settings", s.getComputerSettings)
	restMux.HandleFunc("POST /computer/settings", s.updateComputerSettings)
	restMux.HandleFunc("POST /computer/permissions", s.requestComputerPermissions)
	restMux.HandleFunc("GET /sessions/{id}/messages", s.getSessionMessages)
	restMux.HandleFunc("GET /sessions/{id}/info", s.getSessionInfo)
	restMux.HandleFunc("GET /sessions/{id}/background-tasks", s.listBackgroundTasks)
	restMux.HandleFunc("GET /sessions/{id}/background-tasks/{task}", s.getBackgroundTask)
	restMux.HandleFunc("POST /sessions/{id}/background-tasks/{task}/stop", s.stopBackgroundTask)
	restMux.HandleFunc("POST /sessions/{id}/permissions", s.setSessionPermissions)
	restMux.HandleFunc("GET /sessions/{id}/run", s.getRun)
	restMux.HandleFunc("POST /sessions/{id}/run/cancel", s.cancelRunHTTP)
	restMux.HandleFunc("POST /sessions/{id}/run/continue", s.continueRunHTTP)
	restMux.HandleFunc("POST /sessions/{id}/run/confirm", s.confirmRunHTTP)
	restMux.HandleFunc("DELETE /sessions/{id}", s.deleteSession)
	restMux.HandleFunc("PATCH /sessions/{id}", s.patchSession)
	restMux.HandleFunc("GET /sessions/{id}/queue", s.messageQueueHTTP)
	restMux.HandleFunc("POST /sessions/{id}/queue", s.messageQueueHTTP)
	restMux.HandleFunc("POST /sessions/{id}/attachments", s.uploadAttachment)
	restMux.HandleFunc("POST /sessions/{id}/model", s.switchModel)
	restMux.HandleFunc("GET /models", s.listModels)
	restMux.HandleFunc("PUT /models/default", s.setDefaultModel)
	restMux.HandleFunc("GET /tools", s.listTools)
	restMux.HandleFunc("GET /commands", s.listCommands)
	restMux.HandleFunc("GET /applications", s.listApplications)
	restMux.HandleFunc("POST /sessions/{id}/compact", s.compactSession)
	restMux.HandleFunc("GET /sessions/{id}/context", s.getContextSnapshot)
	restMux.HandleFunc("POST /sessions/{id}/command", s.executeCommand)
	restMux.HandleFunc("GET /sessions/{id}/loop", s.getSessionLoop)
	restMux.HandleFunc("DELETE /sessions/{id}/loop", s.deleteSessionLoop)
	restMux.HandleFunc("POST /tools/register", s.registerTool)
	restMux.HandleFunc("GET /sessions/{id}/diff", s.getSessionDiff)
	restMux.HandleFunc("GET /sessions/{id}/runs/{run}/files", s.getRunFiles)
	restMux.HandleFunc("POST /sessions/{id}/runs/{run}/undo", s.undoRunFile)
	restMux.HandleFunc("GET /workspace/download", s.downloadWorkspaceFile)
	restMux.HandleFunc("GET /workspace/file-data", s.workspaceFileData)
	restMux.HandleFunc("GET /sessions/{id}/run-files", s.listRunFiles)
	restMux.HandleFunc("GET /sessions/{id}/capabilities", s.getCapabilities)
	restMux.HandleFunc("GET /sessions/{id}/file", s.getSessionFile)
	restMux.HandleFunc("PUT /sessions/{id}/file", s.writeFile)
	restMux.HandleFunc("GET /workspace/list-dir", s.listDir)
	restMux.HandleFunc("GET /workspace/search-files", s.searchFiles)
	restMux.HandleFunc("GET /workspace/read-file", s.workspaceReadFile)
	restMux.HandleFunc("GET /workspace/read-file-base64", s.workspaceReadFileBase64)
	restMux.HandleFunc("PUT /workspace/write-file", s.workspaceWriteFile)

	// Knowledge base browser endpoints
	s.registerKBRoutes(restMux)

	s.registerFeishuSettings(restMux)
	s.registerMCPRoutes(restMux)

	// Skill marketplace endpoints (no-op when disabled)
	s.registerSkillMarketRoutes(restMux)

	// User profile endpoints
	s.registerProfileRoutes(restMux)

	// ASR (speech-to-text) endpoint
	NewASRHandler(s.app.Config()).Register(restMux)

	var restHandler = s.admissionMiddleware(restMux)
	restHandler = s.authMiddleware(restHandler)
	restHandler = corsMiddleware(s)(restHandler)
	restHandler = recoveryMiddleware(restHandler)
	restHandler = loggingMiddleware(restHandler)

	// WebSocket route — bypasses all middleware to avoid Hijack issues;
	// 鉴权在 handleWebSocket 升级前自行校验
	wsHandler := corsMiddleware(s)(http.HandlerFunc(s.handleWebSocket))

	// Top-level mux: combines REST API + Web UI + WebSocket
	topMux := http.NewServeMux()
	topMux.Handle("GET /ws", wsHandler)
	topMux.Handle("GET /terminal", http.HandlerFunc(s.handleTerminal))

	// Register REST API routes (these take precedence over "/" catch-all)
	topMux.Handle("/health", restHandler)
	topMux.Handle("/admin/deploy", restHandler)
	topMux.Handle("/chat", restHandler)
	topMux.Handle("/chat/", restHandler)
	topMux.Handle("/sessions", restHandler)
	topMux.Handle("/sessions/", restHandler)
	topMux.Handle("/computer/", restHandler)
	topMux.Handle("/usage/", restHandler)
	topMux.Handle("/models", restHandler)
	topMux.Handle("/models/", restHandler)
	topMux.Handle("/tools", restHandler)
	topMux.Handle("/commands", restHandler)
	topMux.Handle("/mcp", restHandler)
	topMux.Handle("/mcp/", restHandler)
	topMux.Handle("/applications", restHandler)
	topMux.Handle("/workspace/", restHandler)
	topMux.Handle("/kb/", restHandler)
	topMux.Handle("/skills/market", restHandler)
	topMux.Handle("/skills/market/", restHandler)
	topMux.Handle("/profile", restHandler)
	topMux.Handle("/settings/", restHandler)
	topMux.Handle("/asr/", restHandler)

	// Register web UI routes (serves embedded static files at /)
	web.RegisterRoutes(topMux)

	// Merge extra routes (e.g. music audio proxy) if set
	if s.extraRoutes != nil {
		topMux.Handle("/music/", s.extraRoutes)
	}

	return topMux
}

// ListenAndServe starts the HTTP server on the given address.
func (s *Server) ListenAndServe(addr string) error {
	s.baseURL = normalizeLocalBaseURL(addr)
	slog.Info("starting easyagent server", "listen", addr)
	if !loopbackListen(addr) {
		slog.Warn("listening on a non-loopback address over plain HTTP; tokens and conversations are sent unencrypted. Put an HTTPS reverse proxy in front, see docs/SECURITY_MODEL.md", "listen", addr)
	}
	return s.serveUntilSignal(addr)
}

// loopbackListen 判断监听地址是否只接受本机连接；通配地址与主机名一律视为对外。
func loopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// normalizeLocalBaseURL 把监听地址转成进程内桥接可自呼的 loopback URL。
// 通配地址（0.0.0.0/::/空 host）按 127.0.0.1 处理。
func normalizeLocalBaseURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// ─── GET /health ──────────────────────────────────────────────────────────────

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"version": Version,
	})
}

// ─── session resolution ──────────────────────────────────────────────────────

// resolveSession gets an existing session or creates a new one.
func (s *Server) resolveSession(ctx context.Context, sessionID string) (*runtime.AgentSession, error) {
	return s.app.LoadOrCreateSession(ctx, sessionID)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: msg})
}

func writeSSE(w http.ResponseWriter, eventType, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func joinTexts(texts []string) string {
	result := ""
	for i, t := range texts {
		if i > 0 {
			result += "\n"
		}
		result += t
	}
	return result
}

// ─── middleware ───────────────────────────────────────────────────────────────

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.statusCode,
			"duration", time.Since(start).Round(time.Millisecond),
		)
	})
}

func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("handler panic", "error", err, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("internal error: %v", err))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(s *Server) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			// 默认不返回 CORS 头（同源 UI 与原生客户端不受影响）；
			// 仅白名单内的 Origin 显式放行，不再使用 *。
			if origin != "" && s != nil {
				for _, allowed := range s.allowedOrigins {
					if allowed == origin {
						w.Header().Set("Access-Control-Allow-Origin", origin)
						w.Header().Set("Vary", "Origin")
						w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
						w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
						break
					}
				}
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush preserves SSE delivery through logging middleware. Without this method,
// short progress events stay in net/http's buffer until the whole turn finishes.
func (rw *responseWriter) Flush() {
	_ = http.NewResponseController(rw.ResponseWriter).Flush()
}

// Unwrap lets ResponseController reach the underlying writer's capabilities.
func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// Hijack implements the http.Hijacker interface so WebSocket upgrades work
// through the logging middleware wrapper.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return rw.ResponseWriter.(http.Hijacker).Hijack()
}
