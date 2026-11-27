package main

import (
	"github.com/egoist/mygo/ui"
	"testing"
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
