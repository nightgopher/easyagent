// Package accountstore selects local account credentials without copying tokens.
package accountstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Status struct {
	Shared   bool   `json:"shared"`
	Desktop  bool   `json:"desktop"`
	Selected string `json:"selected"`
	Conflict bool   `json:"conflict"`
}
type selection struct {
	Source string `json:"source"`
}

func paths(home string) (shared, desktop string, isDesktop bool, err error) {
	if strings.TrimSpace(home) == "" {
		return "", "", false, errors.New("account home is empty")
	}
	desktop = os.Getenv("EA_DESKTOP_ACCOUNT_HOME")
	shared = os.Getenv("EA_SHARED_ACCOUNT_HOME")
	isDesktop = desktop != ""
	if isDesktop {
		if shared == "" {
			return "", "", false, errors.New("shared account home is unavailable")
		}
		return
	}
	shared = home
	userHome, e := os.UserHomeDir()
	if e != nil || filepath.Clean(home) != filepath.Join(userHome, ".easyagent") || os.Getenv("EA_HOME") != "" {
		desktop = home
		return
	}
	d, e := os.UserConfigDir()
	if e != nil {
		desktop = home
		return
	}
	desktop = filepath.Join(d, "EasyAgent", "core-home")
	return
}
func validProvider(provider string) bool { return provider == "codex" || provider == "easycode" }
func exists(root, provider string) (bool, error) {
	info, e := os.Lstat(filepath.Join(root, provider, "credentials.json"))
	if os.IsNotExist(e) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return false, fmt.Errorf("%s credentials cannot be read safely", provider)
	}
	return true, nil
}
func GetStatus(home, provider string) (Status, error) {
	var s Status
	if !validProvider(provider) {
		return s, errors.New("unknown account provider")
	}
	shared, desktop, isDesktop, err := paths(home)
	if err != nil {
		return s, err
	}
	s.Shared, err = exists(shared, provider)
	if err != nil {
		return s, err
	}
	s.Desktop, err = exists(desktop, provider)
	if err != nil {
		return s, err
	}
	if filepath.Clean(shared) == filepath.Clean(desktop) {
		s.Desktop = false
	}
	s.Conflict = s.Shared && s.Desktop
	file := filepath.Join(home, "account-source-"+provider+".json")
	info, e := os.Lstat(file)
	if e == nil {
		if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) || info.Size() > 1024 {
			return s, errors.New("account selection cannot be read safely")
		}
		f, e := os.Open(file)
		if e != nil {
			return s, e
		}
		defer f.Close()
		var choice selection
		if json.NewDecoder(io.LimitReader(f, 1024)).Decode(&choice) != nil || (choice.Source != "shared" && choice.Source != "desktop") {
			return s, errors.New("account selection is invalid")
		}
		s.Selected = choice.Source
		return s, nil
	}
	if !os.IsNotExist(e) {
		return s, e
	}
	if s.Conflict {
		return s, nil
	}
	if isDesktop && s.Desktop {
		s.Selected = "desktop"
	} else if s.Shared {
		s.Selected = "shared"
	} else if s.Desktop {
		s.Selected = "desktop"
	} else {
		s.Selected = "shared"
	}
	return s, nil
}
func Resolve(home, provider string) (string, error) {
	status, err := GetStatus(home, provider)
	if err != nil {
		return "", err
	}
	if status.Selected == "" {
		return "", fmt.Errorf("检测到桌面和命令行均有 %s 账号，请选择账号（easyagent accounts use %s shared|desktop）", provider, provider)
	}
	shared, desktop, _, err := paths(home)
	if err != nil {
		return "", err
	}
	if status.Selected == "desktop" {
		return desktop, nil
	}
	return shared, nil
}

// Select records the client's choice, preserving both credential files.
func Select(home, provider, source string) error {
	if !validProvider(provider) || (source != "desktop" && source != "shared") {
		return errors.New("invalid account selection")
	}
	status, err := GetStatus(home, provider)
	if err != nil {
		return err
	}
	if source == "desktop" && !status.Desktop || source == "shared" && !status.Shared {
		return errors.New("selected account is not logged in")
	}
	if err = os.MkdirAll(home, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(home, ".account-source-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	data, _ := json.Marshal(selection{Source: source})
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(home, "account-source-"+provider+".json"))
}
