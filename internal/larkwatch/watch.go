package larkwatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// pollDepth 是单会话每次巡查拉取的消息条数；窗口溢出时记日志告警。
const pollDepth = 6

// pollConcurrency 是巡查会话消息的并发上限，避免大量单聊时一轮拖得过久。
const pollConcurrency = 8

// chatRead 是单个会话的巡查结果。
type chatRead struct {
	chat P2PChat
	msgs []Message
	err  error
}

// maxRelayRunes 是转达给主人的最终文本长度上限，超出截断。
const maxRelayRunes = 3000

// Watch 是飞书看门狗：用 lark-cli 用户身份廉价巡查主人单聊的新消息，
// 发现新消息才唤醒管家会话（核心 /chat，忙时转 steer），并把处理结果
// 以机器人身份转达给主人。模型只在有事时醒来，没事不叫醒。
type Watch struct {
	cfg       Config
	lark      *Lark
	inject    *Injector
	state     *State
	statePath string
	selfID    string
	relayChat string
	log       *slog.Logger
}

// New 构造看门狗。runner 通常为 ExecRunner；injector 指向核心服务。
func New(cfg Config, runner Runner, injector *Injector, statePath string, logger *slog.Logger) (*Watch, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Watch{
		cfg:       cfg,
		lark:      NewLark(runner),
		inject:    injector,
		state:     NewState(),
		statePath: statePath,
		log:       logger,
	}, nil
}

// watchable 判断会话是否在巡查范围：排除机器人会话与显式排除清单。
func (w *Watch) watchable(c P2PChat) bool {
	return !c.IsBotChat && !w.cfg.ExcludeChats[c.ChatID]
}

// init 启动准备：识别主人与机器人转达会话，并把现有消息记为基线（不触发）。
func (w *Watch) init(ctx context.Context) error {
	self, err := w.lark.SelfOpenID(ctx)
	if err != nil {
		return fmt.Errorf("resolve self open_id: %w", err)
	}
	w.selfID = self

	botID := w.lark.BotOpenID(ctx)
	chats, err := w.lark.P2PChats(ctx)
	if err != nil {
		return fmt.Errorf("list p2p chats: %w", err)
	}
	if botID != "" {
		for _, c := range chats {
			if c.IsBotChat && c.TargetID == botID {
				w.relayChat = c.ChatID
				break
			}
		}
	}
	if w.relayChat == "" {
		w.log.Warn("no relay chat found; agent replies will not be forwarded to Feishu")
	}

	var baselineTargets []P2PChat
	for _, c := range chats {
		if w.watchable(c) {
			baselineTargets = append(baselineTargets, c)
		}
	}
	baseline := w.readChats(ctx, baselineTargets)
	for _, r := range baseline {
		if r.err != nil {
			w.log.Warn("baseline read failed", "chat", r.chat.ChatID, "error", r.err)
			continue
		}
		if len(r.msgs) > 0 {
			w.state.Advance(r.chat.ChatID, r.msgs[0].Position)
		}
	}
	if err := w.saveState(); err != nil {
		return err
	}
	w.log.Info("lark watch initialized",
		"session", w.cfg.SessionID,
		"self", w.selfID,
		"relay_chat", w.relayChat,
		"interval", w.cfg.Interval.String(),
		"watched_chats", len(w.state.Chats))
	return nil
}

// Run 阻塞运行巡查循环，直到 ctx 取消。OneShot 配置下只巡查一轮。
func (w *Watch) Run(ctx context.Context) error {
	if err := w.init(ctx); err != nil {
		return err
	}
	if w.cfg.OneShot {
		pollCtx, cancel := context.WithTimeout(ctx, w.cfg.RunTimeout)
		defer cancel()
		return w.Poll(pollCtx)
	}
	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.log.Info("lark watch stopped")
			return nil
		case <-ticker.C:
			pollCtx, cancel := context.WithTimeout(ctx, w.cfg.RunTimeout)
			if err := w.Poll(pollCtx); err != nil {
				w.log.Error("poll failed", "error", err)
			}
			cancel()
		}
	}
}

// sighting 是一个会话里新发现的消息（按时间正序）。
type sighting struct {
	chat P2PChat
	msgs []Message
}

// readChats 并发读取多个会话的最新消息，结果顺序与输入一致。
func (w *Watch) readChats(ctx context.Context, chats []P2PChat) []chatRead {
	results := make([]chatRead, len(chats))
	var wg sync.WaitGroup
	sem := make(chan struct{}, pollConcurrency)
	for i, c := range chats {
		wg.Add(1)
		go func(i int, c P2PChat) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			msgs, err := w.lark.LatestMessages(ctx, c.ChatID, pollDepth)
			results[i] = chatRead{chat: c, msgs: msgs, err: err}
		}(i, c)
	}
	wg.Wait()
	return results
}

// Poll 执行一轮巡查：发现新消息则唤醒会话并转达结果。
// 返回错误时不动位点，下一轮自动重试同样的消息。
func (w *Watch) Poll(ctx context.Context) error {
	chats, err := w.lark.P2PChats(ctx)
	if err != nil {
		return fmt.Errorf("list p2p chats: %w", err)
	}

	var watchables []P2PChat
	for _, c := range chats {
		if w.watchable(c) {
			watchables = append(watchables, c)
		}
	}
	results := w.readChats(ctx, watchables)

	maxPos := map[string]int64{}
	var sightings []sighting
	total := 0
	for _, r := range results {
		if r.err != nil {
			w.log.Warn("read messages failed", "chat", r.chat.ChatID, "error", r.err)
			continue
		}
		msgs := r.msgs
		if len(msgs) == 0 {
			continue
		}
		last := w.state.Position(r.chat.ChatID)
		top := maxPos[r.chat.ChatID]
		var fresh []Message
		for _, m := range msgs { // 新→旧
			if m.Position > top {
				top = m.Position
			}
			if m.Position <= last || m.Deleted || m.SenderID == w.selfID {
				continue
			}
			fresh = append(fresh, m)
		}
		maxPos[r.chat.ChatID] = top
		if len(msgs) == pollDepth && msgs[pollDepth-1].Position > last {
			w.log.Warn("message window overflow; older messages skipped", "chat", r.chat.ChatID, "last_seen", last)
		}
		if len(fresh) > 0 {
			// LatestMessages 返回新→旧，转成正序便于阅读。
			reverse(fresh)
			sightings = append(sightings, sighting{chat: r.chat, msgs: fresh})
			total += len(fresh)
		}
	}

	if total == 0 {
		w.advanceAndSave(maxPos)
		return nil
	}

	selected, shown := capSightings(sightings, w.cfg.MaxPerWake)
	prompt := buildPrompt(selected, total)
	if shown < total {
		prompt = fmt.Sprintf("（另有 %d 条较早消息未列出）\n\n%s", total-shown, prompt)
	}

	text, err := w.inject.Inject(ctx, w.cfg.SessionID, prompt, requestID(selected))
	if errors.Is(err, ErrBusy) {
		w.log.Info("session busy; steering interjection instead")
		text = ""
		err = w.inject.Steer(ctx, w.cfg.SessionID, prompt)
	}
	if err != nil && !errors.Is(err, ErrDuplicate) {
		return fmt.Errorf("wake session: %w", err) // 位点不动，下轮重试
	}
	if errors.Is(err, ErrDuplicate) {
		w.log.Info("duplicate request id; skipping relay")
		text = ""
	}

	w.advanceAndSave(maxPos)

	if text != "" && w.relayChat != "" {
		if rerr := w.lark.SendToChat(ctx, w.relayChat, truncateRunes(text, maxRelayRunes), "bot"); rerr != nil {
			w.log.Warn("relay reply failed", "error", rerr)
		}
	}
	return nil
}

// advanceAndSave 推进本轮看到的全部位点并落盘；落盘失败只记日志（位点在内存中仍生效）。
func (w *Watch) advanceAndSave(maxPos map[string]int64) {
	for id, pos := range maxPos {
		w.state.Advance(id, pos)
	}
	if err := w.saveState(); err != nil {
		w.log.Warn("save watch state failed", "error", err)
	}
}

func (w *Watch) saveState() error {
	if w.statePath == "" {
		return nil
	}
	return w.state.Save(w.statePath)
}

// capSightings 把全部新消息铺平后按时间新→旧保留最新 maxN 条，再按会话重新分组（组内恢复正序）。
func capSightings(sightings []sighting, maxN int) ([]sighting, int) {
	total := 0
	type flatMessage struct {
		chat P2PChat
		msg  Message
	}
	var flat []flatMessage
	for _, s := range sightings {
		total += len(s.msgs)
		for _, m := range s.msgs {
			flat = append(flat, flatMessage{chat: s.chat, msg: m})
		}
	}
	if total <= maxN {
		return sightings, total
	}
	// create_time 形如 "2006-01-02 15:04"，字典序即时间序。
	sort.Slice(flat, func(i, j int) bool { return flat[i].msg.Time > flat[j].msg.Time })
	flat = flat[:maxN]

	var out []sighting
	index := map[string]int{}
	shown := 0
	for _, f := range flat {
		i, ok := index[f.chat.ChatID]
		if !ok {
			out = append(out, sighting{chat: f.chat})
			i = len(out) - 1
			index[f.chat.ChatID] = i
		}
		out[i].msgs = append(out[i].msgs, f.msg)
		shown++
	}
	for i := range out { // 分组时是新→旧，转回正序
		reverse(out[i].msgs)
	}
	return out, shown
}

func buildPrompt(items []sighting, total int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【飞书看门狗】发现 %d 条新消息：\n", total)
	for _, s := range items {
		chatLabel := s.chat.Name
		if chatLabel == "" {
			chatLabel = s.chat.ChatID
		}
		for _, m := range s.msgs {
			// 富文本（post）等类型经 lark-cli 已解析出可读文字，直接携带；
			// 仅当确实没有文字内容（图片/文件等）时标注消息类型。
			content := strings.TrimSpace(m.Content)
			if content == "" {
				kind := m.MsgType
				if kind == "" {
					kind = "unknown"
				}
				content = "[" + kind + " 消息，无文字内容]"
			}
			fmt.Fprintf(&b, "- %s（%s，来自会话「%s」）：%s\n", senderLabel(m), m.Time, chatLabel, content)
		}
	}
	b.WriteString("\n请按 butler-rules 铁律处理：先判断是否需要回复；能直接回的以我的身份直接回复；涉及承诺、钱、合同、人事或拿不准的，先把拟稿发给我确认。完成后给出一句话处理摘要。")
	return b.String()
}

func senderLabel(m Message) string {
	if m.Sender != "" {
		return m.Sender
	}
	return m.SenderID
}

// requestID 由选中的消息集合生成稳定 ID：同一批消息重试不会重复执行任务。
func requestID(items []sighting) string {
	lines := make([]string, 0, 16)
	for _, s := range items {
		for _, m := range s.msgs {
			lines = append(lines, fmt.Sprintf("%s:%d:%s", s.chat.ChatID, m.Position, m.ID))
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return "larkwatch-" + hex.EncodeToString(sum[:])[:16]
}

func reverse(msgs []Message) {
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
}

// truncateRunes 按字符（而非字节）截断，附加省略标记。
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…（截断）"
}
