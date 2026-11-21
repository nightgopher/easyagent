//go:build !darwin && !android

package main

import "errors"

func startMicrophone() (voiceRecorder, error) {
	return nil, errors.New("当前系统的麦克风录音尚未接入")
}
