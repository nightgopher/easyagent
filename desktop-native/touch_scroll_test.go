package main

import (
	"github.com/egoist/mygo/ui"
	"testing"
	"time"
)

func TestReproConsecutiveSwipes(t *testing.T) {
	var scroll ui.ScrollState
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).Fill().TrackScroll(&scroll).Children(func() { ui.Box(c).Height(3000).Shrink(0) })
	}, 200, 200)
	tt.FingerDown(100, 170)
	tt.Advance(16 * time.Millisecond)
	tt.FingerMove(100, 130)
	tt.Advance(16 * time.Millisecond)
	tt.FingerMove(100, 90)
	tt.Advance(16 * time.Millisecond)
	tt.FingerUp(100, 50)
	tt.Advance(16 * time.Millisecond)
	tt.FingerDown(100, 170)
	before := scroll.Y
	tt.Advance(16 * time.Millisecond)
	tt.FingerMove(100, 130)
	tt.Advance(16 * time.Millisecond)
	tt.FingerMove(100, 80)
	after := scroll.Y
	tt.FingerUp(100, 80)
	if after <= before {
		t.Fatalf("second swipe did not move content: before %.1f, after %.1f", before, after)
	}
}
