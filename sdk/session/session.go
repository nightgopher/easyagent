package session

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nightgopher/easyagent/sdk/ai"
)

type Session struct {
	storage SessionStorage
	leafID  string // 当前 leaf entry ID，用于链式追加和恢复
}

func New(storage SessionStorage) *Session {
	return &Session{storage: storage}
}

// Storage returns the underlying storage backend.
func (s *Session) Storage() SessionStorage {
	return s.storage
}

// SubagentUsage sums durable tool results after a cursor on the active branch.
// An empty cursor includes the whole branch. Compaction snapshots are not counted
// again: original entries remain in the append-only storage path.
func (s *Session) SubagentUsage(ctx context.Context, afterID string) (ai.Usage, error) {
	entries, err := s.storage.GetPathToRoot(ctx, "")
	if err != nil {
		return ai.Usage{}, err
	}
	var total ai.Usage
	found := afterID == ""
	for _, entry := range entries {
		if !found {
			found = entry.ID == afterID
			continue
		}
		if entry.Type == EntryTypeMessage && entry.Tool != nil && entry.Tool.SubagentUsage != nil {
			total.Add(entry.Tool.SubagentUsage.Usage)
		}
	}
	if !found {
		return ai.Usage{}, fmt.Errorf("usage cursor %q is not on the active branch", afterID)
	}
	return total, nil
}

// InitFromStorage 从 storage 中加载当前 leaf，用于恢复已有会话。
func (s *Session) InitFromStorage(ctx context.Context) error {
	leaf, err := s.storage.GetLeaf(ctx)
	if err != nil {
		return err
	}
	s.leafID = leaf
	return nil
}

func (s *Session) AppendMessage(ctx context.Context, msg ai.Message) error {
	entry := Entry{Type: EntryTypeMessage, ParentID: s.leafID}
	switch m := msg.(type) {
	case ai.UserMessage:
		entry.User = &m
	case ai.AssistantMessage:
		entry.Assistant = &m
	case ai.ToolResultMessage:
		entry.Tool = &m
	default:
		return fmt.Errorf("unsupported message type %T", msg)
	}
	if err := s.storage.Append(ctx, entry); err != nil {
		return err
	}
	// 获取 storage 内部分配的 entry ID（Append 已更新 leafID）
	leaf, err := s.storage.GetLeaf(ctx)
	if err != nil {
		return fmt.Errorf("failed to get leaf after append: %w", err)
	}
	s.leafID = leaf
	// Explicit cursor record preserves compatibility with older readers.
	if err := s.storage.SetLeaf(ctx, leaf); err != nil {
		return fmt.Errorf("failed to set leaf: %w", err)
	}
	return nil
}

// contextEntries reconstructs the logical context and preserves source IDs.
func (s *Session) contextEntries(ctx context.Context) ([]Entry, error) {
	return s.buildContextEntries(ctx, true)
}

func (s *Session) buildContextEntries(ctx context.Context, applyMicro bool) ([]Entry, error) {
	entries, err := s.storage.GetPathToRoot(ctx, "")
	if err != nil {
		return nil, err
	}
	return contextFromEntries(entries, applyMicro, true), nil
}

// HistoryFromEntries reconstructs message history from one immutable storage
// snapshot. Compaction summaries and storage markers are not conversation turns.
// Source entry IDs and micro-compacted tool contents are preserved.
func HistoryFromEntries(entries []Entry) []Entry {
	return contextFromEntries(entries, true, false)
}

func contextFromEntries(entries []Entry, applyMicro, includeSummary bool) []Entry {
	start := 0
	var result []Entry
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == EntryTypeCompaction {
			if includeSummary {
				msg := ai.NewTextUserMessage("Context summary from previous conversation:\n\n" + entries[i].Summary)
				result = append(result, Entry{ID: entries[i].ID, Type: EntryTypeMessage, User: &msg})
			}
			result = append(result, entries[i].Retained...)
			start = i + 1
			break
		}
	}
	replacements := make(map[string]string)
	clearImages := make(map[string]bool)
	originalImages := make(map[string][]ai.ImageBlock)
	for _, entry := range entries[start:] {
		if applyMicro && entry.Type == EntryTypeMicroCompaction {
			for _, replacement := range entry.ToolReplacements {
				replacements[replacement.EntryID] = replacement.Content
				clearImages[replacement.EntryID] = clearImages[replacement.EntryID] || replacement.ClearImages
			}
		}
		if len(entryToMessages(entry)) > 0 {
			result = append(result, entry)
		}
	}
	if !applyMicro {
		// A full-compaction tail may already contain cleaned tool contents.
		// Restore their original contents for display, keeping the logical tail.
		for _, entry := range entries {
			if entry.Type == EntryTypeMessage && entry.Tool != nil {
				replacements[entry.ID] = entry.Tool.Content
				originalImages[entry.ID] = entry.Tool.Images
			}
		}
	}
	for i, entry := range result {
		if content, ok := replacements[entry.ID]; ok && entry.Tool != nil {
			tool := *entry.Tool
			tool.Content = content
			if applyMicro && clearImages[entry.ID] {
				tool.Images = nil
			}
			if !applyMicro {
				tool.Images = originalImages[entry.ID]
			}
			result[i].Tool = &tool
		}
	}
	return result
}

func (s *Session) BuildContext(ctx context.Context) ([]ai.Message, error) {
	entries, err := s.contextEntries(ctx)
	if err != nil {
		return nil, err
	}
	messages := make([]ai.Message, 0, len(entries))
	for _, entry := range entries {
		messages = append(messages, entryToMessages(entry)...)
	}
	return messages, nil
}

// Compactions returns full compactions on the active branch, oldest first.
func (s *Session) Compactions(ctx context.Context) ([]CompactionRecord, error) {
	entries, err := s.storage.GetPathToRoot(ctx, "")
	if err != nil {
		return nil, err
	}
	records := make([]CompactionRecord, 0)
	for _, entry := range entries {
		if entry.Type == EntryTypeCompaction {
			records = append(records, CompactionRecord{ID: entry.ID, Timestamp: entry.Timestamp, Summary: entry.Summary, Info: entry.Compaction})
		}
	}
	return records, nil
}

// entryToMessages extracts ai.Message values from an Entry.
func entryToMessages(entry Entry) []ai.Message {
	switch {
	case entry.User != nil:
		return []ai.Message{*entry.User}
	case entry.Assistant != nil:
		return []ai.Message{*entry.Assistant}
	case entry.Tool != nil:
		return []ai.Message{*entry.Tool}
	}
	return nil
}

// BuildContextEntryIDs is position-aligned with BuildContext, including retained
// messages from compaction snapshots. The summary uses its compaction entry ID.
func (s *Session) BuildContextEntryIDs(ctx context.Context) ([]string, error) {
	entries, err := s.contextEntries(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		for range entryToMessages(entry) {
			ids = append(ids, entry.ID)
		}
	}
	return ids, nil
}

// AppendCompaction writes a compaction entry to the session storage.
// The summary replaces all prior messages when BuildContext is called.
func (s *Session) AppendCompaction(ctx context.Context, summary string) error {
	return s.AppendCompactionKeeping(ctx, summary, nil, nil)
}

// AppendCompactionKeeping atomically persists the summary and retained tail in
// one entry. Snapshots keep source IDs and any micro-compacted tool contents.
func (s *Session) AppendCompactionKeeping(ctx context.Context, summary string, recent []ai.Message, info *CompactionInfo) error {
	entries, err := s.contextEntries(ctx)
	if err != nil {
		return err
	}
	if len(recent) > len(entries) {
		return fmt.Errorf("retained context exceeds stored history")
	}
	retained := make([]Entry, 0, len(recent))
	for i, msg := range recent {
		original := entries[len(entries)-len(recent)+i]
		entry := Entry{ID: original.ID, Timestamp: original.Timestamp, Type: EntryTypeMessage}
		switch m := msg.(type) {
		case ai.UserMessage:
			entry.User = &m
		case ai.AssistantMessage:
			entry.Assistant = &m
		case ai.ToolResultMessage:
			entry.Tool = &m
		default:
			return fmt.Errorf("unsupported retained message %T", msg)
		}
		retained = append(retained, entry)
	}
	entry := Entry{Type: EntryTypeCompaction, ParentID: s.leafID, Summary: summary, Retained: retained, Compaction: info}
	if len(retained) > 0 {
		entry.FirstKeptEntryID = retained[0].ID
	}
	// JSONL readers have a 16 MiB record limit. Reject before writing, rather
	// than producing a session that cannot be reopened (e.g. large image tails).
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if len(data) >= 16*1024*1024-1024 {
		return fmt.Errorf("retained compaction context exceeds storage record limit")
	}
	if err := s.storage.Append(ctx, entry); err != nil {
		return err
	}
	leaf, err := s.storage.GetLeaf(ctx)
	if err != nil {
		return fmt.Errorf("failed to get leaf after compaction: %w", err)
	}
	// Persist leaf pointer so compaction survives session reload,
	// matching the pattern in AppendMessage().
	if err := s.storage.SetLeaf(ctx, leaf); err != nil {
		return fmt.Errorf("failed to set leaf after compaction: %w", err)
	}
	s.leafID = leaf
	return nil
}

func (s *Session) MoveTo(ctx context.Context, entryID string, summary string) error {
	if err := s.storage.SetLeaf(ctx, entryID); err != nil {
		return err
	}
	s.leafID = entryID
	if summary != "" {
		if err := s.storage.Append(ctx, Entry{Type: EntryTypeBranchSummary, Summary: summary, ParentID: s.leafID}); err != nil {
			return err
		}
		// summary entry 追加后，leaf 指向该 entry
		leaf, err := s.storage.GetLeaf(ctx)
		if err != nil {
			return fmt.Errorf("failed to get leaf after summary append: %w", err)
		}
		s.leafID = leaf
	}
	return nil
}

// BuildContextSnapshot returns messages and IDs from the same storage snapshot.
func (s *Session) BuildContextSnapshot(ctx context.Context) ([]ai.Message, []string, error) {
	entries, err := s.contextEntries(ctx)
	if err != nil {
		return nil, nil, err
	}
	messages := make([]ai.Message, 0, len(entries))
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		for _, m := range entryToMessages(e) {
			messages = append(messages, m)
			ids = append(ids, e.ID)
		}
	}
	return messages, ids, nil
}
