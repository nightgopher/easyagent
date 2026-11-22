//go:build !android

package main

import (
	"context"
	"errors"
)

func platformSpeechAvailable() bool { return false }
func platformSystemSpeech(context.Context, string) error {
	return errors.New("当前设备不支持朗读")
}
func platformSpeechPlaying(context.Context) (bool, error) { return false, nil }
func platformStopSpeech(context.Context) error            { return nil }
func platformPlaySpeech(context.Context, []byte) error {
	return errors.New("当前设备不支持朗读")
}
