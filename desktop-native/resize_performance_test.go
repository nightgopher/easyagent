package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"strings"
	"testing"
)

// BenchmarkWindowResize measures the headless resize path, including layout
// and software rendering, but excluding macOS compositing and GPU presentation.
func BenchmarkWindowResize(b *testing.B) {
	for _, count := range []int{0, 20, 100, 500} {
		b.Run(fmt.Sprintf("messages-%d", count), func(b *testing.B) {
			a := newPreview()
			a.Workbench = false
			a.current().Messages = nil
			for i := range count {
				a.current().Messages = append(a.current().Messages, &message{User: i%2 == 0, Text: strings.Repeat("窗口调整时需要重新计算换行。This is a representative reply with **formatted text**.\n\n", 6)})
			}
			tt := ui.NewTester(a.View, 1000, 720)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tt.SetSize(900+i%200, 720)
			}
		})
	}
}
