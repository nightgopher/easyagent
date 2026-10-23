package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Feishu bridge registers its tool through the full handler; /tools/
// must not fall through to the web UI catch-all.
func TestToolsRegisterIsRoutedThroughTheHandler(t *testing.T) {
	srv := New(newTestAppWithWorkspace(t, t.TempDir()), nil)
	w := httptest.NewRecorder()
	body := `{"name":"lookup","callback_url":"http://127.0.0.1:9/cb","allow_private":true}`
	srv.Handler().ServeHTTP(w, localReq(http.MethodPost, "/tools/register", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, srv.ExternalTools(), 1)
}
