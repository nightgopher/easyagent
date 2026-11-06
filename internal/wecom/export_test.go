package wecom

import (
	"net/http"
	"testing"
	"time"
)

// overrideEndpoints points the QR flow at a test server for the test lifetime.
func overrideEndpoints(t *testing.T, base string, client *http.Client) {
	t.Helper()
	origBase, origClient, origInterval, origTimeout := qrBase, httpClient, pollInterval, pollTimeout
	t.Cleanup(func() {
		qrBase, httpClient, pollInterval, pollTimeout = origBase, origClient, origInterval, origTimeout
	})
	qrBase = base
	if client != nil {
		httpClient = client
	}
}

// overridePoll shrinks the poll cadence and timeout so tests finish fast.
func overridePoll(t *testing.T, interval, timeout time.Duration) {
	t.Helper()
	origInterval, origTimeout := pollInterval, pollTimeout
	t.Cleanup(func() { pollInterval, pollTimeout = origInterval, origTimeout })
	pollInterval, pollTimeout = interval, timeout
}
