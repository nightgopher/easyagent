package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/internal/easycode"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

func TestEasyCodeCommandStatusDoesNotExposeCredentials(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "easycode")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"accessToken":"private-access","refreshToken":"private-refresh","expiresAt":4102444800000,"user":{"name":"Example"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"status"}, {"status", "--machine"}} {
		var output bytes.Buffer
		if err := runEasyCodeCommand(context.Background(), args, home, "http://127.0.0.1:1", &output); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "private-access") || strings.Contains(output.String(), "private-refresh") {
			t.Fatal("credential appeared in helper output")
		}
	}
	var output bytes.Buffer
	if err := runEasyCodeCommand(context.Background(), []string{"logout", "--machine"}, home, "http://127.0.0.1:1", &output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("logout left local credentials")
	}
}

func TestEasyCodeSelectionDoesNotChangeServiceOrExplicitProvider(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	manager, err := easycode.NewManager(home, "")
	require.NoError(t, err)
	require.NoError(t, manager.SetCLIModel(ctx, "saved-model"))
	for _, tc := range []struct {
		name, mode, provider, model, wantProvider, wantModel string
	}{
		{"normal CLI", "chat", "", "", "deepv", "saved-model"},
		{"one shot", "run", "", "", "deepv", "saved-model"},
		{"explicit model", "interactive", "deepv", "custom", "deepv", "custom"},
		{"explicit provider", "run", "openai", "", "openai", "original"},
		{"server", "serve", "", "", "openai", "original"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Provider, cfg.OpenAIModel = "openai", "original"
			require.NoError(t, applyEasyCodeCLISelection(ctx, &cfg, home, tc.mode, tc.provider, tc.model))
			require.Equal(t, tc.wantProvider, cfg.Provider)
			require.Equal(t, tc.wantModel, cfg.OpenAIModel)
		})
	}
	require.NoError(t, manager.ClearCLIModel(ctx))
	cfg := config.Default()
	cfg.Provider, cfg.OpenAIModel = "openai", "original"
	require.NoError(t, applyEasyCodeCLISelection(ctx, &cfg, home, "chat", "", ""))
	require.Equal(t, "openai", cfg.Provider)
	require.Equal(t, "original", cfg.OpenAIModel)
}

func TestEasyCodeSelectValidModel(t *testing.T) {
	ctx := context.Background()
	manager, err := easycode.NewManager(t.TempDir(), "")
	require.NoError(t, err)
	models := []easycode.Model{{ID: "first"}, {ID: "second"}}
	selected, err := selectEasyCodeModel(ctx, manager, models, "")
	require.NoError(t, err)
	require.Equal(t, "first", selected)
	selected, err = selectEasyCodeModel(ctx, manager, models, "second")
	require.NoError(t, err)
	require.Equal(t, "second", selected)
	selected, err = selectEasyCodeModel(ctx, manager, models, "")
	require.NoError(t, err)
	require.Equal(t, "second", selected)
	_, err = selectEasyCodeModel(ctx, manager, models, "unknown")
	require.Error(t, err)
	selected, err = manager.CLIModel(ctx)
	require.NoError(t, err)
	require.Equal(t, "second", selected)
}

type lineWriter chan string

func (w lineWriter) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}

func TestEasyCodeLoginActivatesCLIOnlyForHumanCommand(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config/client":
			_, _ = fmt.Fprint(w, `{"success":true,"data":{"feishu":{"appId":"app-id"}}}`)
		case "/api/auth/feishu/exchange":
			_, _ = fmt.Fprint(w, `{"success":true,"data":{"accessToken":"feishu-fixture"}}`)
		case "/auth/jwt/feishu-login":
			_, _ = fmt.Fprint(w, `{"accessToken":"access-fixture","refreshToken":"refresh-fixture","expiresIn":900,"user":{"name":"Test"}}`)
		case "/web-api/models":
			_, _ = fmt.Fprint(w, `{"success":true,"data":[{"name":"model-a","displayName":"Model A","available":true}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	for _, tc := range []struct {
		name      string
		args      []string
		wantModel string
	}{
		{"human", []string{"login", "--no-browser"}, "model-a"},
		{"desktop helper", []string{"login", "--machine"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			lines := make(lineWriter, 8)
			done := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			go func() { done <- runEasyCodeCommand(ctx, tc.args, home, server.URL, lines) }()
			var line string
			select {
			case line = <-lines:
			case err := <-done:
				t.Fatalf("login failed before browser URL: %v", err)
			case <-ctx.Done():
				t.Fatal("timed out waiting for login URL")
			}
			var loginURL string
			if tc.wantModel == "" {
				var event struct {
					URL string `json:"url"`
				}
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				loginURL = event.URL
			} else {
				_, loginURL, _ = strings.Cut(strings.TrimSpace(line), "：")
			}
			start, err := url.Parse(loginURL)
			require.NoError(t, err)
			start.Path = "/start"
			q := start.Query()
			q.Set("tenant", "0")
			start.RawQuery = q.Encode()
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Get(start.String())
			require.NoError(t, err)
			_ = response.Body.Close()
			require.Equal(t, http.StatusFound, response.StatusCode)
			authorize, err := url.Parse(response.Header.Get("Location"))
			require.NoError(t, err)
			callback, err := url.Parse(authorize.Query().Get("redirect_uri"))
			require.NoError(t, err)
			callback.Host = start.Host
			q = callback.Query()
			q.Set("code", "test-code")
			q.Set("state", authorize.Query().Get("state"))
			callback.RawQuery = q.Encode()
			response, err = client.Get(callback.String())
			require.NoError(t, err)
			_ = response.Body.Close()
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.NoError(t, <-done)
			complete := <-lines
			require.NotContains(t, complete, "access-fixture")
			require.NotContains(t, complete, "refresh-fixture")
			manager, err := easycode.NewManager(home, server.URL)
			require.NoError(t, err)
			selected, err := manager.CLIModel(ctx)
			require.NoError(t, err)
			require.Equal(t, tc.wantModel, selected)
		})
	}
}

func TestEasyCodeSSHInstructionsUseLoopback(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, printEasyCodeSSHInstructions(&out, "http://127.0.0.1:7863/?state=abc"))
	require.Contains(t, out.String(), "ssh -N -L 7863:127.0.0.1:7863")
}

func TestEasyCodeDotEnvAndDesktopIsolation(t *testing.T) {
	if helper := os.Getenv("EASYAGENT_TEST_EASYCODE_HELPER"); helper != "" {
		flag.CommandLine = flag.NewFlagSet("easyagent", flag.ContinueOnError)
		os.Args = []string{"easyagent", "easycode", "status"}
		if helper == "machine" {
			os.Args = append(os.Args, "--machine")
		}
		main()
		return
	}

	dir := t.TempDir()
	account := filepath.Join(dir, "account")
	require.NoError(t, os.MkdirAll(filepath.Join(account, "easycode"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(account, "easycode", "credentials.json"), []byte(`{"accessToken":"fixture","expiresAt":4102444800000}`), 0o600))
	goodEnv := filepath.Join(dir, "cli.env")
	require.NoError(t, os.WriteFile(goodEnv, []byte("EA_HOME="+account+"\n"), 0o600))
	badEnv := filepath.Join(dir, "desktop.env")
	require.NoError(t, os.WriteFile(badEnv, []byte("EA_EASYCODE_BASE_URL=http://not-loopback.invalid\n"), 0o600))
	var cleanEnv []string
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "EA_") && !strings.HasPrefix(env, "EASYAGENT_TEST_EASYCODE_HELPER=") {
			cleanEnv = append(cleanEnv, env)
		}
	}
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"human", []string{"EASYAGENT_TEST_EASYCODE_HELPER=human", "EA_ENV_FILE=" + goodEnv}, "已登录 EasyCode"},
		{"machine", []string{"EASYAGENT_TEST_EASYCODE_HELPER=machine", "EA_ENV_FILE=" + badEnv, "EA_HOME=" + account}, `"logged_in":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestEasyCodeDotEnvAndDesktopIsolation$")
			cmd.Env = append(append([]string{}, cleanEnv...), tc.env...)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.Contains(t, string(out), tc.want)
		})
	}
}
