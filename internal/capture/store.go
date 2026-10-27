// Package capture stores explicitly saved fragments independently of chat history.
package capture

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const MaxText = 64 * 1024

var mu sync.Mutex
var ErrConflict = errors.New("record ID already contains different content")

type Record struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}
type Store struct{ Dir string }

func New(dataDir string) Store { return Store{filepath.Join(dataDir, "inbox")} }
func NewID() string            { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func ValidID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && id == strings.ToLower(id)
}
func (s Store) Save(r Record) (Record, error) {
	mu.Lock()
	defer mu.Unlock()
	if !ValidID(r.ID) || strings.TrimSpace(r.Text) == "" || len(r.Text) > MaxText || len(r.Source) > 2048 {
		return Record{}, fmt.Errorf("invalid record: require ID, 1–65536 bytes of text and bounded source")
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return Record{}, err
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return Record{}, err
	}
	defer root.Close()
	name := r.ID + ".json"
	if data, err := root.ReadFile(name); err == nil {
		var old Record
		if err := json.Unmarshal(data, &old); err != nil {
			return Record{}, err
		}
		if old.Text != r.Text || old.Source != r.Source {
			return Record{}, ErrConflict
		}
		return old, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Record{}, err
	}
	r.CreatedAt = time.Now().UTC()
	data, err := json.Marshal(r)
	if err != nil {
		return Record{}, err
	}
	f, err := os.CreateTemp(s.Dir, ".record-*")
	if err != nil {
		return Record{}, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return Record{}, err
	}
	if closeErr != nil {
		return Record{}, closeErr
	}
	// Hard-link publication is atomic and never overwrites a concurrent writer.
	if err := root.Link(filepath.Base(f.Name()), name); err != nil {
		return Record{}, err
	}
	return r, nil
}
func (s Store) List() ([]Record, error) {
	mu.Lock()
	defer mu.Unlock()
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	records := []Record{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || !ValidID(strings.TrimSuffix(entry.Name(), ".json")) {
			continue
		}
		data, err := root.ReadFile(entry.Name())
		if err != nil {
			return nil, err
		}
		var r Record
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.After(records[j].CreatedAt) })
	return records, nil
}
