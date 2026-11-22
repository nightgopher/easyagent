package main

import (
	"strings"
	"testing"
)

var scrollProfileResult string

func BenchmarkScrollMarkdownReferences(b *testing.B) {
	text := strings.Repeat("这是一段对话，包含 `~/project/file.go` 和一些普通内容。\n", 100)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		scrollProfileResult = markdownFileReferences(text)
	}
}

func BenchmarkScrollMarkdownReferencesCached(b *testing.B) {
	text := strings.Repeat("这是一段对话，包含 `~/project/file.go` 和一些普通内容。\n", 100)
	var doc markdownDocument
	doc.fileReferences(text)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scrollProfileResult = doc.fileReferences(text)
	}
}

func TestCachedFileReferencesFollowTextChanges(t *testing.T) {
	var doc markdownDocument
	for _, text := range []string{"", "查看 `~/first.go`", "查看 `~/first.go`", "```\n`~/second.go`\n```", "查看 `~/second.go`"} {
		if got, want := doc.fileReferences(text), markdownFileReferences(text); got != want {
			t.Fatalf("text %q: got %q, want %q", text, got, want)
		}
	}
}
