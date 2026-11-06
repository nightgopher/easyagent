package wecom

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialsRoundtrip(t *testing.T) {
	t.Setenv("EA_HOME", t.TempDir())
	if err := SaveCredentials(Credentials{BotID: "bot-1", BotSecret: "sec-1"}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	creds, err := LoadCredentials()
	if err != nil || creds == nil {
		t.Fatalf("LoadCredentials: %+v err=%v", creds, err)
	}
	if creds.BotID != "bot-1" || creds.BotSecret != "sec-1" {
		t.Fatalf("unexpected credentials: %+v", creds)
	}
}

func TestLoadCredentialsMissing(t *testing.T) {
	t.Setenv("EA_HOME", t.TempDir())
	creds, err := LoadCredentials()
	if creds != nil || err != nil {
		t.Fatalf("want nil,nil, got %+v,%v", creds, err)
	}
}

func TestLoadCredentialsIncomplete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "wecom-credentials.json"), []byte(`{"bot_id":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(); err == nil {
		t.Fatal("want error for incomplete credentials")
	}
}

func TestDeleteCredentials(t *testing.T) {
	t.Setenv("EA_HOME", t.TempDir())
	if err := SaveCredentials(Credentials{BotID: "bot-1", BotSecret: "sec-1"}); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if err := DeleteCredentials(); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}
	creds, err := LoadCredentials()
	if creds != nil || err != nil {
		t.Fatalf("want nil,nil after delete, got %+v,%v", creds, err)
	}
	if err := DeleteCredentials(); err != nil {
		t.Fatalf("second delete must tolerate missing file: %v", err)
	}
}
