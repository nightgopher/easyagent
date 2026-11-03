package providers

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type deepVResponse struct {
	Error      json.RawMessage `json:"error"`
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text         string             `json:"text"`
				Reasoning    string             `json:"reasoning"`
				FunctionCall *deepVResponseCall `json:"functionCall"`
				InlineData   *deepVImage        `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	Usage *struct {
		Input  int  `json:"promptTokenCount"`
		Output int  `json:"candidatesTokenCount"`
		Cached *int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
}
type deepVResponseCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}
type deepVAccumulatedCall struct {
	id, name  string
	fragments strings.Builder
	args      map[string]any
}
type deepVCalls struct {
	calls []*deepVAccumulatedCall
	byID  map[string]*deepVAccumulatedCall
}

func (a *deepVCalls) add(part *deepVResponseCall) error {
	if a.byID == nil {
		a.byID = map[string]*deepVAccumulatedCall{}
	}
	call := a.byID[part.ID]
	raw := bytes.TrimSpace(part.Args)
	fragment := len(raw) > 0 && raw[0] == '"'
	if call == nil && len(a.calls) > 0 {
		last := a.calls[len(a.calls)-1]
		if (part.ID == "" && (part.Name == "" || (fragment && last.name == part.Name))) || (last.id == "" && (part.Name == "" || ((fragment || len(raw) == 0) && last.name == part.Name))) {
			call = last
			if part.ID != "" {
				call.id = part.ID
				a.byID[part.ID] = call
			}
		}
	}
	if call == nil {
		call = &deepVAccumulatedCall{id: part.ID, name: part.Name, args: map[string]any{}}
		a.calls = append(a.calls, call)
		if part.ID != "" {
			a.byID[part.ID] = call
		}
	}
	if part.Name != "" {
		if call.name != "" && call.name != part.Name {
			return errors.New("EasyCode 工具调用名称发生变化")
		}
		call.name = part.Name
	}
	if fragment {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return errors.New("EasyCode 工具参数片段无效")
		}
		if call.fragments.Len()+len(value) > 1<<20 {
			return errors.New("EasyCode 工具参数过长")
		}
		call.fragments.WriteString(value)
	} else {
		args, err := deepVArgs(raw)
		if err != nil {
			return errors.New("EasyCode 工具参数无效")
		}
		for k, v := range args {
			call.args[k] = v
		}
	}
	return nil
}

func (a *deepVCalls) complete() ([]deepVCall, error) {
	var prefix [12]byte
	if len(a.calls) > 0 {
		if _, err := rand.Read(prefix[:]); err != nil {
			return nil, errors.New("无法生成工具调用标识")
		}
	}
	reserved := map[string]bool{}
	for _, call := range a.calls {
		if call.id != "" {
			reserved[call.id] = true
		}
	}
	var result []deepVCall
	sequence := 0
	for _, call := range a.calls {
		if call.name == "" {
			return nil, errors.New("EasyCode 工具调用缺少名称")
		}
		args := map[string]any{}
		if call.fragments.Len() > 0 {
			var err error
			args, err = deepVArgs(json.RawMessage(call.fragments.String()))
			if err != nil {
				return nil, errors.New("EasyCode 工具参数不完整")
			}
		}
		for k, v := range call.args {
			args[k] = v
		}
		id := call.id
		if id == "" {
			for {
				sequence++
				id = fmt.Sprintf("call_easyagent_%s_%d", hex.EncodeToString(prefix[:]), sequence)
				if !reserved[id] {
					reserved[id] = true
					break
				}
			}
		}
		result = append(result, deepVCall{ID: id, Name: call.name, Args: args})
	}
	return result, nil
}

// The wrapper emits Anthropic-shaped events consumed by the shared stream
// parser; incomplete GenAI streams remain errors and never execute tools.
func translateDeepVStream(source io.Reader, dest io.Writer) error {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	w := bufio.NewWriter(dest)
	event := func(kind string, data map[string]any) error {
		data["type"] = kind
		encoded, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded); err != nil {
			return err
		}
		return w.Flush()
	}
	if err := event("message_start", map[string]any{"message": map[string]any{"usage": map[string]any{}}}); err != nil {
		return err
	}
	index := -1
	textIndex := -1
	thinkingIndex := -1
	open := func(kind string) (int, error) {
		index++
		return index, event("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": kind}})
	}
	closeBlock := func(i *int) error {
		if *i < 0 {
			return nil
		}
		err := event("content_block_stop", map[string]any{"index": *i})
		*i = -1
		return err
	}
	terminal := false
	stop := "end_turn"
	var calls deepVCalls
	var input, output int
	var cached *int
	usageKnown := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, ":") {
			if err := event("ping", map[string]any{}); err != nil {
				return err
			}
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			terminal = true
			break
		}
		var frame deepVResponse
		if json.Unmarshal([]byte(data), &frame) != nil {
			return errors.New("EasyCode 模型流格式无效")
		}
		if len(frame.Error) > 0 && string(frame.Error) != "null" {
			return errors.New("EasyCode 模型流返回错误")
		}
		if frame.Usage != nil {
			input = frame.Usage.Input
			output = frame.Usage.Output
			cached = frame.Usage.Cached
			usageKnown = true
		}
		for _, candidate := range frame.Candidates {
			if candidate.FinishReason != "" {
				terminal = true
				switch strings.ToUpper(candidate.FinishReason) {
				case "TOOL_CALL", "FUNCTION_CALL":
					stop = "tool_use"
				case "MAX_TOKENS", "LENGTH":
					stop = "max_tokens"
				case "SAFETY", "BLOCKED", "PROHIBITED_CONTENT":
					return errors.New("EasyCode 模型请求被服务端策略拦截")
				}
			}
			for _, part := range candidate.Content.Parts {
				if part.Reasoning != "" {
					if err := closeBlock(&textIndex); err != nil {
						return err
					}
					if thinkingIndex < 0 {
						var err error
						thinkingIndex, err = open("thinking")
						if err != nil {
							return err
						}
					}
					if err := event("content_block_delta", map[string]any{"index": thinkingIndex, "delta": map[string]any{"type": "thinking_delta", "thinking": part.Reasoning}}); err != nil {
						return err
					}
				}
				if part.Text != "" {
					if err := closeBlock(&thinkingIndex); err != nil {
						return err
					}
					if textIndex < 0 {
						var err error
						textIndex, err = open("text")
						if err != nil {
							return err
						}
					}
					if err := event("content_block_delta", map[string]any{"index": textIndex, "delta": map[string]any{"type": "text_delta", "text": part.Text}}); err != nil {
						return err
					}
				}
				if part.FunctionCall != nil {
					if err := closeBlock(&textIndex); err != nil {
						return err
					}
					if err := closeBlock(&thinkingIndex); err != nil {
						return err
					}
					if err := calls.add(part.FunctionCall); err != nil {
						return err
					}
				}
				if part.InlineData != nil {
					return errors.New("当前客户端暂不支持模型直接生成的图片")
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !terminal {
		return errors.New("EasyCode 模型流提前中断")
	}
	completed, err := calls.complete()
	if err != nil {
		return err
	}
	if err = closeBlock(&textIndex); err != nil {
		return err
	}
	if err = closeBlock(&thinkingIndex); err != nil {
		return err
	}
	for _, call := range completed {
		index++
		if err = event("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name}}); err != nil {
			return err
		}
		args, err := json.Marshal(call.Args)
		if err != nil {
			return err
		}
		if err = event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}}); err != nil {
			return err
		}
		if err = event("content_block_stop", map[string]any{"index": index}); err != nil {
			return err
		}
	}
	if len(completed) > 0 {
		stop = "tool_use"
	}
	usage := map[string]any{"output_tokens": output}
	if usageKnown {
		usage["input_tokens"] = input
		if cached != nil {
			usage["cache_read_input_tokens"] = *cached
		}
	}
	if err = event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop}, "usage": usage}); err != nil {
		return err
	}
	return event("message_stop", map[string]any{})
}
