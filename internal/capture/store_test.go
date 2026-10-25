package capture

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSaveSurvivesRestartAndRetry(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	r := Record{ID: NewID(), Text: "明天研究 https://example.com", Source: "voice"}
	first, err := s.Save(r)
	require.NoError(t, err)
	again, err := New(dir).Save(r)
	require.NoError(t, err)
	require.Equal(t, first, again)
	records, err := New(dir).List()
	require.NoError(t, err)
	require.Equal(t, []Record{first}, records)
	r.Text = "changed"
	_, err = s.Save(r)
	require.ErrorIs(t, err, ErrConflict)
	records, err = s.List()
	require.NoError(t, err)
	require.Equal(t, first, records[0])
}
func TestStoreRejectsInvalidAndReportsIOFailure(t *testing.T) {
	s := New(t.TempDir())
	for _, r := range []Record{{ID: "../escape", Text: "x"}, {ID: NewID(), Text: " "}, {ID: NewID(), Text: strings.Repeat("x", MaxText+1)}} {
		_, err := s.Save(r)
		require.Error(t, err)
	}
	require.NoError(t, os.WriteFile(s.Dir, []byte("blocked"), 0600))
	_, err := s.Save(Record{ID: NewID(), Text: "keep me"})
	require.Error(t, err)
}
func TestConcurrentSavesDoNotLoseRecords(t *testing.T) {
	s := New(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Save(Record{ID: NewID(), Text: "capture"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	records, err := s.List()
	require.NoError(t, err)
	require.Len(t, records, 20)
	require.NoError(t, os.WriteFile(filepath.Join(s.Dir, ".record-incomplete"), []byte("bad"), 0600))
	_, err = s.List()
	require.NoError(t, err)
}
func TestCaptureToolWritesToSameInbox(t *testing.T) {
	s := New(t.TempDir())
	tool := Tool{Store: s, Source: "session:test"}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"text":"保存想法"}`), nil)
	require.NoError(t, err)
	require.False(t, result.IsError)
	records, err := s.List()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "session:test", records[0].Source)
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"text":""}`), nil)
	require.Error(t, err)
}
