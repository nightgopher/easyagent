package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestPCMRecordingUsesSpeechWAVFormat(t *testing.T) {
	pcm := []byte{0, 0, 0x34, 0x12}
	wav := pcmWAV(pcm)
	if len(wav) != 44+len(pcm) || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[36:40]) != "data" {
		t.Fatal("invalid WAV envelope")
	}
	if binary.LittleEndian.Uint16(wav[20:22]) != 1 || binary.LittleEndian.Uint16(wav[22:24]) != 1 || binary.LittleEndian.Uint32(wav[24:28]) != 16000 || binary.LittleEndian.Uint16(wav[34:36]) != 16 {
		t.Fatal("recording is not mono 16 kHz PCM16")
	}
	if binary.LittleEndian.Uint32(wav[40:44]) != uint32(len(pcm)) || !bytes.Equal(wav[44:], pcm) {
		t.Fatal("recording samples were not preserved")
	}
}
