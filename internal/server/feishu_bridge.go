package server

// 本机（非 systemd 托管）的飞书桥接直接运行在 serve 进程内：桌面端通过
// settings API 启动/重启，桥接生命周期跟随 serve。runner 可注入，测试
// 不会触碰真实的飞书长连接。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nightgopher/easyagent/internal/feishu"
	"github.com/nightgopher/easyagent/sdk/config"
)

type feishuBridge struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	gen       int // 当前代；runner 退出时比对，避免旧代退出覆盖新代状态
	running   bool
	startedAt time.Time
	lastError string
	// relay 是当前一代桥接的看门狗转交入口；桥接未运行或该代已结束时为 nil。
	relay    func(waitCtx context.Context, req feishu.RelayRequest) (feishu.RelayResult, error)
	relayCtx context.Context
}

// attach 由桥接 runner 在 handler 就绪后调用，登记转交入口。ctx 是该代的
// 生命周期：代结束后入口自动失效，旧代不会顶替新代。
func (b *feishuBridge) attach(ctx context.Context, handler *feishu.Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	b.relay = func(waitCtx context.Context, req feishu.RelayRequest) (feishu.RelayResult, error) {
		return handler.StartRelay(ctx, waitCtx, req)
	}
	b.relayCtx = ctx
}

// relayer 返回当前可用的转交入口；桥接未运行时返回 nil。
func (b *feishuBridge) relayer() func(context.Context, feishu.RelayRequest) (feishu.RelayResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.relay == nil || b.relayCtx == nil || b.relayCtx.Err() != nil {
		return nil
	}
	return b.relay
}

func (b *feishuBridge) status() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	status := map[string]any{"running": b.running}
	if b.running {
		status["started_at"] = b.startedAt.UTC().Format(time.RFC3339)
	}
	if b.lastError != "" {
		status["error"] = b.lastError
	}
	return status
}

// restart 停掉上一代桥接并用当前已保存的凭据启动新的一代。凭据缺失时
// 同步报错；runner 内部的启动失败异步记录在状态里，由 UI 轮询呈现。
func (b *feishuBridge) restart(baseURL string, runner func(ctx context.Context, baseURL string) error) (map[string]any, error) {
	if creds, _ := feishu.LoadCredentials(); creds == nil || creds.AppID == "" || creds.AppSecret == "" {
		return nil, errors.New("未找到飞书凭据，请先完成扫码绑定")
	}
	b.mu.Lock()
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	b.gen++
	generation := b.gen
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.running = true
	b.startedAt = time.Now()
	b.lastError = ""
	b.mu.Unlock()

	go func() {
		err := runner(ctx, baseURL)
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.gen != generation {
			return // 已被更新的重启取代，不覆盖新代状态
		}
		b.cancel = nil
		b.running = false
		if err != nil && ctx.Err() == nil {
			b.lastError = err.Error()
		}
	}()
	return b.status(), nil
}

// localBridgeRun 返回桥接 runner：测试可注入，生产用真实实现。
func (s *Server) localBridgeRun() func(ctx context.Context, baseURL string) error {
	if s.localBridgeRunner != nil {
		return s.localBridgeRunner
	}
	return func(ctx context.Context, baseURL string) error {
		return runFeishuBridgeLocal(ctx, baseURL, s.localBridge.attach)
	}
}

// runFeishuBridgeLocal 与 cmd/easyagent-bridge 的组装保持一致：核心就是
// serve 自己，长连接跑在进程内。启动失败（含凭据缺失）作为 error 返回。
// attach 在 handler 就绪后登记看门狗转交入口，可为 nil。
func runFeishuBridgeLocal(ctx context.Context, baseURL string, attach func(context.Context, *feishu.Handler)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	creds, _ := feishu.LoadCredentials()
	if creds == nil || creds.AppID == "" || creds.AppSecret == "" {
		return errors.New("未找到飞书凭据，请先完成扫码绑定")
	}
	workspace := config.Env("EA_WORKSPACE")
	callbackURL := os.Getenv("BRIDGE_CALLBACK_URL")
	callbackAddr := os.Getenv("BRIDGE_CALLBACK_ADDR")

	accessPath := os.Getenv("FEISHU_OWNER_STATE_FILE")
	if accessPath == "" {
		accessPath = filepath.Join(config.HomeDir(), "feishu-owner.json")
	}
	access, err := feishu.OpenOwnerAccess(accessPath, creds.UserOpenID)
	if err != nil {
		return errors.New("使用者绑定状态初始化失败")
	}

	client := feishu.NewClient(creds.AppID, creds.AppSecret)
	handler := feishu.NewHandler(baseURL, creds.AppID, client, workspace)
	handler.SetOwnerAccess(access)
	handler.StartNotificationWorker(ctx)
	gateway := feishu.NewGateway(creds.AppID, creds.AppSecret, client, func(ctx context.Context, msg feishu.Message) {
		handler.Handle(ctx, msg)
	})
	gateway.SetOwnerAccess(access)
	gateway.SetCardActionHandler(handler.HandleCardAction)
	if os.Getenv("FEISHU_INBOX_DIR") != "" {
		gateway.SetInbox(handler.InboxDir)
	}
	if attach != nil {
		attach(ctx, handler)
	}

	if callbackURL != "" {
		if callbackAddr == "" {
			callbackAddr = ":9090"
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/tool-callback", handler.HandleToolCallback)
		go func() {
			server := &http.Server{Addr: callbackAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Println("feishu tool callback server stopped:", err)
			}
		}()
		if err := feishu.RegisterTool(baseURL, callbackURL+"/tool-callback"); err != nil {
			fmt.Println("feishu tool registration skipped:", err)
		}
	}

	return gateway.Start(ctx)
}
