package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexLoginStatusShowsModels(t *testing.T) {
	original := invokeCodexHelper
	t.Cleanup(func() { invokeCodexHelper = original })
	invokeCodexHelper = func(ctx context.Context, dir, action string, onBrowser func(string)) (nativeCodexEvent, error) {
		switch action {
		case "status":
			return nativeCodexEvent{Type: "status", LoggedIn: true}, nil
		case "models":
			return nativeCodexEvent{Type: "models", Models: []nativeEasyCodeModel{{ID: "test-model"}}}, nil
		}
		return nativeCodexEvent{}, fmt.Errorf("unexpected action %s", action)
	}
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.refreshCodexStatus()
	waitNative(t, a, func() bool { return !a.Live.Loading["codex-status"] })
	if !a.Live.Codex.LoggedIn || len(a.Live.Codex.Models) != 1 {
		t.Fatal("logged-in account or models missing")
	}
	a.applyCodexModel("unknown")
	if a.Live.Loading["codex-apply"] || a.Live.Config.Model.Mode == "codex" || a.Live.Error == "" {
		t.Fatal("unknown model must not be applied")
	}
}

func TestCodexApplyUsesLoggedInAccount(t *testing.T) {
	a, _, _, _ := nativeCoreFixture(t)
	dir := filepath.Join(a.Live.Dir, "core-home", "codex")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	credential := fmt.Sprintf(`{"accessToken":"test-token","accountId":"test-account","expiresAt":%d}`, time.Now().Add(time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(credential), 0600); err != nil {
		t.Fatal(err)
	}
	a.Live.Codex.LoggedIn = true
	a.Live.Codex.Models = []nativeEasyCodeModel{{ID: "gpt-5.2-codex"}}
	a.applyCodexModel("gpt-5.2-codex")
	waitNative(t, a, func() bool {
		return !a.Live.Loading["codex-apply"] && (a.Live.Error != "" || a.Live.Config.Model.Mode == "codex" && a.Live.Connected)
	})
	if a.Live.Config.Model.Mode != "codex" {
		t.Fatal(a.Live.Error)
	}
	var result struct{ Current struct{ Provider, ID string } }
	if err := a.Live.Client.request(context.Background(), "GET", "/models", nil, &result); err != nil {
		t.Fatal(err)
	}
	if result.Current.Provider != "codex" || result.Current.ID != "gpt-5.2-codex" {
		t.Fatalf("wrong model: %+v", result)
	}
	if a.Live.Config.PreviousModel == nil {
		t.Fatal("previous provider not preserved")
	}
}
