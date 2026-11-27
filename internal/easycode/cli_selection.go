package easycode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

func (m *Manager) selectionLocked(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(m.selectionDir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(m.selectionDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("EasyCode CLI 设置目录必须仅当前用户可访问")
	}
	unlock, err := lockCredentials(ctx, filepath.Join(m.selectionDir, ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

const cliSelectionFile = "cli-selection.json"

type cliSelection struct {
	Model string `json:"model"`
}

func validCLIModel(model string) bool {
	return model != "" && len(model) <= 200 && strings.TrimSpace(model) == model && strings.IndexFunc(model, unicode.IsControl) < 0
}

// CLIModel returns the explicitly selected CLI model without changing the
// desktop or service configuration. A missing selection means CLI mode is off.
func (m *Manager) CLIModel(ctx context.Context) (string, error) {
	path := filepath.Join(m.selectionDir, cliSelectionFile)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	var selection cliSelection
	err := m.selectionLocked(ctx, func() error {
		file, err := openCredential(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return errors.New("无法安全读取 EasyCode CLI 设置")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("无法安全读取 EasyCode CLI 设置")
		}
		if json.NewDecoder(io.LimitReader(file, 4096)).Decode(&selection) != nil || !validCLIModel(selection.Model) {
			return errors.New("EasyCode CLI 设置无效，请重新选择模型")
		}
		return nil
	})
	return selection.Model, err
}

// SetCLIModel selects EasyCode for future CLI runs without overwriting the
// user's previous provider and model settings.
func (m *Manager) SetCLIModel(ctx context.Context, model string) error {
	if !validCLIModel(model) {
		return errors.New("EasyCode 模型 ID 无效")
	}
	return m.selectionLocked(ctx, func() error {
		if err := os.MkdirAll(m.selectionDir, 0700); err != nil {
			return err
		}
		data, err := json.Marshal(cliSelection{Model: model})
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(m.selectionDir, ".cli-selection-*")
		if err != nil {
			return errors.New("无法保存 EasyCode CLI 设置")
		}
		defer os.Remove(file.Name())
		if err = file.Chmod(0o600); err == nil {
			_, err = file.Write(data)
		}
		if err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil || os.Rename(file.Name(), filepath.Join(m.selectionDir, cliSelectionFile)) != nil {
			return errors.New("无法保存 EasyCode CLI 设置")
		}
		return nil
	})
}

// ClearCLIModel restores the previous CLI provider configuration.
func (m *Manager) ClearCLIModel(ctx context.Context) error {
	return m.selectionLocked(ctx, func() error {
		err := os.Remove(filepath.Join(m.selectionDir, cliSelectionFile))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	})
}
