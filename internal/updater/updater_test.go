package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRelease 生成假 Release 服务器：/releases/latest 返回 tag，
// /download/<tag>/ 提供 checksums.txt 与资产（校验和按资产内容实时计算）。
type fakeRelease struct {
	tag        string
	assets     map[string]string
	coreBinary string
}

func (f *fakeRelease) allAssets() map[string]string {
	assets := make(map[string]string, len(f.assets)+1)
	for name, content := range f.assets {
		assets[name] = content
	}
	if f.coreBinary != "" {
		assets[assetName("easyagent")] = f.coreBinary
	}
	return assets
}

func (f *fakeRelease) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name": %q}`, f.tag)
	})
	mux.HandleFunc("GET /download/{tag}/{asset}", func(w http.ResponseWriter, r *http.Request) {
		tag, asset := r.PathValue("tag"), r.PathValue("asset")
		if tag != f.tag {
			http.NotFound(w, r)
			return
		}
		if asset == "checksums.txt" {
			for name, content := range f.allAssets() {
				sum := sha256.Sum256([]byte(content))
				fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
			}
			return
		}
		if content, ok := f.allAssets()[asset]; ok {
			fmt.Fprint(w, content)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newTestUpdater 指向假服务器，并把版本回读 stub 成「校验假二进制首行约定」：
// 内容须以 "# version <want>\n" 开头（真实现执行 <path> --version）。
func newTestUpdater(current string, srv *httptest.Server) *Updater {
	u := New(current)
	u.APIBase = srv.URL
	u.DownloadBase = srv.URL + "/download"
	u.verifyBinary = func(path, want string) error {
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(content), "# version "+want+"\n") {
			return fmt.Errorf("版本不符: %s（期望 %s）", strings.TrimSpace(string(content)), want)
		}
		return nil
	}
	return u
}

func TestCheckComparesSemver(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		upToDate, isTag bool
	}{
		{"v0.47.0", "v0.48.0", false, true},
		{"v0.48.0", "v0.48.0", true, true},
		{"v0.48.1", "v0.48.0", true, true}, // 本地比 Release 新
		{"v0.48.0-alpha.1", "v0.48.0", false, true},
		{"v0.48.0-rc.2", "v0.48.0-rc.1", true, true},
		{"dev", "v0.48.0", false, false},
		{"fc6e49fa", "v0.48.0", false, false}, // mini 主机 main-SHA 通道
	} {
		srv := (&fakeRelease{tag: tc.latest}).server(t)
		status, err := newTestUpdater(tc.current, srv).Check(context.Background())
		if err != nil {
			t.Fatalf("current=%s: %v", tc.current, err)
		}
		if status.UpToDate != tc.upToDate || status.CurrentIsTag != tc.isTag || status.Latest != tc.latest {
			t.Fatalf("current=%s latest=%s: got upToDate=%v isTag=%v", tc.current, tc.latest, status.UpToDate, status.CurrentIsTag)
		}
	}
}

func TestCheckRejectsInvalidTag(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name": "not-a-version"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if _, err := newTestUpdater("v0.47.0", srv).Check(context.Background()); err == nil {
		t.Fatal("invalid tag must fail Check")
	}
}

// TestApplyHappyPath：下载 → 校验 → 原子替换核心与 bridge，旧版本留 .bak，暂存目录清理。
func TestApplyHappyPath(t *testing.T) {
	release := &fakeRelease{
		tag:        "v0.48.0",
		assets:     map[string]string{assetName("easyagent-bridge"): "# version easyagent-bridge v0.48.0\n"},
		coreBinary: "# version easyagent v0.48.0\n",
	}
	srv := release.server(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "easyagent")
	bridge := filepath.Join(dir, "easyagent-bridge")
	writeFile(t, target, "# version easyagent v0.47.0\n")
	writeFile(t, bridge, "# version easyagent-bridge v0.47.0\n")

	var progress bytes.Buffer
	u := newTestUpdater("v0.47.0", srv)
	if err := u.applyTo(context.Background(), Status{Latest: "v0.48.0"}, dir, "easyagent", &progress); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != "# version easyagent v0.48.0\n" {
		t.Fatalf("core not replaced: %q", got)
	}
	if got := readFile(t, bridge); got != "# version easyagent-bridge v0.48.0\n" {
		t.Fatalf("bridge not replaced: %q", got)
	}
	if got := readFile(t, target+".bak"); got != "# version easyagent v0.47.0\n" {
		t.Fatalf("core backup missing: %q", got)
	}
	if got := readFile(t, bridge+".bak"); got != "# version easyagent-bridge v0.47.0\n" {
		t.Fatalf("bridge backup missing: %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("staging leftovers in %s: %v", dir, names)
	}
	if !strings.Contains(progress.String(), "SHA-256 校验通过") || !strings.Contains(progress.String(), "已升级到 v0.48.0") {
		t.Fatalf("progress incomplete: %s", progress.String())
	}
}

// TestApplySkipsBridgeWhenAbsent：目录里没有 bridge 时不引入新二进制。
func TestApplySkipsBridgeWhenAbsent(t *testing.T) {
	srv := (&fakeRelease{tag: "v0.48.0", coreBinary: "# version easyagent v0.48.0\n"}).server(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "easyagent"), "# version easyagent v0.47.0\n")

	u := newTestUpdater("v0.47.0", srv)
	if err := u.applyTo(context.Background(), Status{Latest: "v0.48.0"}, dir, "easyagent", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "easyagent-bridge")); !os.IsNotExist(err) {
		t.Fatal("bridge must not be introduced by update")
	}
}

// TestApplyChecksumMismatchAborts：资产内容被篡改时中止，目标保持旧版本。
func TestApplyChecksumMismatchAborts(t *testing.T) {
	asset := assetName("easyagent")
	clean := "# version easyagent v0.48.0\n"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /download/v0.48.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		sum := sha256.Sum256([]byte(clean)) // 校验和按干净内容，实际返回篡改内容
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
	})
	mux.HandleFunc("GET /download/v0.48.0/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "# version easyagent TAMPERED\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "easyagent"), "# version easyagent v0.47.0\n")

	u := newTestUpdater("v0.47.0", srv)
	err := u.applyTo(context.Background(), Status{Latest: "v0.48.0"}, dir, "easyagent", nil)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("checksum mismatch must abort: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "easyagent")); got != "# version easyagent v0.47.0\n" {
		t.Fatalf("target must stay untouched: %q", got)
	}
}

// TestApplyVersionCheckFailureRollsBack：暂存二进制版本回读失败时不替换。
func TestApplyVersionCheckFailureRollsBack(t *testing.T) {
	srv := (&fakeRelease{tag: "v0.48.0", coreBinary: "# version easyagent WRONG\n"}).server(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "easyagent"), "# version easyagent v0.47.0\n")

	u := newTestUpdater("v0.47.0", srv)
	err := u.applyTo(context.Background(), Status{Latest: "v0.48.0"}, dir, "easyagent", nil)
	if err == nil || !strings.Contains(err.Error(), "版本不符") {
		t.Fatalf("version check failure must abort: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "easyagent")); got != "# version easyagent v0.47.0\n" {
		t.Fatalf("target must stay untouched: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "easyagent.bak")); !os.IsNotExist(err) {
		t.Fatal("no backup expected when replacement never happened")
	}
}

// TestApplyMissingChecksumAborts：checksums.txt 缺少核心资产条目时中止。
func TestApplyMissingChecksumAborts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /download/v0.48.0/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "deadbeef  some-other-asset")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "easyagent"), "# version easyagent v0.47.0\n")

	u := newTestUpdater("v0.47.0", srv)
	err := u.applyTo(context.Background(), Status{Latest: "v0.48.0"}, dir, "easyagent", nil)
	if err == nil || !strings.Contains(err.Error(), "checksums.txt 缺少") {
		t.Fatalf("missing checksum entry must abort: %v", err)
	}
}

// TestApplyUpToDateIsNoOp：已是最新时 Apply 无操作。
func TestApplyUpToDateIsNoOp(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "easyagent"), "# version easyagent v0.48.0\n")
	u := New("v0.48.0")
	if err := u.applyTo(context.Background(), Status{Latest: "v0.48.0", UpToDate: true}, dir, "easyagent", nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "easyagent")); got != "# version easyagent v0.48.0\n" {
		t.Fatalf("up-to-date apply must not touch target: %q", got)
	}
}

// TestReplaceBinaryRollsBack：第二次 mv 失败时自动还原备份。
func TestReplaceBinaryRollsBack(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bin")
	staged := filepath.Join(dir, "staged")
	writeFile(t, target, "old\n")
	writeFile(t, staged, "new\n")
	// staged 被移除 → 第二次 mv 必然失败，触发还原
	if err := os.Remove(staged); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(target, staged); err == nil {
		t.Fatal("replacement must fail when staged file is gone")
	}
	if got := readFile(t, target); got != "old\n" {
		t.Fatalf("target must be restored: %q", got)
	}
	if _, err := os.Stat(target + ".bak"); !os.IsNotExist(err) {
		t.Fatal("restore must consume the backup back into target")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
