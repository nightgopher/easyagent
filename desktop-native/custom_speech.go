package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const speechCredential = "mobile-tts-api-key"
const siliconSpeechCredential = "mobile-siliconflow-tts-api-key"
const siliconSpeechEndpoint = "https://api.siliconflow.cn/v1/audio/speech"
const siliconSpeechDefaultModel = "FunAudioLLM/CosyVoice2-0.5B"
const siliconSpeechDefaultVoice = "FunAudioLLM/CosyVoice2-0.5B:alex"

func customSpeech(ctx context.Context, endpoint, model, voice, text, key string) ([]byte, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("请输入有效的语音接口地址")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !(u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) {
			return nil, errors.New("语音接口须使用 HTTPS")
		}
	}
	if strings.TrimSpace(model) == "" || strings.TrimSpace(voice) == "" {
		return nil, errors.New("请填写语音模型和音色")
	}
	if text == "" || len(text) > 30000 {
		return nil, errors.New("朗读内容为空或过长")
	}
	payload, err := json.Marshal(map[string]string{"model": model, "voice": voice, "input": text, "response_format": "mp3"})
	if err != nil {
		return nil, errors.New("无法准备语音请求")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("无法准备语音请求")
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("无法连接语音接口，请检查网络和地址")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("语音接口请求失败（HTTP %d）", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (12<<20)+1))
	if err != nil {
		return nil, errors.New("语音接收失败")
	}
	if len(data) == 0 || len(data) > 12<<20 {
		return nil, errors.New("语音数据为空或过大")
	}
	typ := strings.ToLower(res.Header.Get("Content-Type"))
	if strings.Contains(typ, "json") || strings.Contains(typ, "text/html") {
		return nil, errors.New("语音接口没有返回音频")
	}
	return data, nil
}
