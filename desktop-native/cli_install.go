package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 桌面端「安装命令行」：把应用内嵌的核心 CLI（macOS 为
// Contents/Resources/easyagent）symlink 到 ~/.easyagent/bin/easyagent，
// 目录与 scripts/install.sh 保持一致。App 自更新替换 bundle 后，symlink
// 指向的内容自动跟进新版，无需重复安装。

const cliInstallMarker = "# EasyAgent"

type cliInstallStatus int

const (
	cliNotInstalled    cliInstallStatus = iota // 目标不存在，可安装
	cliInstalledHere                           // 已安装且指向当前应用内嵌核心
	cliInstallConflict                         // 存在同名的独立安装（实体文件或指向他处的链接），不覆盖
)

func cliBinDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".easyagent", "bin"), nil
}

func cliInstallTarget() (string, error) {
	dir, err := cliBinDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "easyagent"), nil
}

// inspectCLIInstall 返回目标命令的安装状态与 symlink 目标（仅冲突/已装时有意义）。
func inspectCLIInstall(corePath string) (cliInstallStatus, string, error) {
	target, err := cliInstallTarget()
	if err != nil {
		return cliNotInstalled, "", err
	}
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return cliNotInstalled, "", nil
		}
		return cliNotInstalled, "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return cliInstallConflict, "", nil // 实体文件：install.sh 或手动安装
	}
	dest, err := os.Readlink(target)
	if err != nil {
		return cliInstallConflict, "", err
	}
	if sameFileResolving(target, corePath) {
		return cliInstalledHere, dest, nil
	}
	return cliInstallConflict, dest, nil
}

// installCLI 安装命令行并按需配置 PATH。symlink 创建成功但 PATH 配置失败时，
// 命令本体已可用，err 返回配置阶段的失败供 UI 提示；已安装时幂等返回。
func installCLI(corePath string) (cliInstallStatus, string, error) {
	status, _, err := inspectCLIInstall(corePath)
	if err != nil {
		return cliNotInstalled, "", err
	}
	if status != cliNotInstalled {
		return status, "", nil // 幂等 / 冲突不覆盖
	}
	binDir, err := cliBinDir()
	if err != nil {
		return cliNotInstalled, "", err
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return cliNotInstalled, "", err
	}
	target := filepath.Join(binDir, "easyagent")
	if err := os.Symlink(corePath, target); err != nil {
		return cliNotInstalled, "", err
	}
	rcPath, _, err := ensurePathConfigured(binDir)
	if err != nil {
		return cliInstalledHere, "", fmt.Errorf("PATH 配置失败（命令已装到 %s）：%w", target, err)
	}
	_ = ensureEnvFile()
	return cliInstalledHere, rcPath, nil
}

// uninstallCLI 仅删除指向当前应用核心的 symlink；实体安装与他人的链接不动。
func uninstallCLI(corePath string) (removed bool, message string, err error) {
	target, err := cliInstallTarget()
	if err != nil {
		return false, "", err
	}
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "命令行未安装", nil
		}
		return false, "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return false, "检测到独立安装的 easyagent（非本应用安装），未删除", nil
	}
	if !sameFileResolving(target, corePath) {
		return false, "该命令并非指向本应用内嵌核心，未删除", nil
	}
	if err := os.Remove(target); err != nil {
		return false, "", err
	}
	return true, "", nil
}

// sameFileResolving 判断 path（通常是 symlink）解析后是否与 want 同一文件。
func sameFileResolving(path, want string) bool {
	fiPath, err := os.Stat(path)
	if err != nil {
		return false
	}
	fiWant, err := os.Stat(want)
	if err != nil {
		return false
	}
	return os.SameFile(fiPath, fiWant)
}

// pathContainsDir 检查 PATH 是否已含 binDir（含 rc 里常见的 $HOME 展开形式）。
func pathContainsDir(binDir string) bool {
	home, err := os.UserHomeDir()
	dollar := ""
	if err == nil && home != "" && strings.HasPrefix(binDir, home) {
		dollar = "$HOME" + strings.TrimPrefix(binDir, home)
	}
	for _, entry := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if entry == binDir || (dollar != "" && entry == dollar) {
			return true
		}
	}
	return false
}

// shellRCFile 按当前 shell 选 rc 文件；GUI 启动时 SHELL 可能为空，
// macOS 默认 zsh、其余按 sh 惯例落 .profile。
func shellRCFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	shell := filepath.Base(os.Getenv("SHELL"))
	if shell == "" || shell == "." || shell == "/" {
		if runtime.GOOS == "darwin" {
			shell = "zsh"
		} else {
			shell = "sh"
		}
	}
	switch shell {
	case "zsh":
		return filepath.Join(home, ".zshrc"), nil
	case "bash":
		return filepath.Join(home, ".bashrc"), nil
	case "fish":
		return filepath.Join(home, ".config", "fish", "config.fish"), nil
	default:
		return filepath.Join(home, ".profile"), nil
	}
}

// ensurePathConfigured 把 binDir 追加进 PATH（rc 文件），返回写入的 rc 路径。
// PATH 已含该目录、或 rc 里已有 .easyagent/bin 配置（含 $HOME 展开形式）时不动。
func ensurePathConfigured(binDir string) (string, bool, error) {
	if pathContainsDir(binDir) {
		return "", false, nil
	}
	rcPath, err := shellRCFile()
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(rcPath), 0o755); err != nil {
		return "", false, err
	}
	existing, readErr := os.ReadFile(rcPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", false, readErr
	}
	if strings.Contains(string(existing), ".easyagent/bin") {
		return rcPath, false, nil
	}
	line := fmt.Sprintf("export PATH=\"$PATH:%s\"", binDir)
	if filepath.Base(os.Getenv("SHELL")) == "fish" {
		line = fmt.Sprintf("set -gx PATH $PATH %s", binDir)
	}
	f, err := os.OpenFile(rcPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n%s\n%s\n", cliInstallMarker, line); err != nil {
		return "", false, err
	}
	return rcPath, true, nil
}

// ensureEnvFile 创建空的 ~/.easyagent/.env（与 scripts/install.sh 一致），
// 给用户一个可发现的 env 配置入口；已存在则不动。
func ensureEnvFile() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".easyagent", ".env")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, nil, 0o600)
}
