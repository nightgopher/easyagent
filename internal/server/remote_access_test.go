package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoteAccessManagementRequiresLocalOwnerEvenWithOpenAccess(t *testing.T) {
	s := New(newTestAppWithWorkspace(t, t.TempDir()), nil)
	s.allowNoAuth = true
	for _, tc := range []struct {
		key, provided, remote string
		want                  int
	}{
		{"", "", "127.0.0.1:9000", 403},
		{"owner", "", "127.0.0.1:9000", 401},
		{"owner", "owner", "192.0.2.1:9000", 403},
		{"owner", "owner", "127.0.0.1:9000", 200},
	} {
		s.SetAPIKey(tc.key)
		r := httptest.NewRequest("GET", "/remote-access", nil)
		r.RemoteAddr = tc.remote
		if tc.provided != "" {
			r.Header.Set("Authorization", "Bearer "+tc.provided)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		require.Equal(t, tc.want, w.Code, w.Body.String())
		if w.Code == http.StatusOK {
			require.NotContains(t, w.Body.String(), "host_token")
			require.NotContains(t, w.Body.String(), "private_key")
		}
	}
	if s.remoteHost != nil {
		require.NoError(t, s.remoteHost.Close())
	}
}
