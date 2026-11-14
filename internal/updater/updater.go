// Package updater 实现 CLI 自升级：发现最新 Release → 下载 → 校验 → 原子替换自身。
//
// 产物约定见 scripts/release.py 与 scripts/install.sh：CLI 是裸二进制
// easyagent-<os>-<arch>（bridge 同构），校验依赖 Release 附带的 checksums.txt
// （SHA-256，信任根 GitHub HTTPS），替换前用暂存二进制回读 --version。
package updater

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAPIBase      = "https://api.github.com/repos/nightgopher/easyagent"
	DefaultDownloadBase = "https://github.com/nightgopher/easyagent/releases/download"
)

// releaseTagRe 校验 Release tag 形如 vX.Y.Z[-alpha.N|-beta.N|-rc.N]，
// 与 scripts/install.sh 的版本正则保持一致。
var releaseTagRe = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.[1-9][0-9]*)?$`)

// Status 是一次 Check 的结果。
type Status struct {
	Current string // 当前二进制版本字符串（-ldflags 注入）
	Latest  string // 最新 Release tag，形如 v0.47.0
	// CurrentIsTag 为 false 表示当前版本不是 Release tag（dev、main-SHA）：
	// 可能是 easyagent-update 定时通道管理的主机，切换通道前应明确知晓。
	CurrentIsTag bool
	UpToDate     bool // 当前已等于或新于最新 Release
}

// Updater 发现并安装最新 Release。
type Updater struct {
	CurrentVersion string
	APIBase        string
	DownloadBase   string
	HTTP           *http.Client

	// verifyBinary 用暂存二进制回读版本，期望输出等于 want。可注入以便测试；
	// 生产实现执行 <path> --version（超时 15s）。
	verifyBinary func(path, want string) error
}

// New 返回面向当前进程的自升级器；currentVersion 来自 -ldflags 注入。
func New(currentVersion string) *Updater {
	return &Updater{
		CurrentVersion: currentVersion,
		APIBase:        DefaultAPIBase,
		DownloadBase:   DefaultDownloadBase,
		HTTP:           &http.Client{}, // 不设整体超时：大文件下载由 ctx 控制
	}
}

// Check 查询最新 Release 并与当前版本比较。
func (u *Updater) Check(ctx context.Context) (Status, error) {
	tag, err := u.latestTag(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Current:      u.CurrentVersion,
		Latest:       tag,
		CurrentIsTag: releaseTagRe.MatchString(u.CurrentVersion),
	}
	status.UpToDate = status.CurrentIsTag && compareVersions(u.CurrentVersion, tag) >= 0
	return status, nil
}

func (u *Updater) latestTag(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.APIBase+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	// 固定 ASCII UA：非 ASCII 或缺失 UA 会被 GitHub API 拒绝（400）。
	req.Header.Set("User-Agent", "easyagent-update")
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := u.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("查询最新 Release 失败: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("查询最新 Release 失败: HTTP %d", res.StatusCode)
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("解析最新 Release 失败: %w", err)
	}
	if !releaseTagRe.MatchString(payload.TagName) {
		return "", fmt.Errorf("无法识别的 Release 版本: %q", payload.TagName)
	}
	return payload.TagName, nil
}

// Apply 升级到 status.Latest：定位自身二进制后原子替换（同目录的 bridge 一并
// 升级，如已安装）。进度写入 progress（可为 nil）。status.UpToDate 时是无操作。
// 重启进程前运行的仍是旧版本。
func (u *Updater) Apply(ctx context.Context, status Status, progress io.Writer) error {
	target, err := selfPath()
	if err != nil {
		return err
	}
	return u.applyTo(ctx, status, filepath.Dir(target), filepath.Base(target), progress)
}

// applyTo 是 Apply 的注入版本，测试用：核心二进制安装为 dir/coreName，下载时
// 才使用 Release 资产名（easyagent-<os>-<arch>，install.sh 安装时会改名）。
func (u *Updater) applyTo(ctx context.Context, status Status, dir, coreName string, progress io.Writer) error {
	if status.UpToDate {
		return nil
	}
	if !releaseTagRe.MatchString(status.Latest) {
		return fmt.Errorf("无效的目标版本: %q", status.Latest)
	}
	say := func(format string, args ...any) {
		if progress != nil {
			fmt.Fprintf(progress, format+"\n", args...)
		}
	}

	stage, err := os.MkdirTemp(dir, ".easyagent-update-*")
	if err != nil {
		return fmt.Errorf("创建暂存目录失败（%s 需可写）: %w", dir, err)
	}
	defer os.RemoveAll(stage)

	tag := status.Latest
	sums, err := u.fetchChecksums(ctx, tag)
	if err != nil {
		return err
	}
	coreAsset := assetName("easyagent")
	if err := u.stageAsset(ctx, tag, sums, coreAsset, stage, progress); err != nil {
		return err
	}
	if err := u.verifyBinaryFor(tag, "easyagent "+tag, filepath.Join(stage, coreAsset)); err != nil {
		return err
	}

	// bridge 仅在已安装时随之升级，避免引入新二进制或留下版本错配。
	bridgeName := bridgeInstallName(coreName)
	bridgeTarget := filepath.Join(dir, bridgeName)
	bridgeInstalled := fileExists(bridgeTarget)
	var stagedBridge string
	if bridgeInstalled {
		if err := u.stageAsset(ctx, tag, sums, assetName("easyagent-bridge"), stage, progress); err != nil {
			return err
		}
		stagedBridge = filepath.Join(stage, assetName("easyagent-bridge"))
		if err := u.verifyBinaryFor(tag, "easyagent-bridge "+tag, stagedBridge); err != nil {
			return err
		}
	}

	target := filepath.Join(dir, coreName)
	say("替换 %s（旧版本备份为 %s.bak）…", target, target)
	if err := replaceBinary(target, filepath.Join(stage, coreAsset)); err != nil {
		return fmt.Errorf("替换核心失败（旧安装已保留）: %w", err)
	}
	if bridgeInstalled {
		say("替换 %s…", bridgeTarget)
		if err := replaceBinary(bridgeTarget, stagedBridge); err != nil {
			return fmt.Errorf("替换 bridge 失败: %w", err)
		}
	}
	say("已升级到 %s，重启 easyagent 后生效。", tag)
	return nil
}

// bridgeInstallName 推断 bridge 的安装名：与核心二进制同扩展名（Windows .exe）。
func bridgeInstallName(coreName string) string {
	if strings.HasSuffix(coreName, ".exe") {
		return "easyagent-bridge.exe"
	}
	return "easyagent-bridge"
}

func (u *Updater) verifyBinaryFor(tag, want, path string) error {
	if u.verifyBinary != nil {
		return u.verifyBinary(path, want)
	}
	return runVersionCheck(path, want)
}

func (u *Updater) fetchChecksums(ctx context.Context, tag string) (map[string]string, error) {
	res, err := u.get(ctx, u.DownloadBase+"/"+tag+"/checksums.txt")
	if err != nil {
		return nil, fmt.Errorf("获取 checksums.txt 失败: %w", err)
	}
	defer res.Body.Close()
	sums := map[string]string{}
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if _, dup := sums[name]; dup {
			return nil, fmt.Errorf("checksums.txt 存在重复条目: %s", name)
		}
		sums[name] = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 checksums.txt 失败: %w", err)
	}
	return sums, nil
}

// stageAsset 下载单个资产到 stageDir，边下边算 SHA-256 并与 checksums.txt 比对。
func (u *Updater) stageAsset(ctx context.Context, tag string, sums map[string]string, asset, stageDir string, progress io.Writer) error {
	digest, ok := sums[asset]
	if !ok {
		return fmt.Errorf("checksums.txt 缺少 %s，取消升级", asset)
	}
	if progress != nil {
		fmt.Fprintf(progress, "下载 %s …\n", asset)
	}
	res, err := u.get(ctx, u.DownloadBase+"/"+tag+"/"+asset)
	if err != nil {
		return fmt.Errorf("下载 %s 失败: %w", asset, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 失败: HTTP %d", asset, res.StatusCode)
	}

	staged := filepath.Join(stageDir, asset)
	f, err := os.Create(staged) // #nosec G304 -- 暂存目录由 MkdirTemp 生成，asset 是固定命名的 Release 资产，非用户输入。
	if err != nil {
		return err
	}
	hash := sha256.New()
	if _, err = io.Copy(io.MultiWriter(f, hash), res.Body); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err != nil {
		return fmt.Errorf("保存 %s 失败: %w", asset, err)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != digest {
		_ = os.Remove(staged)
		return fmt.Errorf("SHA-256 校验失败: %s（期望 %s，实际 %s）", asset, digest, actual)
	}
	if err := os.Chmod(staged, 0o755); err != nil { // #nosec G302 -- 替换目标是可执行二进制，必须保留执行位（install.sh 同款 0755）。
		return err
	}
	if progress != nil {
		fmt.Fprintf(progress, "SHA-256 校验通过: %s\n", asset)
	}
	return nil
}

func (u *Updater) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "easyagent-update")
	return u.HTTP.Do(req)
}

// assetName 是 Release 资产命名：easyagent-<os>-<arch>[.exe]（无目录前缀）。
func assetName(base string) string {
	name := base + "-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// runVersionCheck 是默认的版本回读：执行 <path> --version 并期望精确输出。
func runVersionCheck(path, want string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return fmt.Errorf("回读暂存二进制版本失败: %w", err)
	}
	if strings.TrimSpace(string(out)) != want {
		return fmt.Errorf("暂存二进制版本不符: %s（期望 %s）", strings.TrimSpace(string(out)), want)
	}
	return nil
}

// replaceBinary 用暂存文件原子替换 target：先 mv 出 .bak，失败自动还原。
// 同目录 mv 保证同文件系统；Windows 允许对运行中的 exe 重命名。
func replaceBinary(target, staged string) error {
	bak := target + ".bak"
	if err := os.Rename(target, bak); err != nil {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		if restoreErr := os.Rename(bak, target); restoreErr != nil {
			return fmt.Errorf("%w（还原备份也失败: %v，请手动从 %s 恢复）", err, restoreErr, bak)
		}
		return err
	}
	return nil
}

// selfPath 定位当前进程的真实二进制路径（解析符号链接）。
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("无法定位自身可执行文件: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("解析可执行文件路径失败: %w", err)
	}
	return resolved, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// compareVersions 比较两个已通过 releaseTagRe 校验的版本：1 表示 a 更新。
// 主版本号按数值逐段比较；同号时无后缀 > rc > beta > alpha，N 再按数值比较。
func compareVersions(a, b string) int {
	aBase, aPre := versionParts(a)
	bBase, bPre := versionParts(b)
	for i := 0; i < 3; i++ {
		if aBase[i] != bBase[i] {
			if aBase[i] > bBase[i] {
				return 1
			}
			return -1
		}
	}
	ra, na := preReleaseRank(aPre)
	rb, nb := preReleaseRank(bPre)
	if ra != rb {
		if ra > rb {
			return 1
		}
		return -1
	}
	if na != nb {
		if na > nb {
			return 1
		}
		return -1
	}
	return 0
}

func versionParts(v string) ([3]int, string) {
	var base [3]int
	parts := releaseTagRe.FindStringSubmatch(v)
	if parts == nil {
		return base, ""
	}
	for i := range base {
		n, _ := strconv.Atoi(parts[i+1])
		base[i] = n
	}
	if len(parts) > 4 {
		return base, parts[4]
	}
	return base, ""
}

// preReleaseRank 返回后缀档位（正式版最高）与后缀编号；无后缀时编号无意义。
func preReleaseRank(suffix string) (int, int) {
	switch {
	case suffix == "":
		return 4, 0
	case strings.HasPrefix(suffix, "rc."):
		return 3, atoiSuffix(suffix)
	case strings.HasPrefix(suffix, "beta."):
		return 2, atoiSuffix(suffix)
	case strings.HasPrefix(suffix, "alpha."):
		return 1, atoiSuffix(suffix)
	}
	return 0, 0
}

func atoiSuffix(suffix string) int {
	_, n, _ := strings.Cut(suffix, ".")
	value, _ := strconv.Atoi(n)
	return value
}
