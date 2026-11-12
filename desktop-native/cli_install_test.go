package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 隔离 HOME/SHELL/PATH，返回 (home, corePath)。
func cliInstallTestEnv(t *testing.T, shell string, path string) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", shell)
	t.Setenv("PATH", path)
	core := filepath.Join(t.TempDir(), "core-easyagent")
	if err := os.WriteFile(core, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home, core
}

func cliReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// 安装：symlink 指向核心、rc 写入 export 行、.env 创建；重装幂等不重复写 rc。
func TestInstallCLILinksCoreAndWritesRC(t *testing.T) {
	home, core := cliInstallTestEnv(t, "/bin/zsh", "/usr/bin:/bin")

	status, rcPath, err := installCLI(core)
	if err != nil {
		t.Fatal(err)
	}
	if status != cliInstalledHere {
		t.Fatalf("status = %v, want cliInstalledHere", status)
	}
	if rcPath != filepath.Join(home, ".zshrc") {
		t.Fatalf("rcPath = %q, want ~/.zshrc", rcPath)
	}

	target := filepath.Join(home, ".easyagent", "bin", "easyagent")
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("target not a usable symlink: %v", err)
	}
	// macOS 的 /var 是 /private/var 的 symlink，用 SameFile 语义比较。
	fiResolved, err := os.Stat(resolved)
	if err != nil {
		t.Fatal(err)
	}
	fiCore, err := os.Stat(core)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fiResolved, fiCore) {
		t.Fatalf("symlink resolves to %q, want %q", resolved, core)
	}

	rc := cliReadFile(t, rcPath)
	if !strings.Contains(rc, cliInstallMarker) || !strings.Contains(rc, `export PATH="$PATH:`+filepath.Join(home, ".easyagent", "bin")+`"`) {
		t.Fatalf("rc missing EasyAgent PATH line: %q", rc)
	}

	if _, err := os.Stat(filepath.Join(home, ".easyagent", ".env")); err != nil {
		t.Fatalf(".env not created: %v", err)
	}

	// 幂等：重装返回已安装，rc 不重复追加。
	rcBefore := cliReadFile(t, rcPath)
	status, rcPath2, err := installCLI(core)
	if err != nil {
		t.Fatal(err)
	}
	if status != cliInstalledHere || rcPath2 != "" {
		t.Fatalf("reinstall status = %v rcPath = %q, want installed with no rc write", status, rcPath2)
	}
	if cliReadFile(t, rcPath) != rcBefore {
		t.Fatal("reinstall must not touch rc file")
	}
}

// PATH 已含目录时不写 rc；rc 里已有 $HOME 形式配置时也不重复追加。
func TestInstallCLISkipsRCWhenPathConfigured(t *testing.T) {
	home, core := cliInstallTestEnv(t, "/bin/zsh", "")
	binDir := filepath.Join(home, ".easyagent", "bin")

	t.Setenv("PATH", binDir+":/usr/bin")
	if status, rcPath, err := installCLI(core); err != nil || status != cliInstalledHere || rcPath != "" {
		t.Fatalf("install with PATH set: status=%v rcPath=%q err=%v", status, rcPath, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatal("rc must not be created when PATH already contains bin dir")
	}

	// $HOME 展开形式写在 rc 里 → 视为已配置。
	home2, core2 := cliInstallTestEnv(t, "/bin/zsh", "/usr/bin")
	rc2 := filepath.Join(home2, ".zshrc")
	if err := os.WriteFile(rc2, []byte("export PATH=\"$PATH:$HOME/.easyagent/bin\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, rcPath, err := installCLI(core2); err != nil || rcPath == "" {
		t.Fatalf("install: rcPath=%q err=%v", rcPath, err)
	}
	if strings.Count(cliReadFile(t, rc2), ".easyagent/bin") != 1 {
		t.Fatal("existing $HOME-form PATH entry must not be duplicated")
	}
}

// 三种 shell 的 rc 位置与语法。
func TestInstallCLIRCFlavors(t *testing.T) {
	cases := []struct {
		shell    string
		rc       string
		linePart string
	}{
		{"/bin/zsh", ".zshrc", `export PATH="$PATH:`},
		{"/usr/bin/bash", ".bashrc", `export PATH="$PATH:`},
		{"/opt/homebrew/bin/fish", filepath.Join(".config", "fish", "config.fish"), "set -gx PATH $PATH "},
		{"", ".zshrc", `export PATH="$PATH:`}, // GUI 启动 SHELL 为空 → darwin 默认 zsh
	}
	for _, tc := range cases {
		t.Run(tc.shell, func(t *testing.T) {
			home, core := cliInstallTestEnv(t, tc.shell, "/usr/bin")
			_, rcPath, err := installCLI(core)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(home, tc.rc)
			if rcPath != want {
				t.Fatalf("rcPath = %q, want %q", rcPath, want)
			}
			if !strings.Contains(cliReadFile(t, rcPath), tc.linePart) {
				t.Fatalf("rc missing %q: %q", tc.linePart, cliReadFile(t, rcPath))
			}
		})
	}
}

// 冲突不覆盖：实体文件（install.sh 安装）与指向他处的 symlink 都原样保留。
func TestInstallCLIConflictKeepsExisting(t *testing.T) {
	home, core := cliInstallTestEnv(t, "/bin/zsh", "/usr/bin")
	target := filepath.Join(home, ".easyagent", "bin", "easyagent")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}

	// 实体文件。
	if err := os.WriteFile(target, []byte("user-installed"), 0o755); err != nil {
		t.Fatal(err)
	}
	status, _, err := installCLI(core)
	if err != nil {
		t.Fatal(err)
	}
	if status != cliInstallConflict {
		t.Fatalf("status = %v, want cliInstallConflict", status)
	}
	if got := cliReadFile(t, target); got != "user-installed" {
		t.Fatalf("existing binary overwritten: %q", got)
	}

	// 指向他处的 symlink（含悬空）。
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other-easyagent")
	if err := os.Symlink(other, target); err != nil { // other 不存在 → 悬空
		t.Fatal(err)
	}
	status, _, err = installCLI(core)
	if err != nil {
		t.Fatal(err)
	}
	if status != cliInstallConflict {
		t.Fatalf("dangling symlink status = %v, want cliInstallConflict", status)
	}
	resolved, err := os.Readlink(target)
	if err != nil || resolved != other {
		t.Fatalf("symlink target changed: %q err=%v", resolved, err)
	}
}

// 卸载：只删指向本应用核心的 symlink；实体安装与他人的链接不动。
func TestUninstallCLIRemovesOnlyOwnLink(t *testing.T) {
	home, core := cliInstallTestEnv(t, "/bin/zsh", "/usr/bin")
	target := filepath.Join(home, ".easyagent", "bin", "easyagent")

	if _, _, err := installCLI(core); err != nil {
		t.Fatal(err)
	}
	rc := cliReadFile(t, filepath.Join(home, ".zshrc"))

	removed, message, err := uninstallCLI(core)
	if err != nil || !removed || message != "" {
		t.Fatalf("uninstall: removed=%v message=%q err=%v", removed, message, err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("symlink not removed")
	}
	if cliReadFile(t, filepath.Join(home, ".zshrc")) != rc {
		t.Fatal("uninstall must not touch rc file")
	}
	// 再卸载：未安装提示。
	if removed, message, _ := uninstallCLI(core); removed || message != "命令行未安装" {
		t.Fatalf("re-uninstall: removed=%v message=%q", removed, message)
	}

	// 实体安装不删。
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("user-installed"), 0o755); err != nil {
		t.Fatal(err)
	}
	removed, message, err = uninstallCLI(core)
	if err != nil || removed || message == "" {
		t.Fatalf("entity uninstall: removed=%v message=%q err=%v", removed, message, err)
	}
	if got := cliReadFile(t, target); got != "user-installed" {
		t.Fatal("entity file must survive uninstall")
	}
}
