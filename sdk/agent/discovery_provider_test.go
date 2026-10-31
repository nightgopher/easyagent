package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryFullLoopProviderWireRequests(t *testing.T) {
	for _, providerName := range []string{"openai", "anthropic"} {
		t.Run(providerName, func(t *testing.T) {
			var mu sync.Mutex
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				requests = append(requests, body)
				turn := len(requests)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				name, args := SearchToolsName, `{"query":"mcp__oa__pending"}`
				if turn == 2 {
					name, args = "mcp__oa__pending", `{"input":"leave"}`
				}
				if providerName == "openai" {
					if turn <= 2 {
						fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-%d\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%q}}]},\"finish_reason\":null}]}\n\n", turn, name, args)
						fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
					} else {
						fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					}
				} else {
					fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":10}}}\n\n")
					if turn <= 2 {
						fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-%d\",\"name\":%q,\"input\":{}}}\n\n", turn, name)
						fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\n", args)
						fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n")
					} else {
						fmt.Fprint(w, `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}

`)
					}
					fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				}
			}))
			defer server.Close()
			registry := providers.NewRegistry()
			if providerName == "openai" {
				registry.Register(providers.NewOpenAIProvider("fixture", server.URL))
			} else {
				registry.Register(providers.NewAnthropicProvider("fixture", server.URL))
			}
			tools := discoveryTools()
			tools[1].(*discoveryTestTool).schema = map[string]any{"type": "object", "properties": map[string]any{"UNIQUE_DEFERRED_PARAMETER": map[string]any{"type": "string"}}}
			_, summary, err := DiscoveryPromptTools(tools, ToolSearchLocal)
			require.NoError(t, err)
			a := New(Options{Registry: registry, Model: ai.Model{ID: "test-model", Provider: providerName, ContextWindow: 128000, MaxTokens: 1000}, Tools: tools, ToolSearchMode: ToolSearchLocal, System: summary, MaxTurns: 3})
			_, err = a.Prompt(context.Background(), ai.NewTextUserMessage("find the pending leave tool"))
			require.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, requests, 3)
			first, _ := json.Marshal(requests[0])
			second, _ := json.Marshal(requests[1])
			require.NotContains(t, string(first), "UNIQUE_DEFERRED_PARAMETER")
			require.Contains(t, string(second), "UNIQUE_DEFERRED_PARAMETER")
			require.Len(t, requests[0]["tools"], 2)
			require.Len(t, requests[1]["tools"], 3)
			last, _ := json.Marshal(requests[2]["messages"])
			require.Contains(t, string(last), "done")
		})
	}
}
