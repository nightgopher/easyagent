package server

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEmbeddingConfigAPIDoesNotReturnKeys(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	businessRequest(t, s, "POST", "/knowledge/embeddings/test", nil, 400)
	input := map[string]any{"enabled": true, "endpoint": "https://api.siliconflow.cn/v1/embeddings", "model": "BAAI/bge-m3", "api_key": "secret-example"}
	w := businessRequest(t, s, "PUT", "/knowledge/embeddings", input, 200)
	require.NotContains(t, w.Body.String(), "secret-example")
	require.Contains(t, w.Body.String(), `"has_key":true`)
	w = businessRequest(t, s, "GET", "/knowledge/embeddings", nil, 200)
	require.NotContains(t, w.Body.String(), "secret-example")
	input["endpoint"] = "https://other.example/v1/embeddings"
	input["api_key"] = ""
	businessRequest(t, s, "PUT", "/knowledge/embeddings", input, 400)
	input["endpoint"] = "https://api.siliconflow.cn/v1/embeddings"
	input["enabled"] = false
	input["clear_key"] = true
	w = businessRequest(t, s, "PUT", "/knowledge/embeddings", input, 200)
	require.Contains(t, w.Body.String(), `"has_key":false`)
	s.SetAPIKey("host-token")
	businessRequest(t, s, "GET", "/knowledge/embeddings", nil, 401)
}
