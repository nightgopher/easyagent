//go:build android

package main

import (
	"context"
	"os"

	"github.com/egoist/mygo"
	_ "github.com/nightgopher/easyagent/desktop-native/internal/mobiletts"
)

func platformSpeechAvailable() bool { return true }
func platformSystemSpeech(ctx context.Context, text string) error {
	return mygo.Native("easyagent_tts").Call(ctx, "system", map[string]string{"text": text}, nil)
}
func platformSpeechPlaying(ctx context.Context) (bool, error) {
	var v struct {
		Playing bool `json:"playing"`
	}
	err := mygo.Native("easyagent_tts").Call(ctx, "status", nil, &v)
	return v.Playing, err
}
func platformStopSpeech(ctx context.Context) error {
	return mygo.Native("easyagent_tts").Call(ctx, "stop", nil, nil)
}
func platformPlaySpeech(ctx context.Context, data []byte) error {
	var cache struct {
		Path string `json:"path"`
	}
	if err := mygo.Native("easyagent_tts").Call(ctx, "cache", nil, &cache); err != nil {
		return err
	}
	f, err := os.CreateTemp(cache.Path, "easyagent-tts-*.mp3")
	if err != nil {
		return err
	}
	name := f.Name()
	if _, err = f.Write(data); err != nil {
		f.Close()
		os.Remove(name)
		return err
	}
	if err = f.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err = mygo.Native("easyagent_tts").Call(ctx, "file", map[string]string{"path": name}, nil); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
