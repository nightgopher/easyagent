package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

type pendingVoice struct {
	Data  []byte
	Owner *session
	Host  string
}

type voiceTranscription struct {
	Text string
	Err  error
}

type voiceRecorder interface {
	Stop() ([]byte, error)
	Levels() [6]float32
}

func pcmWAV(pcm []byte) []byte {
	data := make([]byte, 44+len(pcm))
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+len(pcm)))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 16000)
	binary.LittleEndian.PutUint32(data[28:], 32000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(len(pcm)))
	copy(data[44:], pcm)
	return data
}
func (a *preview) pickAttachments() {
	origin := a.current()
	a.background(func(context.Context, *backendClient) (any, error) {
		return mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "选择附件", Multiple: true, Parent: a.Window})
	}, func(v any) {
		paths, _ := v.([]string)
		if len(paths) == 0 {
			return
		}
		if a.current() != origin {
			a.Live.Error = "会话已切换，请在目标会话重新添加附件"
			return
		}
		a.attachNativeFiles(origin, paths)
	})
}
func (a *preview) attachNativeFiles(s *session, paths []string) {
	if len(paths) == 0 || !a.Live.Connected || s.Archived || s.Sending || a.Live.Loading["queue-add/"+s.RemoteID] {
		return
	}
	if s.RemoteID == "" {
		workspace := s.Workspace
		if workspace == "" {
			workspace = a.Live.workspace()
		}
		a.createLiveSessionIn("", workspace, s.Application, func(created *session) { a.attachNativeFiles(created, paths) })
		return
	}
	limit := 8 - len(s.Attachments)
	if limit <= 0 {
		a.Live.Error = "最多添加 8 个附件"
		return
	}
	type uploadResult struct {
		items  []map[string]any
		errors []string
	}
	a.run("attachments/"+s.RemoteID, func(ctx context.Context, b *backendClient) (any, error) {
		result := uploadResult{}
		if len(paths) > limit {
			result.errors = append(result.errors, "最多添加 8 个附件")
		}
		for _, path := range paths[:min(limit, len(paths))] {
			att, err := b.upload(ctx, s.RemoteID, path)
			if err != nil {
				result.errors = append(result.errors, err.Error())
				continue
			}
			result.items = append(result.items, att)
		}
		return result, nil
	}, func(v any) {
		result := v.(uploadResult)
		s.Attachments = append(s.Attachments, result.items...)
		if len(result.errors) > 0 {
			a.Live.Error = strings.Join(result.errors, "\n")
		}
	})
}
func (a *preview) startVoice() {
	l := a.Live
	if l.Loading["voice"] {
		return
	}
	if l.Voice == nil {
		if l.PendingVoice != nil {
			return
		}
		if !l.Connected || a.current().Archived {
			return
		}
		owner, generation := a.current(), l.VoiceGeneration
		a.run("voice", func(context.Context, *backendClient) (any, error) { return startMicrophone() }, func(v any) {
			l.Loading["voice"] = false
			recorder, _ := v.(voiceRecorder)
			if generation != l.VoiceGeneration {
				if recorder != nil {
					go recorder.Stop()
				}
				return
			}
			l.Voice = recorder
			l.VoiceOwner = owner
			l.VoiceStarted = time.Now()
			l.Notice = "正在录音，点击停止按钮结束并转写。"
		})
		return
	}
	voice := l.Voice
	l.Voice = nil
	l.Notice = "正在处理录音…"
	owner, generation, host := l.VoiceOwner, l.VoiceGeneration, l.Config.ActiveHost
	l.VoiceOwner = nil
	a.run("voice", func(context.Context, *backendClient) (any, error) { return voice.Stop() }, func(v any) {
		if generation != l.VoiceGeneration || owner == nil {
			return
		}
		data, _ := v.([]byte)
		if len(data) == 0 {
			l.Notice = ""
			l.Error = "录音为空，请重试。"
			return
		}
		l.PendingVoice = &pendingVoice{Data: data, Owner: owner, Host: host}
		a.retryVoice()
	})
}

func (a *preview) retryVoice() {
	l := a.Live
	pending := l.PendingVoice
	if pending == nil || l.Loading["voice"] {
		return
	}
	if !l.Connected || l.Config.ActiveHost != pending.Host {
		l.Error = "请连接录音时使用的主机后重试转写。"
		return
	}
	owner := pending.Owner
	found := false
	for _, current := range a.Sessions {
		if current == owner || owner.RemoteID != "" && current.RemoteID == owner.RemoteID {
			owner = current
			found = true
			break
		}
	}
	if !found {
		l.Error = "原对话暂不可用，请返回录音时的主机和对话后重试。"
		return
	}
	l.Notice = "正在转写语音…"
	l.Error = ""
	a.run("voice", func(ctx context.Context, b *backendClient) (any, error) {
		result := voiceTranscription{}
		if b == nil {
			result.Err = errors.New("尚未连接运行主机")
			return result, nil
		}
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		part, err := w.CreateFormFile("file", "voice.wav")
		if err == nil {
			_, err = part.Write(pending.Data)
		}
		if err == nil {
			err = w.Close()
		}
		if err != nil {
			result.Err = err
			return result, nil
		}
		req, err := http.NewRequestWithContext(ctx, "POST", b.URL+"/asr/transcribe", &body)
		if err != nil {
			result.Err = err
			return result, nil
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		var value map[string]any
		result.Err = b.execute(req, &value)
		result.Text = str(value["text"])
		return result, nil
	}, func(v any) {
		if l.PendingVoice != pending {
			return
		}
		result := v.(voiceTranscription)
		l.Notice = ""
		if result.Err != nil {
			l.Error = "语音转写失败，录音已暂存，可重试。"
			return
		}
		if strings.TrimSpace(result.Text) == "" {
			l.Error = "没有识别到语音，录音已暂存，可重试。"
			return
		}
		l.PendingVoice = nil

		owner.Draft += result.Text
		l.Notice = "语音转写完成。"
		if a.current() == owner {
			a.ReturnToComposer = true
		}
	})
}

func (a *preview) discardPendingVoice() {
	if a.Live == nil || a.Live.Loading["voice"] {
		return
	}
	a.Live.PendingVoice = nil
	a.Live.Error = ""
	a.Live.Notice = "录音已放弃。"
}

func (a *preview) cancelVoice() {
	if a.Live == nil {
		return
	}
	l := a.Live
	l.VoiceGeneration++
	if l.Voice != nil {
		recorder := l.Voice
		l.Voice = nil
		go recorder.Stop()
	}
	l.VoiceOwner = nil
	if strings.HasPrefix(l.Notice, "正在录音") || strings.HasPrefix(l.Notice, "正在转写语音") {
		l.Notice = ""
	}
}
