package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
	"github.com/stretchr/testify/require"
)

func TestSessionSearchHTTPAuthPersistenceAndHistory(t *testing.T) {
	application := newTestApp(t)
	mgr := application.SessionManager()
	id, _, err := mgr.Create(context.Background())
	require.NoError(t, err)
	sess, _, err := mgr.Open(context.Background(), id)
	require.NoError(t, err)
	defer func() { require.NoError(t, sess.Storage().Close()) }()
	require.NoError(t, sess.AppendMessage(context.Background(), ai.NewTextUserMessage("不同的标题")))
	require.NoError(t, sess.AppendMessage(context.Background(), ai.AssistantMessage{Text: "历史内容：迷你主机令牌"}))
	ids, err := sess.BuildContextEntryIDs(context.Background())
	require.NoError(t, err)
	require.NoError(t, sess.AppendCompaction(context.Background(), "摘要"))
	srv := New(application, nil)
	defer srv.cancel()
	srv.SetAPIKey("search-test-token")
	request := func(path, token string) *httptest.ResponseRecorder {
		r := localReq(http.MethodGet, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}
	path := "/sessions/search?q=" + url.QueryEscape("迷你主机")
	require.Equal(t, http.StatusUnauthorized, request(path, "").Code)
	w := request(path, "search-test-token")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var result sessionmgr.SearchResults
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Len(t, result.Hits, 1)
	require.Equal(t, ids[1], result.Hits[0].EntryID)
	historyPath := "/sessions/" + id + "/history?entry_id=" + ids[1]
	require.Equal(t, http.StatusUnauthorized, request(historyPath, "").Code)
	w = request(historyPath, "search-test-token")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "历史内容：迷你主机令牌")
	require.Equal(t, http.StatusNotFound, request("/sessions/missing/history?entry_id="+ids[1], "search-test-token").Code)
	// Searching must not load all historical sessions into the runtime registry.
	_, loaded := application.SessionStore().Get(id)
	require.False(t, loaded)
	for _, suffix := range []string{"", "?q=x&limit=0", "?q=x&limit=101", "?q=x&offset=-1", "?q=x&offset=no", "?q=x&since=no", "?q=x&archived=bad", "?q=%00"} {
		require.Equal(t, http.StatusBadRequest, request("/sessions/search"+suffix, "search-test-token").Code, suffix)
	}
	require.Equal(t, http.StatusBadRequest, request("/sessions/"+id+"/history", "search-test-token").Code)
	// The indexed copy is removed when its source session disappears.
	require.NoError(t, mgr.Delete(id))
	w = request(path, "search-test-token")
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Empty(t, result.Hits)
}
