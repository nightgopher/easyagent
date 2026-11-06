package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestComputerHelperCacheInvalidatesSourceAndToolchain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the computer helper cache is a macOS-only build step (xcrun/swiftc)")
	}
	root := t.TempDir()
	cwd := filepath.Join(root, "desktop-native")
	source := filepath.Join(root, "native/computer-use/main.swift")
	for _, directory := range []string{filepath.Dir(source), cwd, filepath.Join(root, "bin")} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	// A fake compiler records actual invocations; a cache hit must avoid it.
	write(filepath.Join(root, "bin/xcrun"), `#!/bin/sh
if [ "$2" = --version ] || [ "$1" = --sdk ]; then echo "$TEST_SDK_VERSION"; exit; fi
echo compiled >> "$TEST_COMPILE_LOG"
while [ "$1" != -o ]; do shift; done
echo executable > "$2"
`, 0755)
	log := filepath.Join(root, "compiles")
	t.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_SDK_VERSION", "1")
	t.Setenv("TEST_COMPILE_LOG", log)
	output := filepath.Join(root, "output/helper")
	build := func(want int) {
		t.Helper()
		buildComputerHelper(root, cwd, "arm64", "/sdk", output)
		data, err := os.ReadFile(log)
		if err != nil || strings.Count(string(data), "compiled") != want {
			t.Fatalf("compiler calls: %q, %v; want %d", data, err, want)
		}
		if data, err := os.ReadFile(output); err != nil || string(data) != "executable\n" {
			t.Fatalf("missing packaged helper: %q, %v", data, err)
		}
	}
	write(source, "first source", 0644)
	build(1)
	build(1)
	write(source, "changed source", 0644)
	build(2)
	t.Setenv("TEST_SDK_VERSION", "2")
	build(3)
	if err := os.Remove(filepath.Join(cwd, "build.noindex/helper-cache/arm64/easyagent-cua-helper")); err != nil {
		t.Fatal(err)
	}
	build(4)
}

func TestResetResourcesRemovesPreviousRuntimeOnly(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "darwin-arm64")
	stale := filepath.Join(target, "runtime", "node")
	if err := os.MkdirAll(filepath.Dir(stale), 0755); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(root, "other-target")
	for _, path := range []string{stale, filepath.Join(target, "workflow-runtime.mjs"), sibling} {
		if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := resetResources(target); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("stale resources: %v, %v", entries, err)
	}
	if data, err := os.ReadFile(sibling); err != nil || string(data) != "old" {
		t.Fatalf("other target changed: %s, %v", data, err)
	}
}
