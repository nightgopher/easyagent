package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEasyCodeCommandStatusDoesNotExposeCredentials(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "easycode")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"accessToken":"private-access","refreshToken":"private-refresh","expiresAt":4102444800000,"user":{"name":"Example"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"status"}, {"status", "--machine"}} {
		var output bytes.Buffer
		if err := runEasyCodeCommand(context.Background(), args, home, "http://127.0.0.1:1", &output); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "private-access") || strings.Contains(output.String(), "private-refresh") {
			t.Fatal("credential appeared in helper output")
		}
	}
	var output bytes.Buffer
	if err := runEasyCodeCommand(context.Background(), []string{"logout", "--machine"}, home, "http://127.0.0.1:1", &output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("logout left local credentials")
	}
}
