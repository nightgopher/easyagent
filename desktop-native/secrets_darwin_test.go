//go:build darwin

package main

import (
	"github.com/ebitengine/purego"
	"os"
	"testing"
)

func TestNativeSystemKeychainRoundTrip(t *testing.T) {
	if os.Getenv("EASYAGENT_NATIVE_KEYCHAIN_TEST") != "1" {
		t.Skip("opt-in synthetic keychain check")
	}
	store := systemSecrets{}
	account := "integration/issue209/" + requestID()
	defer func() {
		if err := store.Delete(account); err != nil {
			t.Errorf("synthetic keychain cleanup failed: %v", err)
		}
	}()
	if err := store.Set(account, "synthetic-native-test"); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(account)
	if err != nil || value != "synthetic-native-test" {
		t.Fatalf("keychain roundtrip failed: %v", err)
	}
	if err = store.Set(account, "synthetic-updated"); err != nil {
		t.Fatal(err)
	}
	value, err = store.Get(account)
	if err != nil || value != "synthetic-updated" {
		t.Fatalf("keychain update failed: %v", err)
	}
	if err = store.Delete(account); err != nil {
		t.Fatal(err)
	}
	value, err = store.Get(account)
	if err != nil || value != "" {
		t.Fatalf("keychain delete failed: %v", err)
	}
}

func TestNativeKeychainSavedHostAuthentication(t *testing.T) {
	if os.Getenv("EASYAGENT_NATIVE_KEYCHAIN_TEST") != "1" {
		t.Skip("opt-in synthetic keychain check")
	}
	testSavedHostAuthentication(t, systemSecrets{})
}

func TestNativeKeychainQueryScopesDefaultStore(t *testing.T) {
	if os.Getenv("EASYAGENT_NATIVE_KEYCHAIN_TEST") != "1" {
		t.Skip("opt-in synthetic keychain check")
	}
	if err := initKeychain(); err != nil {
		t.Fatal(err)
	}
	q, target, done, err := keychainQuery("integration/query-only", true)
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
}
