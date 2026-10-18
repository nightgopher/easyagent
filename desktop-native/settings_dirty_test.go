package main

import "testing"

func confirmDirtySwitch(t *testing.T, a *preview, tab string) {
	t.Helper()
	a.openSettings(tab)
	if a.Live.Confirm == nil {
		t.Fatalf("switching away from a dirty %s form should ask for confirmation", a.Settings.Tab)
	}
	a.Live.Confirm.Action()
	a.Live.Confirm = nil
}

func TestSettingsMCPFormDiscardedOnConfirmedSwitch(t *testing.T) {
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Settings.Open = true
	a.Settings.Tab = "mcp"
	a.Settings.MCPName = "half-typed"
	a.Settings.Command = "npx"
	if !a.settingsFormDirty() {
		t.Fatal("half-filled MCP form should read as dirty")
	}
	confirmDirtySwitch(t, a, "general")
	if a.Settings.Tab != "general" || a.Settings.MCPName != "" || a.Settings.Command != "" {
		t.Fatal("confirmed switch should discard the form and proceed")
	}
}

func TestSettingsHostFormDiscardedOnConfirmedSwitch(t *testing.T) {
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Settings.Open = true
	a.Settings.Tab = "connections"
	a.Settings.HostName = "half-host"
	a.Settings.HostURL = "http://192.168.5.16:9999"
	confirmDirtySwitch(t, a, "general")
	if a.Settings.Tab != "general" || a.Settings.HostName != "" || a.Settings.HostURL != "" {
		t.Fatal("confirmed switch should discard the host form and proceed")
	}
}

func TestSettingsCleanFormSwitchesWithoutConfirmation(t *testing.T) {
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Settings.Open = true
	a.Settings.Tab = "mcp"
	a.openSettings("general")
	if a.Live.Confirm != nil {
		t.Fatal("clean forms should switch without confirmation")
	}
	if a.Settings.Tab != "general" {
		t.Fatal("clean switch should proceed")
	}
}
