package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestGeneralSettingsControlsStayInsidePaddedGroup(t *testing.T) {
	for _, width := range []int{640, 960, 1240} {
		for _, language := range []string{"中文", "English"} {
			t.Run(fmt.Sprintf("%d-%s", width, language), func(t *testing.T) {
				a := alignmentPreview(t)
				a.Settings.Open, a.Settings.Tab = true, "general"
				a.Settings.Preferences.Language = language
				tt := ui.NewTester(a.View, width, 820)
				group, ok := tt.Find("常规设置")
				if !ok || group.X+group.W > float32(width) {
					t.Fatalf("settings group exceeds the window: %+v", group)
				}
				for _, label := range []string{"语言选择", "恢复默认语言", "检查更新"} {
					control, ok := tt.Find(a.tr(label))
					if !ok || control.X < group.X+15 || control.X+control.W > group.X+group.W-15 || control.Y+control.H > group.Y+group.H {
						t.Fatalf("%s escapes the group padding: control=%+v group=%+v", label, control, group)
					}
				}
				if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
					saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("general-refined-%d-%s.png", width, language)))
				}
			})
		}
	}
}

func TestModelSetupActionUsesContentWidth(t *testing.T) {
	a := alignmentPreview(t)
	a.Settings.Open, a.Settings.Tab = true, "models"
	a.Live.Data["models"] = map[string]any{"current": map[string]any{"id": "glm-5.3-flash"}}
	a.current().Model = "glm-5.3-flash"
	a.Live.Connected = true
	tt := ui.NewTester(a.View, 960, 820)
	button, ok := tt.Find("配置全局模型")
	if !ok || button.W > 160 {
		t.Fatalf("setup action stretches across the settings page: %+v", button)
	}
	if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, tt, filepath.Join(dir, "model-refined.png"))
	}
	click(t, tt, "配置全局模型")
	if a.Settings.Mode != SettingCopyDesktopMode || a.Settings.Model != "glm-5.3-flash" {
		t.Fatal("setup action lost the current model")
	}
}
