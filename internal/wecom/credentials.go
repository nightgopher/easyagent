package wecom

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nightgopher/easyagent/sdk/config"
)

// Credentials is the persisted smart-bot identity from a confirmed QR scan.
type Credentials struct {
	BotID     string `json:"bot_id"`
	BotSecret string `json:"bot_secret"`
}

// credentialsPath returns the path to the wecom credentials file.
func credentialsPath() string {
	return filepath.Join(config.HomeDir(), "wecom-credentials.json")
}

// SaveCredentials writes wecom credentials to ~/.easyagent/wecom-credentials.json.
func SaveCredentials(creds Credentials) error {
	dir := filepath.Dir(credentialsPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create credentials dir: %w", err)
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	f, err := os.CreateTemp(dir, ".wecom-credentials-*")
	if err != nil {
		return fmt.Errorf("create credentials: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write credentials: %w", err)
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync credentials: %w", err)
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), credentialsPath()); err != nil {
		return fmt.Errorf("replace credentials: %w", err)
	}

	return nil
}

// LoadCredentials reads wecom credentials from disk.
// Returns nil if the file does not exist.
func LoadCredentials() (*Credentials, error) {
	data, err := os.ReadFile(credentialsPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if creds.BotID == "" || creds.BotSecret == "" {
		return nil, fmt.Errorf("credentials file is incomplete")
	}
	return &creds, nil
}

// DeleteCredentials removes the wecom credentials file.
// Returns nil if the file does not exist.
func DeleteCredentials() error {
	if err := os.Remove(credentialsPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete credentials: %w", err)
	}
	return nil
}
