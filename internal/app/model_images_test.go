package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hwj123hwj/easyagent/sdk/agent"
	"github.com/hwj123hwj/easyagent/sdk/ai"
	"github.com/hwj123hwj/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

// Exercise gateway metadata -> runtime selection -> real read tool -> HTTP payload.
func TestModelImageCapabilitiesReachActualRequests(t *testing.T) {
	for _, model := range []string{"text-only", "vision", "unknown"} {
		t.Run(model, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			t.Setenv("EA_MODELS_FILE", filepath.Join(dir, "models.json"))
			var pngData bytes.Buffer
			require.NoError(t, png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "shot.png"), pngData.Bytes(), 0600))
			var mu sync.Mutex
			var payloads []string
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = io.WriteString(w, `{"data":[{"id":"text-only","input_modalities":["text"]},{"id":"vision","supports_images":true},{"id":"unknown"}]}`)
					return
				}
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				mu.Lock()
				payloads = append(payloads, string(data))
				n := len(payloads)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				chunk := `{"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`
				if n == 1 {
					args, _ := json.Marshal(map[string]string{"path": "shot.png"})
					chunk = fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"read-1","type":"function","function":{"name":"read","arguments":%q}}]},"finish_reason":"tool_calls"}]}`, string(args))
				}
				_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			}))
			defer gateway.Close()
			cfg := config.Default()
			cfg.DataDir, cfg.Workspace = dir, dir
			cfg.MCPConfigPath = filepath.Join(dir, "mcp.json")
			cfg.Provider, cfg.OpenAIBaseURL, cfg.OpenAIAPIKey, cfg.OpenAIModel = "openai", gateway.URL, "test-key", model
			application, err := New(AppOptions{Config: cfg})
			require.NoError(t, err)
			defer func() { require.NoError(t, application.Close()) }()
			sess, err := application.LoadOrCreateSession(ctx, "")
			require.NoError(t, err)
			stream, err := sess.PromptMessageStream(ctx, ai.UserMessage{Content: []ai.ContentBlock{
				{Type: "text", Text: "read shot.png"}, {Type: "image", Image: &ai.ImageBlock{MediaType: "image/png", Data: pngData.Bytes()}},
			}})
			require.NoError(t, err)
			sawHint := false
			for event := range stream {
				require.NotEqual(t, agent.StreamEventError, event.Type, event.Error)
				if event.Type == agent.StreamEventToolEnd && event.ToolName == "read" {
					sawHint = bytes.Contains([]byte(fmt.Sprint(event.ToolResult)), []byte(ai.ImageUnsupportedMessage))
				}
			}
			mu.Lock()
			requests := append([]string(nil), payloads...)
			mu.Unlock()
			require.Len(t, requests, 2)
			for _, request := range requests {
				if model == "text-only" {
					require.NotContains(t, request, `"type":"image`)
					require.NotContains(t, request, "data:image/png")
					require.Contains(t, request, "vision-capable")
				} else {
					require.Contains(t, request, "data:image/png")
				}
			}
			require.Equal(t, model == "text-only", sawHint)
			history, err := sess.Session().BuildContext(ctx)
			require.NoError(t, err)
			require.Equal(t, "image", history[0].(ai.UserMessage).Content[1].Type, "stored user images remain available after switching models")
			require.NoError(t, sess.SwitchModel(ctx, "vision", "openai"))
			snapshot, err := sess.ContextSnapshot(ctx)
			require.NoError(t, err)
			require.NotNil(t, snapshot.Model.SupportsImages)
			require.True(t, *snapshot.Model.SupportsImages)
		})
	}
}
