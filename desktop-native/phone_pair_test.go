package main

import (
	"bytes"
	"github.com/skip2/go-qrcode"
	"image"
	"image/jpeg"
	"strings"
	"testing"
	"time"
)

func TestPhoneDecodePairImage(t *testing.T) {
	raw := `{"v":1,"relay_url":"https://relay.example","route_id":"` + strings.Repeat("a", 43) + `","host_pubkey_fp":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","pair_token":"` + strings.Repeat("b", 43) + `","expires_at":"` + time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339) + `"}`
	png, err := qrcode.Encode(raw, qrcode.Medium, 1200)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(png))
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err = jpeg.Encode(&data, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	captured, err := jpeg.Decode(&data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodePairImage(captured)
	if err != nil || got != raw {
		t.Fatalf("QR decode failed: %v", err)
	}
	if _, err = parseRelayPair(got, time.Now()); err != nil {
		t.Fatal(err)
	}
}
