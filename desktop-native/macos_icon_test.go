package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image/png"
	"os"
	"testing"
)

// Older Launchpad versions display the ICNS canvas as-is, so every PNG
// representation must include the inset and transparent rounded corners.
func TestMacOSIconHasInsetAndRoundedCorners(t *testing.T) {
	var config struct {
		Icon  string `json:"icon"`
		MacOS struct {
			Icon string `json:"icon"`
		} `json:"macos"`
	}
	configBytes, err := os.ReadFile("mygo.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(configBytes, &config); err != nil {
		t.Fatal(err)
	}
	if config.Icon != "resources/icon.png" || config.MacOS.Icon != "resources/icon-macos.icns" {
		t.Fatalf("unexpected icon configuration: %+v", config)
	}
	data, err := os.ReadFile(config.MacOS.Icon)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 8 || string(data[:4]) != "icns" || int(binary.BigEndian.Uint32(data[4:8])) != len(data) {
		t.Fatal("invalid ICNS header")
	}
	want := map[string]int{"ic11": 32, "ic12": 64, "ic07": 128, "ic13": 256, "ic08": 256, "ic14": 512, "ic09": 512, "ic10": 1024}
	for offset := 8; offset < len(data); {
		if len(data)-offset < 8 {
			t.Fatal("truncated ICNS entry")
		}
		kind := string(data[offset : offset+4])
		size := int(binary.BigEndian.Uint32(data[offset+4 : offset+8]))
		if size < 8 || size > len(data)-offset {
			t.Fatalf("invalid %s entry length", kind)
		}
		if dimension, ok := want[kind]; ok {
			img, err := png.Decode(bytes.NewReader(data[offset+8 : offset+size]))
			if err != nil {
				t.Fatalf("decode %s: %v", kind, err)
			}
			if img.Bounds().Dx() != dimension || img.Bounds().Dy() != dimension {
				t.Fatalf("%s has wrong dimensions", kind)
			}
			alpha := func(x, y int) uint32 { _, _, _, a := img.At(x, y).RGBA(); return a }
			if alpha(dimension/20, dimension/2) != 0 || alpha(dimension/2, dimension/20) != 0 {
				t.Fatalf("%s is missing the transparent inset", kind)
			}
			if alpha(dimension*12/100, dimension*12/100) != 0 || alpha(dimension/2, dimension/2) == 0 {
				t.Fatalf("%s is missing rounded corners or artwork", kind)
			}
			delete(want, kind)
		}
		offset += size
	}
	if len(want) != 0 {
		t.Fatalf("missing ICNS representations: %v", want)
	}
}
