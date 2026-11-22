package main

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

var speechLinks = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
var speechMarkup = regexp.MustCompile("(?m)^\\s{0,3}(?:#{1,6}|[-*+]>?)\\s+|[`*_~]")

func speechText(text string) string {
	text = speechLinks.ReplaceAllString(text, "$1")
	text = speechMarkup.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}
func (a *preview) stopSpeech() {
	if a.Live == nil {
		return
	}
	l := a.Live
	l.SpeechGeneration++
	if l.SpeechCancel != nil {
		l.SpeechCancel()
		l.SpeechCancel = nil
	}
	l.SpeechPlaying = false
	if l.Notice == "正在准备语音…" {
		l.Notice = ""
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = platformStopSpeech(ctx)
	}()
}
func (a *preview) playSpeech(text string) {
	if a.Live == nil || !platformSpeechAvailable() {
		return
	}
	l := a.Live
	if l.SpeechPlaying || l.Loading["speech"] {
		a.stopSpeech()
		return
	}
	text = speechText(text)
	if text == "" {
		l.Error = "没有可朗读的文字"
		return
	}
	l.SpeechGeneration++
	generation := l.SpeechGeneration
	l.Error = ""
	l.Notice = "正在准备语音…"
	ctx, cancel := context.WithTimeout(l.ctx, 60*time.Second)
	l.SpeechCancel = cancel
	provider, voice := a.Settings.Preferences.SpeechProvider, a.Settings.Preferences.SpeechVoice
	if voice == "" {
		voice = "zh-CN-XiaoxiaoNeural"
	}
	a.run("speech", func(context.Context, *backendClient) (any, error) {
		defer cancel()
		if provider == "system" {
			return platformSystemSpeech(ctx, text), nil
		}
		audio, err := edgeSpeech(ctx, text, voice)
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil {
			err = platformPlaySpeech(ctx, audio)
		}
		return err, nil
	}, func(v any) {
		if l.SpeechGeneration != generation {
			return
		}
		l.Notice = ""
		l.SpeechCancel = nil
		if err, ok := v.(error); ok && err != nil {
			l.Error = "朗读失败：" + err.Error()
			return
		}
		l.SpeechPlaying = true
		l.SpeechNext = time.Now().Add(2 * time.Second)
	})
}
func (a *preview) speechButton(c *ui.Context, text, label string) {
	if !platformSpeechAvailable() || a.Live == nil {
		return
	}
	iconName := "play"
	if a.Live.SpeechPlaying || a.Live.Loading["speech"] {
		iconName = "stop"
		label = "停止朗读"
	}
	iconButton(c, iconName, label).Disabled(strings.TrimSpace(text) == "" && !a.Live.SpeechPlaying && !a.Live.Loading["speech"]).OnClick(func() { a.playSpeech(text) })
}
func (a *preview) speechSettings(c *ui.Context) {
	a.heading(c, "语音朗读")
	if !platformSpeechAvailable() {
		a.note(c, "当前设备暂不支持朗读。")
		return
	}
	s := &a.Settings
	provider := s.Preferences.SpeechProvider
	choices := []string{"Edge 在线", "系统语音"}
	selected := choices[0]
	if provider == "system" {
		selected = choices[1]
	}
	if a.selectControl(c, "朗读服务", &selected, choices).Width(260).Changed() {
		if selected == choices[1] {
			s.Preferences.SpeechProvider = "system"
		} else {
			s.Preferences.SpeechProvider = "edge"
		}
		s.savePreferences()
	}
	if s.Preferences.SpeechProvider == "system" {
		a.note(c, "使用手机系统语音引擎；声音与语言由手机系统设置管理。")
	} else {
		a.note(c, "使用 Edge 在线自然语音，朗读时文字会发送到微软；需要网络连接。")
		if s.Preferences.SpeechVoice == "" {
			s.Preferences.SpeechVoice = "zh-CN-XiaoxiaoNeural"
		}
		voices := []struct{ ID, Name string }{{"zh-CN-XiaoxiaoNeural", "小晓 · 女声"}, {"zh-CN-XiaoyiNeural", "晓伊 · 女声"}, {"zh-CN-YunxiNeural", "云希 · 男声"}, {"zh-CN-YunyangNeural", "云扬 · 男声"}}
		labels := make([]string, 0, len(voices)+1)
		selected := s.Preferences.SpeechVoice
		for _, v := range voices {
			labels = append(labels, v.Name)
			if v.ID == s.Preferences.SpeechVoice {
				selected = v.Name
			}
		}
		if selected == s.Preferences.SpeechVoice {
			labels = append(labels, selected)
		}
		if a.selectControl(c, "音色", &selected, labels).Width(260).Changed() {
			for _, v := range voices {
				if v.Name == selected {
					s.Preferences.SpeechVoice = v.ID
					break
				}
			}
			s.savePreferences()
		}
		a.input(c, "Edge 音色 ID", &s.Preferences.SpeechVoice, "例如 zh-CN-XiaoxiaoNeural", false)
		a.action(c, "保存音色 ID").OnClick(func() {
			if !edgeVoiceID.MatchString(s.Preferences.SpeechVoice) {
				s.Error = "音色 ID 仅能包含字母、数字和连字符"
				return
			}
			s.savePreferences()
		})
	}
	if a.Live != nil && strings.HasPrefix(a.Live.Error, "朗读失败：") {
		ui.Text(c, a.Live.Error).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
	}
	if a.Live != nil && (a.Live.SpeechPlaying || a.Live.Loading["speech"]) {
		a.action(c, "停止朗读").OnClick(a.stopSpeech)
	} else {
		a.action(c, "试听声音").OnClick(func() { a.playSpeech("你好，我是 EasyAgent。今天想一起完成什么？") })
	}
}
