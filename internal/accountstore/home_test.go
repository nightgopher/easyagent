package accountstore

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func put(t *testing.T, home, provider, value string) {
	t.Helper()
	d := filepath.Join(home, provider)
	if err := os.MkdirAll(d, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "credentials.json"), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestDesktopAndCLIUseSameCredentialAndPreserveConflicts(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user")
	shared := filepath.Join(user, ".easyagent")
	desktop := filepath.Join(user, "Library", "Application Support", "EasyAgent", "core-home")
	t.Setenv("HOME", user)
	t.Setenv("EA_HOME", "")
	t.Setenv("EA_DESKTOP_ACCOUNT_HOME", desktop)
	t.Setenv("EA_SHARED_ACCOUNT_HOME", shared)
	for _, p := range []string{"codex", "easycode"} {
		got, err := Resolve(desktop, p)
		if err != nil || got != shared {
			t.Fatalf("empty desktop %s: %s %v", p, got, err)
		}
		put(t, shared, p, "shared-"+p)
		got, err = Resolve(desktop, p)
		if err != nil || got != shared {
			t.Fatalf("desktop should use CLI account: %s %v", got, err)
		}
		put(t, desktop, p, "desktop-"+p)
		status, err := GetStatus(desktop, p)
		if err != nil || !status.Conflict || status.Selected != "" {
			t.Fatalf("preserve desktop: %+v %v", status, err)
		}
		if err = Select(desktop, p, "shared"); err != nil {
			t.Fatal(err)
		}
		got, err = Resolve(desktop, p)
		if err != nil || got != shared {
			t.Fatalf("explicit selection: %s %v", got, err)
		}
		a, _ := os.ReadFile(filepath.Join(shared, p, "credentials.json"))
		b, _ := os.ReadFile(filepath.Join(desktop, p, "credentials.json"))
		if string(a) != "shared-"+p || string(b) != "desktop-"+p {
			t.Fatal("credentials modified")
		}
	}
}
func TestMissingChoiceAndUnsafeFiles(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	desktop := filepath.Join(root, "desktop")
	t.Setenv("EA_DESKTOP_ACCOUNT_HOME", desktop)
	t.Setenv("EA_SHARED_ACCOUNT_HOME", shared)
	if err := Select(desktop, "codex", "shared"); err == nil {
		t.Fatal("selected missing account")
	}
	put(t, shared, "codex", "secret")
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(filepath.Join(shared, "codex", "credentials.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(desktop, "codex"); err == nil {
		t.Fatal("unsafe credential accepted")
	}
}

func TestCLIFindsExistingDesktopAccountAndCanChoose(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("EA_HOME", "")
	t.Setenv("EA_DESKTOP_ACCOUNT_HOME", "")
	t.Setenv("EA_SHARED_ACCOUNT_HOME", "")
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(configDir, "EasyAgent", "core-home")
	cli := filepath.Join(root, ".easyagent")
	put(t, desktop, "codex", "desktop")
	got, err := Resolve(cli, "codex")
	if err != nil || got != desktop {
		t.Fatalf("CLI did not find desktop login: %q %v", got, err)
	}
	put(t, cli, "codex", "cli")
	status, err := GetStatus(cli, "codex")
	if err != nil || !status.Conflict || status.Selected != "" {
		t.Fatalf("CLI conflict: %+v %v", status, err)
	}
	if err := Select(cli, "codex", "desktop"); err != nil {
		t.Fatal(err)
	}
	got, err = Resolve(cli, "codex")
	if err != nil || got != desktop {
		t.Fatalf("CLI selection: %q %v", got, err)
	}
	t.Setenv("EA_HOME", cli)
	got, err = Resolve(cli, "codex")
	if err != nil || got != cli {
		t.Fatalf("explicit EA_HOME should isolate server: %q %v", got, err)
	}
}
