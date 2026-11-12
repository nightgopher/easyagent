package larkwatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ChatState 记录单个会话已处理到的消息位点。
type ChatState struct {
	LastPosition int64 `json:"last_position"`
}

// State 是看门狗的持久化状态：每个会话的最后位点。
type State struct {
	Chats map[string]ChatState `json:"chats"`
}

// NewState 返回空状态。
func NewState() *State {
	return &State{Chats: map[string]ChatState{}}
}

// LoadState 从磁盘读取状态；文件不存在时返回空状态而非错误。
func LoadState(path string) (*State, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path 是看门狗自己的状态文件，来自显式配置而非外部输入
	if err != nil {
		if os.IsNotExist(err) {
			return NewState(), nil
		}
		return nil, fmt.Errorf("read watch state: %w", err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("parse watch state %s: %w", path, err)
	}
	if st.Chats == nil {
		st.Chats = map[string]ChatState{}
	}
	return &st, nil
}

// Save 把状态原子写回磁盘（临时文件 + rename），权限 0600。
func (s *State) Save(path string) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create state temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write state temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}

// Position 返回会话的已处理位点，未知会话返回 0。
func (s *State) Position(chatID string) int64 {
	return s.Chats[chatID].LastPosition
}

// Advance 推进会话位点。
func (s *State) Advance(chatID string, position int64) {
	if position <= s.Chats[chatID].LastPosition {
		return
	}
	s.Chats[chatID] = ChatState{LastPosition: position}
}
