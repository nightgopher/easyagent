package ui

import (
	"strings"
	"testing"
	"time"
)

func TestReplacePatternTerminatesOnUnclosedBold(t *testing.T) {
	done := make(chan string, 1)
	go func() { done <- replacePattern("**未闭合 与 *斜体*", "", nil) }()
	select {
	case got := <-done:
		if !strings.Contains(got, "**未闭合") || !strings.Contains(got, ColorCyan+"斜体"+ColorReset) {
			t.Fatalf("unexpected rendering: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacePattern loops forever on an unclosed **")
	}
}
