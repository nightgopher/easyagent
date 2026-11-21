package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDynamicWorkflowDetailCanOmitStaticGraph(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	if err := s.app.DynamicWorkflows().Available(); err != nil {
		t.Skipf("Node unavailable: %v", err)
	}
	id, err := s.createSessionRecord(context.Background(), "coding", "", "", "")
	require.NoError(t, err)
	run, err := s.app.DynamicWorkflows().Start(context.Background(), "graph", `const a=agent("researcher"); return await a.ask("test");`, id)
	require.NoError(t, err)
	// The mock provider holds its response, so the run stays live while the graph is read.
	require.Eventually(t, func() bool { r, e := s.app.DynamicWorkflows().Get(run.ID); return e == nil && len(r.Graph) > 0 }, 10*time.Second, 50*time.Millisecond)
	w := businessRequest(t, s, "GET", "/dynamic-workflows/"+run.ID, nil, 200)
	var full map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &full))
	require.NotNil(t, full["graph"])
	w = businessRequest(t, s, "GET", "/dynamic-workflows/"+run.ID+"?omit_graph=1", nil, 200)
	var light map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &light))
	require.NotContains(t, light, "graph")
	require.Equal(t, full["id"], light["id"])
	w = businessRequest(t, s, "GET", "/dynamic-workflows/"+run.ID, nil, 200)
	require.Contains(t, w.Body.String(), `"graph"`)
}

func TestDynamicWorkflowRoutesRequireAuthentication(t *testing.T) {
	srv := New(newTestApp(t), nil)
	srv.SetAPIKey("test-secret")
	for _, route := range []struct{ method, path string }{{"GET", "/dynamic-workflows"}, {"GET", "/dynamic-workflows/dw-000000000000000000000000"}, {"POST", "/dynamic-workflows/dw-000000000000000000000000/cancel"}, {"POST", "/dynamic-workflows/dw-000000000000000000000000/resume"}} {
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, localReq(route.method, route.path, nil))
		require.Equal(t, http.StatusUnauthorized, w.Code)
	}
	req := localReq("GET", "/dynamic-workflows", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"runs":[]`)
	req = localReq("POST", "/dynamic-workflows/dw-000000000000000000000000/resume", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer test-secret")
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}
