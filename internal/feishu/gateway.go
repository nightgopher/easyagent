package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// Message represents a parsed Feishu message event.
type Message struct {
	Text         string
	MessageID    string
	ChatID       string
	ChatType     string // "p2p" or "group"
	SenderOpenID string
	MsgType      string
}

// MessageHandler is called for each incoming message.
type MessageHandler func(ctx context.Context, msg Message)

// CardActionHandler is called for each incoming interactive card action.
type CardActionHandler func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error)

// Gateway manages a WebSocket long connection to Feishu event subscription.
type Gateway struct {
	access      *OwnerAccess
	appID       string
	appSecret   string
	client      *Client // for API calls like image download
	handler     MessageHandler
	cardHandler CardActionHandler
	onReady     func()
	// inbox names, per chat key, the directory inside the chat's workspace
	// where files users send are saved. A nil resolver keeps legacy image storage;
	// an empty result from a configured resolver disables downloads for that chat.
	inbox func(chatKey string) string
	// openAPI and httpClient are overridden in tests to serve the resource API locally.
	openAPI    string
	httpClient *http.Client

	// Dedup state
	seen    map[string]struct{}
	mu      sync.Mutex
	maxSeen int

	// Content dedup: "chatID:text" → first seen timestamp
	recentContents map[string]int64
	dedupWindowMs  int64

	// Text choice waiters (per chatID)
	choiceWaiters map[string]*choiceWaiter
	choiceMu      sync.Mutex
}

// choiceWaiter holds state for a pending text choice in a specific chat.
type choiceWaiter struct {
	ch         chan string
	buttons    []string
	defaultVal string
}

// NewGateway creates a new WebSocket gateway.
func NewGateway(appID, appSecret string, client *Client, handler MessageHandler) *Gateway {
	return &Gateway{
		appID:          appID,
		appSecret:      appSecret,
		client:         client,
		handler:        handler,
		seen:           make(map[string]struct{}),
		maxSeen:        1000,
		recentContents: make(map[string]int64),
		dedupWindowMs:  5000,
		choiceWaiters:  make(map[string]*choiceWaiter),
	}
}

// Start connects to Feishu WS and blocks until ctx is cancelled.
func (g *Gateway) Start(ctx context.Context) error {
	dispatcher := larkdispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			g.handleEvent(ctx, event)
			return nil
		})
	if g.cardHandler != nil {
		dispatcher.OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			if event == nil || event.Event == nil || event.Event.Operator == nil || !g.access.Allowed(event.Event.Operator.OpenID) {
				return cardToast("error", "未授权"), nil
			}
			return g.cardHandler(ctx, event)
		})
	}

	wsClient := larkws.NewClient(g.appID, g.appSecret,
		larkws.WithEventHandler(dispatcher),
		larkws.WithOnReady(func() {
			slog.Info("feishu WebSocket connected")
			if g.onReady != nil {
				g.onReady()
			}
		}),
	)

	slog.Info("feishu gateway connecting via WebSocket...")

	return runGatewayClient(ctx, wsClient)
}

type gatewayClient interface {
	Start(context.Context) error
	Close()
}

// The SDK's Start parks indefinitely after connecting, even after cancellation.
// Close explicitly so service shutdown does not wait for systemd's kill timeout.
func runGatewayClient(ctx context.Context, client gatewayClient) error {
	done := make(chan error, 1)
	go func() { done <- client.Start(ctx) }()
	var err error
	select {
	case <-ctx.Done():
		client.Close()
		return nil
	case err = <-done:
		client.Close()
	}
	if err != nil {
		slog.Error("feishu ws client stopped", "error", err)
	}
	return err
}

// SetOnReady sets a callback invoked after the WebSocket connection is ready.
func (g *Gateway) SetOnReady(handler func()) {
	g.onReady = handler
}

// SetCardActionHandler sets the interactive card action handler.
func (g *Gateway) SetCardActionHandler(handler CardActionHandler) {
	g.cardHandler = handler
}

// SetInbox names, for each chat key, the directory inside the chat's
// workspace where the files users send are saved. A chat that resolves to ""
// cannot download media; a nil resolver keeps legacy image storage in temp.
func (g *Gateway) SetInbox(inbox func(chatKey string) string) { g.inbox = inbox }

// handleEvent processes a single incoming message event.
func (g *Gateway) handleEvent(ctx context.Context, event *larkim.P2MessageReceiveV1) {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return
	}

	msg := event.Event.Message
	sender := event.Event.Sender

	// Filter: ignore non-user messages (bot's own messages)
	if sender != nil && sender.SenderType != nil && *sender.SenderType != "user" {
		return
	}

	msgType := "text"
	if msg.MessageType != nil {
		msgType = *msg.MessageType
	}

	// Gate before media downloads, choice waiters, dedup, or handler dispatch.
	actor := ""
	if sender != nil && sender.SenderId != nil && sender.SenderType != nil && *sender.SenderType == "user" {
		actor = derefStr(sender.SenderId.OpenId)
	}
	if !g.access.Allowed(actor) {
		if msgType == "text" && actor != "" {
			m := Message{Text: extractText(msg.Content), SenderOpenID: actor, ChatType: derefStr(msg.ChatType), MsgType: msgType}
			if g.access.Pair(m) {
				_, _ = g.client.ReplyMessage(ctx, derefStr(msg.MessageId), derefStr(msg.ChatId), "配对完成，仅此飞书账号获授权。")
			}
		}
		return
	}
	if msgType == "text" && isPairCommand(extractText(msg.Content)) {
		return
	}

	// Extract text content based on message type
	var text string
	messageID := derefStr(msg.MessageId)
	chatID := derefStr(msg.ChatId)
	chatType := derefStr(msg.ChatType)
	senderOpenID := ""
	if sender != nil && sender.SenderId != nil {
		senderOpenID = derefStr(sender.SenderId.OpenId)
	}
	route := Message{ChatID: chatID, ChatType: chatType, SenderOpenID: senderOpenID}
	chatKey := route.ChatKey()

	switch msgType {
	case "text":
		text = extractText(msg.Content)
	case "image":
		text = g.handleImageMessage(ctx, msg, chatKey)
	case "file":
		text = g.handleFileMessage(ctx, msg, chatKey, "file")
	case "media":
		text = g.handleFileMessage(ctx, msg, chatKey, "media")
	case "post":
		var err error
		text, err = g.handlePostMessage(ctx, msg, chatKey)
		if err != nil {
			slog.Warn("invalid rich text message", "messageID", messageID, "error", err)
			if !g.isDuplicate(messageID) && g.client != nil {
				_, _ = g.client.ReplyMessage(ctx, messageID, derefStr(msg.ChatId), "这条富文本消息未能读取，未提交给 Agent。请重发为纯文本，或分段发送。")
			}
			return
		}
	default:
		slog.Debug("unsupported message type, skipping", "type", msgType, "messageID", messageID)
		return
	}

	// Strip @bot mentions
	if msg.Mentions != nil {
		for _, m := range msg.Mentions {
			if m.Key != nil {
				text = strings.ReplaceAll(text, *m.Key, "")
			}
		}
		text = strings.TrimSpace(text)
	}

	if text == "" {
		return
	}

	// Build Message
	feishuMsg := Message{
		Text:         text,
		MessageID:    messageID,
		ChatID:       chatID,
		ChatType:     chatType,
		SenderOpenID: senderOpenID,
		MsgType:      msgType,
	}

	// Dedup by messageID
	if g.isDuplicate(messageID) {
		slog.Debug("skipping duplicate message", "messageID", messageID)
		return
	}

	// Content dedup
	if g.isContentDuplicate(chatID, text) {
		slog.Debug("skipping content-duplicate message", "messageID", messageID)
		return
	}

	// Check text choice waiter — intercept if message matches a pending choice
	if g.tryResolveChoice(chatID, text) {
		slog.Debug("message consumed by text choice waiter", "messageID", messageID)
		return
	}

	// Dispatch to handler with a fresh context (not the SDK's event context
	// which gets canceled as soon as the callback returns).
	go g.handler(context.Background(), feishuMsg)
}

// WaitForTextChoice sends a formatted options message and blocks until the user replies.
// Returns the matched button label, or defaultVal on timeout.
// Only one waiter per chatID is active at a time; a new call supersedes the previous one.
func (g *Gateway) WaitForTextChoice(
	ctx context.Context,
	chatID string,
	title string,
	content string,
	buttons []string,
	defaultVal string,
	timeout time.Duration,
) (string, error) {
	if len(buttons) == 0 {
		return defaultVal, nil
	}

	ch := make(chan string, 1)

	g.choiceMu.Lock()
	// Supersede any existing waiter for this chat
	if old, ok := g.choiceWaiters[chatID]; ok {
		select {
		case old.ch <- defaultVal:
		default:
		}
	}
	g.choiceWaiters[chatID] = &choiceWaiter{
		ch:         ch,
		buttons:    buttons,
		defaultVal: defaultVal,
	}
	g.choiceMu.Unlock()

	defer func() {
		g.choiceMu.Lock()
		if w, ok := g.choiceWaiters[chatID]; ok && w.ch == ch {
			delete(g.choiceWaiters, chatID)
		}
		g.choiceMu.Unlock()
	}()

	// Send formatted options message
	var sb strings.Builder
	if title != "" {
		fmt.Fprintf(&sb, "**%s**\n\n", title)
	}
	if content != "" {
		sb.WriteString(content + "\n\n")
	}
	for i, btn := range buttons {
		fmt.Fprintf(&sb, "> **%d**. %s\n", i+1, btn)
	}
	sb.WriteString("\n请回复序号或选项名称进行选择。")

	if _, err := g.client.SendMessage(ctx, chatID, sb.String(), ""); err != nil {
		return defaultVal, fmt.Errorf("send choice message: %w", err)
	}

	select {
	case result := <-ch:
		return result, nil
	case <-time.After(timeout):
		return defaultVal, nil
	case <-ctx.Done():
		return defaultVal, ctx.Err()
	}
}

// tryResolveChoice checks if a message matches a pending choice waiter.
// Returns true if the message was consumed by a waiter.
func (g *Gateway) tryResolveChoice(chatID, text string) bool {
	g.choiceMu.Lock()
	w, ok := g.choiceWaiters[chatID]
	g.choiceMu.Unlock()

	if !ok {
		return false
	}

	if matched := matchChoice(text, w.buttons); matched != "" {
		select {
		case w.ch <- matched:
		default:
		}
		return true
	}

	return false
}

// matchChoice tries to match user input to a button label.
// Supports: number ("1" → first button) or text (case-insensitive match).
func matchChoice(input string, buttons []string) string {
	input = strings.TrimSpace(input)

	// Try numeric match
	for i, btn := range buttons {
		if input == fmt.Sprintf("%d", i+1) {
			return btn
		}
	}

	// Try text match (case-insensitive)
	lower := strings.ToLower(input)
	for _, btn := range buttons {
		if strings.ToLower(btn) == lower {
			return btn
		}
	}

	return ""
}

// isDuplicate checks and records a message ID for dedup.
func (g *Gateway) isDuplicate(messageID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.seen[messageID]; ok {
		return true
	}

	g.seen[messageID] = struct{}{}

	// Evict entries when over limit
	if len(g.seen) > g.maxSeen {
		count := len(g.seen) - 900
		for k := range g.seen {
			delete(g.seen, k)
			count--
			if count <= 0 {
				break
			}
		}
	}

	return false
}

// isContentDuplicate checks content+time window dedup.
func (g *Gateway) isContentDuplicate(chatID, text string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	key := chatID + ":" + text
	now := time.Now().UnixMilli()

	if firstSeen, ok := g.recentContents[key]; ok {
		if now-firstSeen < g.dedupWindowMs {
			return true
		}
	}

	g.recentContents[key] = now

	// Cleanup expired entries (2x window)
	expireThreshold := now - g.dedupWindowMs*2
	for k, ts := range g.recentContents {
		if ts < expireThreshold {
			delete(g.recentContents, k)
		}
	}

	return false
}

// handleImageMessage downloads the image and returns a markdown image reference.
func (g *Gateway) handleImageMessage(ctx context.Context, msg *larkim.EventMessage, chatKey string) string {
	if msg.Content == nil {
		return ""
	}

	var content struct {
		ImageKey string `json:"image_key"`
	}
	if err := json.Unmarshal([]byte(*msg.Content), &content); err != nil || content.ImageKey == "" {
		return ""
	}

	localPath, err := g.downloadResource(ctx, chatKey, derefStr(msg.MessageId), content.ImageKey, "image", "feishu-image-"+content.ImageKey+".png")
	if err != nil {
		slog.Warn("download image failed", "imageKey", content.ImageKey, "error", err)
		return ""
	}

	return fmt.Sprintf("![image](%s)", localPath)
}

// handleFileMessage saves a file the user sent, such as a video or a
// document, and names its path for the model. A file message carries
// file_key and file_name; a video message carries file_key only.
func (g *Gateway) handleFileMessage(ctx context.Context, msg *larkim.EventMessage, chatKey, kind string) string {
	var content struct {
		FileKey  string `json:"file_key"`
		FileName string `json:"file_name"`
	}
	if msg.Content != nil {
		_ = json.Unmarshal([]byte(*msg.Content), &content)
	}
	if content.FileKey == "" {
		return "[文件消息]"
	}
	// Without an inbox the file would land outside the workspace, where the
	// model's file tools cannot read it; skip it as before.
	if g.configuredInbox(chatKey) == "" {
		return ""
	}
	name := sanitizeFileName(content.FileName)
	if kind == "media" {
		name = sanitizeFileName("feishu-video-" + content.FileKey + ".mp4")
	}
	label := name
	localPath, err := g.downloadResource(ctx, chatKey, derefStr(msg.MessageId), content.FileKey, "file", name)
	if err != nil {
		slog.Warn("download file failed", "fileKey", content.FileKey, "error", err)
		return fmt.Sprintf("[文件: %s]（下载失败，无法读取，请重新以文件形式发送）", label)
	}
	return fmt.Sprintf("[文件: %s] 已保存到 %s", label, localPath)
}

// sanitizeFileName reduces a user-supplied name to a plain file name.
func sanitizeFileName(name string) string {
	if cut := strings.LastIndexAny(name, "/\\"); cut >= 0 {
		name = name[cut+1:]
	}
	name = strings.Map(func(r rune) rune {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-", r) {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	// Leave room for CreateTemp's unique suffix and avoid oversized names.
	runes := []rune(name)
	if len(runes) > 48 {
		name = string(runes[:48])
	}
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

// handlePostMessage parses a post (rich text) message into markdown.
func (g *Gateway) handlePostMessage(ctx context.Context, msg *larkim.EventMessage, chatKey string) (string, error) {
	if msg.Content == nil {
		return "", fmt.Errorf("missing post content")
	}
	type body struct {
		Title   string          `json:"title"`
		Content [][]postElement `json:"content"`
	}
	var postBody body
	// Receive events use a direct body; some integrations send locale-wrapped bodies.
	if err := json.Unmarshal([]byte(*msg.Content), &postBody); err != nil || postBody.Content == nil {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(*msg.Content), &raw); err != nil {
			return "", fmt.Errorf("invalid post JSON")
		}
		keys := make([]string, 0, len(raw))
		for key := range raw {
			if key != "zh_cn" && key != "en_us" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		keys = append([]string{"zh_cn", "en_us"}, keys...)
		parsed := false
		for _, key := range keys {
			var candidate body
			if err := json.Unmarshal(raw[key], &candidate); err == nil && candidate.Content != nil {
				postBody = candidate
				parsed = true
				break
			}
		}
		if !parsed {
			return "", fmt.Errorf("missing post paragraphs")
		}
	}

	var parts []string
	if postBody.Title != "" {
		parts = append(parts, fmt.Sprintf("**%s**", postBody.Title))
	}

	for _, paragraph := range postBody.Content {
		var paraText string
		for _, elem := range paragraph {
			switch elem.Tag {
			case "text", "md":
				paraText += elem.Text
			case "code_block":
				paraText += "\n```" + elem.Language + "\n" + elem.Text + "\n```\n"
			case "hr":
				paraText += "\n---\n"
			case "a":
				paraText += fmt.Sprintf("[%s](%s)", elem.Text, elem.Href)
			case "at":
				paraText += elem.Text
			case "img":
				if elem.ImageKey == "" {
					return "", fmt.Errorf("missing post image key")
				}
				localPath, err := g.downloadResource(ctx, chatKey, derefStr(msg.MessageId), elem.ImageKey, "image", "feishu-image-"+elem.ImageKey+".png")
				if err != nil {
					slog.Warn("download post image failed", "imageKey", elem.ImageKey, "error", err)
					return "", fmt.Errorf("download post image: %w", err)
				}
				paraText += fmt.Sprintf(" ![image](%s) ", localPath)
			}
		}
		if strings.TrimSpace(paraText) != "" {
			parts = append(parts, paraText)
		}
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("post contains no readable content")
	}
	return strings.Join(parts, "\n"), nil
}

// postElement represents a single element in a post message paragraph.
type postElement struct {
	Tag      string `json:"tag"`
	Language string `json:"language"`
	Text     string `json:"text"`
	Href     string `json:"href"`
	ImageKey string `json:"image_key"`
}

// defaultOpenAPI is Feishu's open API root.
const defaultOpenAPI = "https://open.feishu.cn"

const maxMediaBytes = 50 << 20
const mediaDownloadTimeout = 2 * time.Minute

// downloadResource saves a resource of a message, of kind image or file, and
// returns its local path.
// API: GET /open-apis/im/v1/messages/{messageId}/resources/{key}?type=image|file
func (g *Gateway) downloadResource(ctx context.Context, chatKey, messageID, key, kind, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, mediaDownloadTimeout)
	defer cancel()
	dir := g.inboxDir(chatKey)
	if dir == "" {
		return "", fmt.Errorf("no valid inbox configured for this chat")
	}
	if err := checkMediaDirectory(dir); err != nil {
		return "", fmt.Errorf("unsafe media directory: %w", err)
	}
	if g.client == nil {
		return "", fmt.Errorf("no client configured for %s download", kind)
	}

	token, err := g.client.getTenantToken(ctx)
	if err != nil {
		return "", fmt.Errorf("get token: %w", err)
	}

	base := g.openAPI
	if base == "" {
		base = defaultOpenAPI
	}
	resourceURL := fmt.Sprintf("%s/open-apis/im/v1/messages/%s/resources/%s?type=%s",
		base, url.PathEscape(messageID), url.PathEscape(key), kind)
	req, err := http.NewRequestWithContext(ctx, "GET", resourceURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	httpClient := g.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return "", fmt.Errorf("download failed (HTTP %d): %s", resp.StatusCode, body)
	}
	if resp.ContentLength > maxMediaBytes {
		return "", fmt.Errorf("media exceeds 50 MiB limit")
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create media directory: %w", err)
	}
	if err := checkMediaDirectory(dir); err != nil {
		return "", fmt.Errorf("unsafe media directory: %w", err)
	}
	name = sanitizeFileName(name)
	ext := filepath.Ext(name)
	f, err := os.CreateTemp(dir, strings.TrimSuffix(name, ext)+"-*"+ext)
	if err != nil {
		return "", fmt.Errorf("create media file: %w", err)
	}
	localPath := f.Name()
	complete := false
	defer func() {
		_ = f.Close()
		if !complete {
			_ = os.Remove(localPath)
		}
	}()

	n, err := io.Copy(f, io.LimitReader(resp.Body, maxMediaBytes+1))
	if err != nil {
		return "", fmt.Errorf("save %s: %w", kind, err)
	}
	if n > maxMediaBytes {
		return "", fmt.Errorf("media exceeds 50 MiB limit")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := resp.Body.Close(); err != nil {
		return "", fmt.Errorf("close media response: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close media file: %w", err)
	}
	complete = true

	slog.Info("downloaded feishu resource", "kind", kind, "key", key, "path", localPath)
	return localPath, nil
}

// checkMediaDirectory rejects symlink components and non-directories, including
// dangling links, before creating an inbox. It allows missing descendants, not
// arbitrary relative paths. Concurrent filesystem replacement remains a TOCTOU.
func checkMediaDirectory(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("media directory must be absolute")
	}
	for current := filepath.Clean(dir); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && !info.IsDir() {
			return fmt.Errorf("media directory component is not a directory: %s", current)
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

// inboxDir uses temp storage only when no inbox resolver is installed.
func (g *Gateway) inboxDir(chatKey string) string {
	if g.inbox != nil {
		return g.configuredInbox(chatKey)
	}
	// Canonicalize OS temp aliases such as macOS /var before checking them.
	dir, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return ""
	}
	return dir
}

// configuredInbox is the inbox inside the chat's workspace, or "" when the
// chat has none.
func (g *Gateway) configuredInbox(chatKey string) string {
	if g.inbox == nil {
		return ""
	}
	return g.inbox(chatKey)
}

// extractText parses the message content JSON to get the text field.
func extractText(content *string) string {
	if content == nil {
		return ""
	}

	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(*content), &parsed); err != nil {
		return *content
	}
	return parsed.Text
}

// ChatKey returns the routing key for a message:
// group chat → chatID, direct chat → senderOpenID.
func (m *Message) ChatKey() string {
	if m.ChatType == "group" && m.ChatID != "" {
		return m.ChatID
	}
	if m.SenderOpenID != "" {
		return m.SenderOpenID
	}
	return m.ChatID
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (g *Gateway) SetOwnerAccess(access *OwnerAccess) { g.access = access }
