package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountConflictOffersBothSources(t *testing.T) {
	for _, provider := range []string{"codex", "easycode"} {
		t.Run(provider, func(t *testing.T) {
			a := alignmentPreview(t)
			a.Settings.Open = true
			a.Settings.Tab = "models"
			a.Live.Data["account-status/"+provider] = accountSharingStatus{Shared: true, Desktop: true, Conflict: true}
			if provider == "codex" {
				a.Settings.ModelShowChatGPT = true
			} else {
				a.Settings.Mode = SettingCopyEasyCodeMode
			}
			tt := ui.NewTester(a.View, 1240, 1200)
			if !tt.HasText("使用命令行账号") || !tt.HasText("使用桌面原账号") {
				t.Fatal("missing conflict choice")
			}
		})
	}
}

func TestAccountConflictSelectionRestartsNativeCore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EA_HOME", "")
	a, _, _, _ := nativeCoreFixture(t)
	shared := filepath.Join(home, ".easyagent", "codex")
	desktop := filepath.Join(a.Live.Dir, "core-home", "codex")
	for _, dir := range []string{shared, desktop} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data := fmt.Sprintf(`{"accessToken":"test","accountId":"test","expiresAt":%d}`, time.Now().Add(time.Hour).UnixMilli())
		if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a.refreshAccountSharing("codex")
	waitNative(t, a, func() bool { return !a.Live.Loading["account-status/codex"] })
	status, ok := a.Live.Data["account-status/codex"].(accountSharingStatus)
	if !ok || !status.Conflict {
		t.Fatalf("missing conflict: %+v %s", status, a.Live.Error)
	}
	a.chooseSharedAccount("codex", "shared")
	waitNative(t, a, func() bool { return !a.Live.Loading["account-apply"] && (a.Live.Error != "" || a.Live.Connected) })
	if a.Live.Error != "" {
		t.Fatal(a.Live.Error)
	}
	waitNative(t, a, func() bool { return !a.Live.Loading["account-status/codex"] })
	status, _ = a.Live.Data["account-status/codex"].(accountSharingStatus)
	if status.Selected != "shared" {
		t.Fatalf("selection not persisted: %+v", status)
	}
	for _, dir := range []string{shared, desktop} {
		if _, err := os.Stat(filepath.Join(dir, "credentials.json")); err != nil {
			t.Fatal("account was lost", err)
		}
	}
}
