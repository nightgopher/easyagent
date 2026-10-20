package sessionmgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sessionMetadata keeps unmodified fields as JSON, including numbers that
// would lose precision if decoded into interface{} as float64.
type sessionMetadata map[string]json.RawMessage

// readMetadata treats missing and null metadata as an empty object. Other
// read or format errors remain visible to callers that update or fork it.
func readMetadata(dir string) (sessionMetadata, error) {
	meta := sessionMetadata{}
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if os.IsNotExist(err) {
		return meta, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session metadata: %w", err)
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("read session metadata: %w", err)
	}
	if meta == nil {
		meta = sessionMetadata{}
	}
	return meta, nil
}

func (meta sessionMetadata) decodeField(name string, value any) error {
	if data, ok := meta[name]; ok {
		return json.Unmarshal(data, value)
	}
	// Older typed readers also accepted case-insensitive JSON field names.
	for key, data := range meta {
		if strings.EqualFold(key, name) {
			return json.Unmarshal(data, value)
		}
	}
	return nil
}

// write patches only supplied fields, then atomically replaces the file.
// Manager serializes read-modify-write operations with metaMu.
func (meta sessionMetadata) write(dir string, fields map[string]any) error {
	for name, value := range fields {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		meta[name] = data
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".meta-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(dir, "meta.json"))
}
