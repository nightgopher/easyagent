//go:build android

package main

import (
	"context"
	"errors"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	_ "github.com/nightgopher/easyagent/desktop-native/internal/mobileaudio"
)

type androidMicrophone struct {
	mu      sync.Mutex
	stopped bool
	done    chan struct{}
	level   atomic.Uint32
}

func startMicrophone() (voiceRecorder, error) {
	err := mygo.Native("easyagent_audio").Call(context.Background(), "start", nil, nil)
	if err != nil {
		var native *mygo.NativeError
		if errors.As(err, &native) && native.Code == "PermissionDenied" {
			return nil, errors.New("未获得麦克风权限，请在系统设置中允许录音")
		}
		return nil, errors.New("无法启动录音，请检查麦克风是否被其他应用占用")
	}
	m := &androidMicrophone{done: make(chan struct{})}
	go m.pollLevel()
	return m, nil
}

func (m *androidMicrophone) pollLevel() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			var result struct {
				Level float32 `json:"level"`
			}
			err := mygo.Native("easyagent_audio").Call(ctx, "level", nil, &result)
			cancel()
			if err == nil {
				m.level.Store(math.Float32bits(result.Level))
			}
		}
	}
}

func (m *androidMicrophone) Stop() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil, errors.New("录音已经结束")
	}
	m.stopped = true
	close(m.done)
	var result struct {
		Path string `json:"path"`
	}
	if err := mygo.Native("easyagent_audio").Call(context.Background(), "stop", nil, &result); err != nil {
		var native *mygo.NativeError
		if errors.As(err, &native) && native.Code == "TooLong" {
			return nil, errors.New("录音最长 10 分钟，请缩短后重试")
		}
		return nil, errors.New("录音失败，请重试")
	}
	if result.Path == "" {
		return nil, errors.New("未录到声音，请重试")
	}
	defer os.Remove(result.Path)
	audio, err := os.ReadFile(result.Path)
	if err != nil || !isM4AAudio(audio) {
		return nil, errors.New("未录到声音，请重试")
	}
	return audio, nil
}

func (m *androidMicrophone) Levels() [6]float32 {
	level := math.Float32frombits(m.level.Load())
	return [6]float32{level * .5, level * .8, level, level * .7, level * .9, level * .5}
}
