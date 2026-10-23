//go:build darwin

package credentialstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ebitengine/purego"
)

func nativeTestSecretStore(t *testing.T) Store {
	t.Helper()
	if os.Getenv("EASYAGENT_NATIVE_KEYCHAIN_TEST") != "1" {
		t.Skip("opt-in synthetic keychain check")
	}
	if err := initKeychain(); err != nil {
		t.Fatal(err)
	}
	var create func(string, uint32, *byte, bool, uintptr, *uintptr) int32
	var remove func(uintptr) int32
	purego.RegisterLibFunc(&create, keychain.security, "SecKeychainCreate")
	purego.RegisterLibFunc(&remove, keychain.security, "SecKeychainDelete")
	path := filepath.Join(t.TempDir(), "test.keychain-db")
	password := []byte("synthetic-test-keychain")
	var target uintptr
	if status := create(path, uint32(len(password)), &password[0], false, 0, &target); status != 0 {
		t.Fatalf("create isolated keychain: %d", status)
	}
	t.Cleanup(func() {
		if status := remove(target); status != 0 {
			t.Errorf("remove isolated keychain: %d", status)
		}
		keychain.release(target)
	})
	return Store{Path: path, Service: "com.hwj.easyagent.test"}
}

func TestNativeSystemKeychainRoundTrip(t *testing.T) {
	store := nativeTestSecretStore(t)
	account := "integration/issue209/" + "round-trip"
	defer func() {
		if err := store.Delete(account); err != nil {
			t.Errorf("synthetic keychain cleanup failed: %v", err)
		}
	}()
	if err := store.Set(account, "synthetic-native-test"); err != nil {
		t.Fatal(err)
	}
	value, _, err := store.Get(account)
	if err != nil || value != "synthetic-native-test" {
		t.Fatalf("keychain roundtrip failed: %v", err)
	}
	if err = store.Set(account, "synthetic-updated"); err != nil {
		t.Fatal(err)
	}
	value, _, err = store.Get(account)
	if err != nil || value != "synthetic-updated" {
		t.Fatalf("keychain update failed: %v", err)
	}
	if err = store.Delete(account); err != nil {
		t.Fatal(err)
	}
	value, _, err = store.Get(account)
	if err != nil || value != "" {
		t.Fatalf("keychain delete failed: %v", err)
	}
}

func TestNativeKeychainQueryScopesDefaultStore(t *testing.T) {
	if os.Getenv("EASYAGENT_NATIVE_KEYCHAIN_TEST") != "1" {
		t.Skip("opt-in synthetic keychain check")
	}
	if err := initKeychain(); err != nil {
		t.Fatal(err)
	}
	q, target, done, err := (Store{Service: "com.hwj.easyagent.test"}).query("integration/query-only", true)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var get func(uintptr, uintptr) uintptr
	var count func(uintptr) int64
	var item func(uintptr, int64) uintptr
	purego.RegisterLibFunc(&get, keychain.core, "CFDictionaryGetValue")
	purego.RegisterLibFunc(&count, keychain.core, "CFArrayGetCount")
	purego.RegisterLibFunc(&item, keychain.core, "CFArrayGetValueAtIndex")
	search := get(q, keychainConstant("kSecMatchSearchList"))
	if search == 0 || count(search) != 1 || item(search, 0) != target {
		t.Fatal("credential query relies on the global search list")
	}
	if get(q, keychainConstant("kSecUseAuthenticationUI")) != keychainConstant("kSecUseAuthenticationUIFail") {
		t.Fatal("credential query can trigger an operating-system password dialog")
	}
}
