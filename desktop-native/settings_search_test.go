package main

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsSidebarSearch(t *testing.T) {
	models := false
	for _, p := range settingsPages {
		if settingsPageMatches(p, " 模型 ") {
			models = true
		}
	}
	if !models {
		t.Fatal("query 模型 should match the models settings page")
	}
	if settingsPageMatches(settingsPage{ID: "general", Label: "常规", Description: "语言"}, "mcp") {
		t.Fatal("unrelated query should not match")
	}
	if !settingsPageMatches(settingsPage{ID: "general", Label: "常规", Description: "语言"}, "") {
		t.Fatal("empty query should match every page")
	}
}

func TestShortcutRegistryDrivesNewSession(t *testing.T) {
	for _, shortcut := range nativeShortcuts {
		if shortcut.Label == "新会话" {
			if shortcut.match == nil || shortcut.run == nil {
				t.Fatal("新会话 must dispatch from the registry, not only render")
			}
			return
		}
	}
	t.Fatal("新会话 missing from the shortcut registry")
}

func TestSettingsRestoreIndividualDefaults(t *testing.T) {
	a := newPreview()
	s := &a.Settings
	s.Path = filepath.Join(t.TempDir(), "preferences.json")
	s.Preferences.Theme = "light"
	s.Preferences.WorkbenchWidth = 500
	a.openSettings("appearance")
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "恢复默认主题")
	if s.Preferences.Theme != "dark" || s.Preferences.WorkbenchWidth != 500 {
		t.Fatal("restoring the theme must preserve other preferences")
	}
	s.Preferences.Language = "English"
	a.openSettings("general")
	tt.Frame()
	click(t, tt, "恢复默认语言")
	if s.Preferences.Language != "中文" || s.Preferences.WorkbenchWidth != 500 {
		t.Fatal("restoring the language must preserve other preferences")
	}
	recovered := newSettings()
	recovered.loadPreferences(s.Path)
	if recovered.Preferences != s.Preferences {
		t.Fatal("restored defaults were not persisted")
	}
}
