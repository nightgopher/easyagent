package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestEasyCodeLoginPageCanBeCancelled(t *testing.T) {
	oldHelper, oldOpen := invokeEasyCodeHelper, openEasyCodeBrowser
	t.Cleanup(func() { invokeEasyCodeHelper, openEasyCodeBrowser = oldHelper, oldOpen })
	var cancelled atomic.Bool
	invokeEasyCodeHelper = func(ctx context.Context, _, action string, onBrowser func(string)) (nativeEasyCodeEvent, error) {
		if action == "status" {
			return nativeEasyCodeEvent{Type: "status"}, nil
		}
		if action == "login" {
			onBrowser("http://127.0.0.1:7863/?state=fixture")
			<-ctx.Done()
			cancelled.Store(true)
			return nativeEasyCodeEvent{}, ctx.Err()
		}
		return nativeEasyCodeEvent{}, fmt.Errorf("unexpected operation")
	}
	openEasyCodeBrowser = func(string) error { return nil }
	a := alignmentPreview(t)
	a.current().Messages = nil
	tt := ui.NewTester(a.View, 1240, 1200)
	click(t, tt, "飞书登录 EasyCode")
	if !a.Settings.Open || a.Settings.Tab != "models" || a.Settings.Mode != SettingCopyEasyCodeMode {
		t.Fatal("first-run sign-in did not open account settings")
	}
	waitNative(t, a, func() bool { return !a.Live.Loading["easycode-status"] })
	tt.Frame()
	click(t, tt, "飞书登录")
	waitNative(t, a, func() bool { return a.Live.EasyCode.URL != "" })
	tt.Frame()
	if !tt.HasText("取消登录") || !tt.HasText("打开登录页面") {
		t.Fatal("missing login controls")
	}
	click(t, tt, "取消登录")
	deadline := time.Now().Add(time.Second)
	for !cancelled.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !cancelled.Load() || a.Live.Config.Model.Mode != "inherit" || a.Live.EasyCode.LoggedIn {
		t.Fatal("cancel changed model or account")
	}
}

func TestEasyCodeLoginDoesNotReplaceAccountDuringRun(t *testing.T) {
	oldHelper := invokeEasyCodeHelper
	t.Cleanup(func() { invokeEasyCodeHelper = oldHelper })
	var calls atomic.Int32
	invokeEasyCodeHelper = func(context.Context, string, string, func(string)) (nativeEasyCodeEvent, error) {
		calls.Add(1)
		return nativeEasyCodeEvent{}, nil
	}
	a := alignmentPreview(t)
	a.current().Running = true
	a.startEasyCodeLogin()
	if calls.Load() != 0 || a.Live.Loading["easycode-login"] || a.Live.Error == "" {
		t.Fatal("login started during active work")
	}
	a.current().Running = false
}

func TestEasyCodeExistingAccountMustBeSignedOutBeforeLogin(t *testing.T) {
	oldHelper := invokeEasyCodeHelper
	t.Cleanup(func() { invokeEasyCodeHelper = oldHelper })
	var logins atomic.Int32
	invokeEasyCodeHelper = func(_ context.Context, _, action string, _ func(string)) (nativeEasyCodeEvent, error) {
		if action == "login" {
			logins.Add(1)
		}
		return nativeEasyCodeEvent{Type: "status", LoggedIn: true}, nil
	}
	a := alignmentPreview(t)
	a.startEasyCodeLogin()
	waitNative(t, a, func() bool { return !a.Live.Loading["easycode-login"] })
	if logins.Load() != 0 || !a.Live.EasyCode.LoggedIn || a.Live.Error == "" {
		t.Fatal("existing account could be overwritten")
	}
}

func TestDamagedEasyCodeCredentialHasClearAction(t *testing.T) {
	oldHelper := invokeEasyCodeHelper
	t.Cleanup(func() { invokeEasyCodeHelper = oldHelper })
	var cleared atomic.Bool
	invokeEasyCodeHelper = func(_ context.Context, _, action string, _ func(string)) (nativeEasyCodeEvent, error) {
		if action == "status" {
			return nativeEasyCodeEvent{}, fmt.Errorf("credential unavailable")
		}
		if action == "logout" {
			cleared.Store(true)
			return nativeEasyCodeEvent{Type: "logout"}, nil
		}
		return nativeEasyCodeEvent{}, fmt.Errorf("unexpected operation")
	}
	a := alignmentPreview(t)
	a.Settings.Open = true
	a.Settings.Tab = "models"
	a.Settings.Mode = SettingCopyEasyCodeMode
	a.refreshEasyCodeStatus()
	waitNative(t, a, func() bool { return !a.Live.Loading["easycode-status"] })
	tt := ui.NewTester(a.View, 1240, 1000)
	click(t, tt, "清除 EasyCode 登录信息")
	waitNative(t, a, func() bool { return !a.Live.Loading["easycode-logout"] })
	if !cleared.Load() || a.Live.EasyCode.NeedsRepair {
		t.Fatal("damaged credential could not be cleared")
	}
}

func TestEasyCodeSettingsShowAvailableModels(t *testing.T) {
	for _, width := range []int{900, 1240} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := alignmentPreview(t)
			a.Settings.Open = true
			a.Settings.Tab = "models"
			a.Settings.Mode = SettingCopyEasyCodeMode
			a.Live.EasyCode.LoggedIn = true
			a.Live.EasyCode.Name = "Example User"
			a.Live.EasyCode.Models = []nativeEasyCodeModel{{ID: "deepseek-flash"}, {ID: "glm-5.3-flash"}}
			tt := ui.NewTester(a.View, width, 900)
			for _, label := range []string{"Example User", "退出 EasyCode", "EasyCode 默认模型", "deepseek-flash"} {
				if !tt.HasText(label) {
					t.Fatalf("missing %q", label)
				}
			}
			if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
				saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("easycode-settings-%d.png", width)))
			}
		})
	}
}

func TestEasyCodeLoginAppliesModelAndLogoutRestoresPrevious(t *testing.T) {
	a, _, _, _ := nativeCoreFixture(t)
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer user-token" {
			t.Errorf("request %s did not use the account", r.URL.Path)
		}
		switch r.URL.Path {
		case "/web-api/models":
			fmt.Fprint(w, `{"success":true,"data":[{"name":"deepseek-flash","available":true}]}`)
		case "/v1/chat/stream":
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"account reply\"}]},\"finishReason\":\"STOP\"}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()
	t.Setenv("EA_EASYCODE_BASE_URL", service.URL)
	oldHelper, oldOpen := invokeEasyCodeHelper, openEasyCodeBrowser
	t.Cleanup(func() { invokeEasyCodeHelper, openEasyCodeBrowser = oldHelper, oldOpen })
	openEasyCodeBrowser = func(string) error { return nil }
	invokeEasyCodeHelper = func(ctx context.Context, dir, action string, onBrowser func(string)) (nativeEasyCodeEvent, error) {
		event := nativeEasyCodeEvent{Type: action}
		path := filepath.Join(dir, "core-home", "easycode", "credentials.json")
		switch action {
		case "login":
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return event, err
			}
			value := fmt.Sprintf(`{"accessToken":"user-token","expiresAt":%d}`, time.Now().Add(time.Hour).UnixMilli())
			if err := os.WriteFile(path, []byte(value), 0600); err != nil {
				return event, err
			}
			event.Type = "complete"
			event.User.Name = "Test user"
			event.Models = []nativeEasyCodeModel{{ID: "deepseek-flash"}}
		case "logout":
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return event, err
			}
		case "status":
			_, statErr := os.Stat(path)
			event.LoggedIn = statErr == nil
			event.User.Name = "Test user"
		case "models":
			event.Models = []nativeEasyCodeModel{{ID: "deepseek-flash"}}
		}
		return event, nil
	}
	a.Settings.Mode = SettingCopyEasyCodeMode
	a.startEasyCodeLogin()
	waitNative(t, a, func() bool { return a.Live.Config.Model.Mode == "easycode" && a.Live.Connected })
	if a.Live.Config.PreviousModel == nil || a.Live.Config.PreviousModel.Mode != "inherit" {
		t.Fatal("previous model was not preserved")
	}
	s := a.current()
	s.Draft = "hello account"
	a.submitLive()
	waitNative(t, a, func() bool { s = a.current(); return !s.Running && len(s.Messages) > 1 })
	if s.Messages[len(s.Messages)-1].Text != "account reply" {
		t.Fatalf("wrong account response: %+v", s.Messages)
	}
	a.logoutEasyCode()
	waitNative(t, a, func() bool {
		return !a.Live.Loading["easycode-logout"] && a.Live.Config.Model.Mode == "inherit" && a.Live.Connected
	})
	if a.Live.EasyCode.LoggedIn || a.Live.Config.PreviousModel != nil {
		t.Fatal("logout did not restore prior settings")
	}
}
