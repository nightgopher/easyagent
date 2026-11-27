package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Invoked by build-native.sh against the staged bundle, without opening the UI.
func TestPackagedCoreSmoke(t *testing.T) {
	bundle := os.Getenv("EASYAGENT_TEST_BUNDLE")
	if bundle == "" {
		t.Skip("set EASYAGENT_TEST_BUNDLE to validate a built app")
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "EA_") || strings.HasPrefix(key, "OPENAI_") || strings.HasPrefix(key, "ANTHROPIC_") || key == "EASYAGENT_NATIVE_CORE" {
			t.Setenv(key, "")
		}
	}
	exe := filepath.Join(bundle, "Contents", "MacOS", "EasyAgent")
	want := filepath.Join(bundle, "Contents", "Resources", "easyagent")
	core, err := coreBinaryAt(exe)
	if err != nil || core != want {
		t.Fatalf("bundle selected %q, want %q: %v", core, want, err)
	}
	for _, stale := range []string{"runtime", "workflow-runtime.mjs"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(core), stale)); !os.IsNotExist(err) {
			t.Fatalf("obsolete bundled resource %s: %v", stale, err)
		}
	}
	dir := t.TempDir()
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"smoke-only","object":"model"}]}`))
			return
		}
		t.Errorf("health smoke unexpectedly called model endpoint: %s", r.URL.Path)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer provider.Close()
	for key, value := range map[string]string{
		"EASYAGENT_NATIVE_CORE": core,
		"EA_ENV_FILE":           filepath.Join(dir, "absent.env"),
		"EA_WORKSPACE":          dir, "EA_KB_REPO_PATH": dir,
		"EA_SKILL_MARKET": "0", "EA_ENABLE_BASH": "false",
		"HOME": dir, "EA_BASE_URL": provider.URL,
	} {
		t.Setenv(key, value)
	}
	backend := &localBackend{}
	defer backend.close()
	// Keep the whole run above the core's readiness budget: when the smoke
	// budget stretches for slow shared-desktop starts, the run context must
	// stretch with it, or the 20s ceiling cuts the wait short first.
	ctxBudget := 20 * time.Second
	if v := os.Getenv("EASYAGENT_CORE_READY_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 5 {
			ctxBudget = time.Duration(n)*time.Second + 15*time.Second
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), ctxBudget)
	defer cancel()
	// A clean installation must be able to start and show model settings
	// without touching even a configured default gateway URL.
	first, err := backend.start(ctx, dir, modelConfig{Mode: "inherit"}, &memorySecrets{})
	if err != nil {
		t.Fatalf("first-run core failed: %v", err)
	}
	var catalog struct {
		Source  string `json:"source"`
		Current any    `json:"current"`
	}
	if err = first.request(ctx, "GET", "/models", nil, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Source != "unconfigured" || catalog.Current != nil || requests.Load() != 0 {
		t.Fatalf("unexpected first-run state: %+v, gateway calls %d", catalog, requests.Load())
	}
	var session map[string]any
	if err = first.request(ctx, "POST", "/sessions", map[string]any{}, &session); err != nil {
		t.Fatalf("first-run session creation: %v", err)
	}
	backend.stop()
	if requests.Load() != 0 {
		t.Fatalf("unconfigured core contacted model service %d times", requests.Load())
	}
	credentialDir := filepath.Join(dir, "core-home", "codex")
	if err := os.MkdirAll(credentialDir, 0700); err != nil {
		t.Fatal(err)
	}
	credential := fmt.Sprintf(`{"accessToken":"test-token","accountId":"test-account","expiresAt":%d}`, time.Now().Add(time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(credentialDir, "credentials.json"), []byte(credential), 0600); err != nil {
		t.Fatal(err)
	}
	// A saved default from another provider must not override the explicitly
	// selected ChatGPT service on the next desktop startup.
	if err := os.WriteFile(filepath.Join(dir, "core-data", "settings.json"), []byte(`{"default_provider":"openai","default_model":"old-model"}`), 0600); err != nil {
		t.Fatal(err)
	}
	codex, err := backend.start(ctx, dir, modelConfig{Mode: "codex", Provider: "codex", Model: "gpt-5.2-codex"}, &memorySecrets{})
	if err != nil {
		t.Fatalf("ChatGPT core failed: %v", err)
	}
	var codexCatalog struct{ Current struct{ Provider, ID string } }
	if err = codex.request(ctx, "GET", "/models", nil, &codexCatalog); err != nil {
		t.Fatal(err)
	}
	if codexCatalog.Current.Provider != "codex" || codexCatalog.Current.ID != "gpt-5.2-codex" {
		t.Fatalf("wrong ChatGPT default: %+v", codexCatalog)
	}
	backend.stop()
	model := modelConfig{Mode: "override", Provider: "openai", BaseURL: provider.URL, Model: "smoke-only"}
	client, err := backend.start(ctx, dir, model, &memorySecrets{values: map[string]string{"model": "smoke-only"}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", client.URL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health status: %d", res.StatusCode)
	}
	done := backend.done
	backend.stop()
	select {
	case <-done:
	default:
		t.Fatal("core still running after stop")
	}
}
