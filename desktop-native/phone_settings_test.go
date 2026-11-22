package main

import "testing"

func TestPhoneSettingsOfferDeviceAndViewingPages(t *testing.T) {
	want := []string{"connections", "appearance", "speech", "general", "usage", "workflows"}
	pages := phoneSettingsPages()
	if len(pages) != len(want) {
		t.Fatalf("got %d phone settings, want %d", len(pages), len(want))
	}
	for i, id := range want {
		if pages[i].ID != id {
			t.Fatalf("phone settings[%d] = %q, want %q", i, pages[i].ID, id)
		}
	}
	if pages[0].Label != "连接电脑" {
		t.Fatalf("phone pairing entry should describe connecting a computer: %q", pages[0].Label)
	}
}
