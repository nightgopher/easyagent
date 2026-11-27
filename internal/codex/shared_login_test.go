package codex

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentLoginDoesNotReplaceExistingAccount(t *testing.T) {
	home := t.TempDir()
	a, err := NewManager(home, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewManager(home, "")
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for _, m := range []*Manager{a, b} {
		wg.Add(1)
		go func(m *Manager) {
			defer wg.Done()
			if m.saveNewLogin(context.Background(), credentials{AccessToken: "test", AccountID: "account"}) == nil {
				successes.Add(1)
			}
		}(m)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("expected one winning login, got %d", successes.Load())
	}
}
