package dynamicflow

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

//go:embed workflow-runtime.mjs.gz
var bundledRuntime []byte

//go:embed ZCODE-LICENSE.txt ZCODE-NOTICE.txt ZCODE-THIRD-PARTY-NOTICES.txt
var licenseFiles embed.FS

// InstallRuntime extracts the pinned engine, not Node.js, into the private data directory.
func InstallRuntime(dataDir string) (string, error) {
	reader, err := gzip.NewReader(bytes.NewReader(bundledRuntime))
	if err != nil {
		return "", err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	dir := filepath.Join(dataDir, "workflow-runtime")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	for _, name := range []string{"ZCODE-LICENSE.txt", "ZCODE-NOTICE.txt", "ZCODE-THIRD-PARTY-NOTICES.txt"} {
		content, err := licenseFiles.ReadFile(name)
		if err != nil {
			return "", err
		}
		if err = os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			return "", err
		}
	}
	path := filepath.Join(dir, fmt.Sprintf("%x.mjs", sum[:]))
	if current, err := os.ReadFile(path); err == nil && sha256.Sum256(current) == sum { // #nosec G304 -- path is a fixed digest under the configured private runtime directory.
		return path, nil
	}
	f, err := os.CreateTemp(dir, ".runtime-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
