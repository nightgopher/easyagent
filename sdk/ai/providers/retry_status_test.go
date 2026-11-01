package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/stretchr/testify/require"
)

func TestModelRetryStatusAndCancellation(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			var hits atomic.Int32
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Retry-After", "60")
				http.Error(w, `{"error":"Too many failed attempts, retry later"}`, http.StatusTooManyRequests)
			}))
			defer gateway.Close()
			var provider Provider = NewOpenAIProvider("test-key", gateway.URL)
			if kind == "anthropic" {
				provider = NewAnthropicProvider("test-key", gateway.URL)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, err := provider.Stream(ctx, ai.StreamRequest{Model: ai.Model{ID: "test"}})
			require.NoError(t, err)
			select {
			case <-stream.Events(): // start
			case <-time.After(time.Second):
				t.Fatal("stream did not start")
			}
			select {
			case event := <-stream.Events():
				retry, ok := event.(ai.EventRetry)
				require.True(t, ok, "must report waiting before sleeping")
				require.Equal(t, int64(60000), retry.Retry.DelayMS)
				require.Equal(t, 1, retry.Retry.Attempt)
				require.Equal(t, 3, retry.Retry.MaxAttempts)
				require.InDelta(t, time.Now().Add(time.Minute).UnixMilli(), retry.Retry.RetryAt, 1000)
			case <-time.After(time.Second):
				t.Fatal("retry is invisible or a normal cooldown is rejected")
			}
			cancel()
			finished := make(chan struct{})
			go func() {
				for range stream.Events() {
				}
				close(finished)
			}()
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("cancel must interrupt backoff")
			}
			_, err = stream.Result()
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, int32(1), hits.Load())
		})
	}
}

func TestModelPermanentQuotaDoesNotRetry(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			fastModelRetry(t)
			var hits atomic.Int32
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				http.Error(w, `{"error":{"code":"insufficient_quota"}}`, http.StatusTooManyRequests)
			}))
			defer gateway.Close()
			var provider Provider = NewOpenAIProvider("test-key", gateway.URL)
			if kind == "anthropic" {
				provider = NewAnthropicProvider("test-key", gateway.URL)
			}
			stream, err := provider.Stream(context.Background(), ai.StreamRequest{Model: ai.Model{ID: "test"}})
			require.NoError(t, err)
			for event := range stream.Events() {
				_, retry := event.(ai.EventRetry)
				require.False(t, retry)
			}
			_, err = stream.Result()
			require.Error(t, err)
			require.Equal(t, int32(1), hits.Load())
		})
	}
}
