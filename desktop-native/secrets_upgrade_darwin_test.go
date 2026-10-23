//go:build darwin

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/hwj123hwj/easyagent/desktop-native/internal/credentialstore"
)

var nativeKeychainProbeBuild = "first"

func TestNativeKeychainUpgradeProbe(t *testing.T) {
	action := os.Getenv("EASYAGENT_KEYCHAIN_PROBE_ACTION")
	if action == "" {
		t.Skip("signed upgrade fixture only")
	}
	path := os.Getenv("EASYAGENT_KEYCHAIN_PROBE_PATH")
	if !strings.HasSuffix(path, ".noindex/probe.keychain-db") {
		t.Fatal("probe requires its disposable keychain")
	}
	if err := credentialstore.DisableInteraction(); err != nil {
		t.Fatal(err)
	}
	t.Logf("build=%s action=%s", nativeKeychainProbeBuild, action)
	store := systemSecrets{path: path, dir: os.Getenv("EASYAGENT_KEYCHAIN_PROBE_CACHE"), helper: os.Getenv("EASYAGENT_KEYCHAIN_PROBE_HELPER")}
	legacy := credentialstore.Store{Path: path, Service: credentialstore.LegacyService}
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	read := func(account, expected string) {
		t.Helper()
		value, err := store.Get(account)
		if err != nil || value != expected {
			t.Fatalf("synthetic %s did not survive: %v", account, err)
		}
	}
	switch action {
	case "seed":
		check(store.Set("host/upgrade", "synthetic-host"))
		check(store.Set("model", "synthetic-model"))
		read("host/upgrade", "synthetic-host")
		read("model", "synthetic-model")
		for _, account := range []string{"host/migrate", "host/blocked", "model/blocked", "host/clear", "host/remove"} {
			check(legacy.Set(account, "synthetic-legacy"))
			if value, found, err := legacy.Get(account); err != nil || !found || value != "synthetic-legacy" {
				t.Fatalf("seed legacy: %v", err)
			}
		}
	case "migrate":
		read("host/migrate", "synthetic-legacy")
		if _, found, err := legacy.Get("host/migrate"); err != nil || !found {
			t.Fatalf("migration changed legacy: %v", err)
		}
	case "upgrade":
		read("host/upgrade", "synthetic-host")
		read("model", "synthetic-model")
		read("host/migrate", "synthetic-legacy")
		check(store.Set("host/upgrade", "synthetic-next"))
		read("host/upgrade", "synthetic-next")
	case "repair":
		for _, account := range []string{"host/blocked", "model/blocked"} {
			if value, err := store.Get(account); err == nil || value != "" || !strings.Contains(err.Error(), "重新填写") {
				t.Fatal("legacy cdhash restriction was bypassed or lacked guidance")
			}
			check(store.Set(account, "synthetic-replacement"))
			read(account, "synthetic-replacement")
			if _, _, err := legacy.Get(account); err == nil {
				t.Fatal("repair altered protected legacy item")
			}
		}
	case "remove":
		if value, err := store.Get("host/remove"); err == nil || value != "" {
			t.Fatal("removal fixture must have an inaccessible legacy credential")
		}
		config := clientConfig{ActiveHost: "local", Hosts: []hostConfig{
			{ID: "local", Local: true}, {ID: "remove", HasToken: true}, {ID: "upgrade", HasToken: true},
		}}
		removed, err := persistHostRemoval(t.TempDir(), store, config, 1)
		check(err)
		check(removed.CleanupError)
		if len(removed.Config.Hosts) != 2 || removed.Config.Hosts[1].ID != "upgrade" {
			t.Fatal("removal changed another profile")
		}
		read("host/remove", "")
		read("host/upgrade", "synthetic-next")
		read("model", "synthetic-model")
	case "helper-upgrade":
		read("host/upgrade", "synthetic-next")
		read("model", "synthetic-model")
		read("host/migrate", "synthetic-legacy")
	case "rotate":
		check(store.Set("host/upgrade", "synthetic-newest"))
		read("host/upgrade", "synthetic-newest")
	case "latest":
		read("host/upgrade", "synthetic-newest")
		read("model", "synthetic-model")
	case "parallel":
		account := os.Getenv("EASYAGENT_KEYCHAIN_PROBE_ACCOUNT")
		for range 3 {
			check(store.Set(account, "synthetic-concurrent"))
			read(account, "synthetic-concurrent")
		}
	case "parallel-result":
		read("host/concurrent-a", "synthetic-concurrent")
		read("host/concurrent-b", "synthetic-concurrent")
	case "clear":
		check(store.Delete("host/clear"))
		read("host/clear", "")
		check(store.Delete("host/upgrade"))
		read("host/upgrade", "")
	case "relaunch":
		read("host/remove", "")
		read("host/upgrade", "")
		read("host/clear", "")
		read("host/blocked", "synthetic-replacement")
		read("model/blocked", "synthetic-replacement")
		read("model", "synthetic-model")
		index := store.dir + "/credential-helpers.noindex/owners.json"
		owners, err := os.ReadFile(index)
		check(err)
		if strings.Contains(string(owners), "synthetic-") {
			t.Fatal("a credential leaked into the ownership index")
		}
		check(os.WriteFile(index, []byte("{"), 0600))
		if value, err := store.Get("model"); err == nil || value != "" {
			t.Fatal("corrupt ownership metadata guessed a credential generation")
		}
		check(os.WriteFile(index, owners, 0600))
		read("host/migrate", "synthetic-legacy")
	case "reject":
		if value, err := store.Get("model"); err == nil || value != "" {
			t.Fatal("untrusted parent read credential")
		}
		if store.Set("model", "synthetic-tamper") == nil {
			t.Fatal("untrusted parent overwrote credential")
		}
		if store.Delete("model") == nil {
			t.Fatal("untrusted parent cleared credential")
		}
		if response, err := store.request(store.helper, "get", "model", ""); err == nil || response.Value != "" {
			t.Fatal("direct helper invocation bypassed parent authentication")
		}
	case "network":
		testSavedHostAuthentication(t, store)
	case "locked":
		if value, err := store.Get("model"); err == nil || value != "" {
			t.Fatal("locked keychain did not fail without exposing a credential")
		}
	case "tamper":
		paths, err := store.helperPaths()
		check(err)
		check(os.Chmod(paths[0], 0755))
		if value, err := store.Get("model"); err == nil || value != "" {
			t.Fatal("unsafe helper cache permissions were accepted")
		}
		check(os.Chmod(paths[0], 0500))
		data, err := os.ReadFile(paths[0])
		check(err)
		changed := append([]byte{}, data...)
		changed[len(changed)/2] ^= 1
		check(os.Chmod(paths[0], 0700))
		check(os.WriteFile(paths[0], changed, 0500))
		check(os.Chmod(paths[0], 0500))
		if value, err := store.Get("model"); err == nil || value != "" {
			t.Fatal("modified helper code was accepted")
		}
		check(os.Chmod(paths[0], 0700))
		check(os.WriteFile(paths[0], data, 0500))
		check(os.Chmod(paths[0], 0500))
		read("model", "synthetic-model")
	default:
		t.Fatalf("unknown action: %s", action)
	}
}
