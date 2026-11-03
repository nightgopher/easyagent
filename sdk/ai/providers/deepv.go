package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nightgopher/easyagent/sdk/ai"
)

// DeepVTokenSource supplies the current user's credentials. The provider does
// not own storage and never accepts a shared server-side gateway key.
type DeepVTokenSource interface {
	AccessToken(context.Context) (string, error)
	RefreshAfterRejection(context.Context, string) (string, error)
}

type DeepVProvider struct{ inner *AnthropicProvider }

// NewDeepVProvider adapts the EasyCode GenAI stream to the existing event
// pipeline, including its cancellation, retry and idle timeout behavior.
func NewDeepVProvider(base string, tokens DeepVTokenSource) *DeepVProvider {
	inner := NewAnthropicProvider("easyagent-internal", base)
	inner.providerName = "deepv"
	httpClient := newStreamHTTPClient()
	inner.client = &http.Client{
		Transport:     &deepVTransport{base: strings.TrimRight(base, "/"), tokens: tokens, upstream: httpClient.Transport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &DeepVProvider{inner: inner}
}

func (*DeepVProvider) Name() string { return "deepv" }

func (p *DeepVProvider) StreamSimple(ctx context.Context, req ai.SimpleStreamRequest) (*ai.EventStream, error) {
	return p.Stream(ctx, ai.StreamRequest{Model: req.Model, Messages: req.Messages, System: req.System, Tools: req.Tools, MaxTokens: req.MaxTokens, Temperature: req.Temperature})
}

func (p *DeepVProvider) Stream(ctx context.Context, req ai.StreamRequest) (*ai.EventStream, error) {
	if req.MaxTokens != nil && *req.MaxTokens > 32000 {
		n := 32000
		req.MaxTokens = &n
	}
	// Some providers only persist the aggregate reasoning string. Preserve it
	// when switching to EasyCode without changing the stored conversation.
	messages := append([]ai.Message(nil), req.Messages...)
	for i, raw := range messages {
		if msg, ok := raw.(ai.AssistantMessage); ok && len(msg.ThinkingBlocks) == 0 && msg.Thinking != "" {
			msg.ThinkingBlocks = []ai.ThinkingBlock{{Type: "thinking", Thinking: msg.Thinking}}
			messages[i] = msg
		}
	}
	req.Messages = messages
	return p.inner.Stream(ctx, req)
}

type deepVTransport struct {
	base     string
	tokens   DeepVTokenSource
	upstream http.RoundTripper
}

func (t *deepVTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.URL.Path != "/v1/messages" {
		return nil, errors.New("EasyCode 模型请求无效")
	}
	if req.Body == nil {
		return nil, errors.New("EasyCode 模型请求无效")
	}
	defer req.Body.Close()
	var source anthropicRequest
	if err := json.NewDecoder(io.LimitReader(req.Body, 32<<20)).Decode(&source); err != nil {
		return nil, errors.New("无法读取模型请求")
	}
	wire, err := deepVFromAnthropic(source)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, errors.New("无法构造 EasyCode 模型请求")
	}
	if t.tokens == nil {
		return nil, errors.New("请先登录 EasyCode")
	}
	token, err := t.tokens.AccessToken(req.Context())
	if err != nil {
		return nil, err
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("EasyCode 登录凭据无效，请重新登录")
	}
	send := func(token string) (*http.Response, error) {
		out, err := http.NewRequestWithContext(req.Context(), http.MethodPost, t.base+"/v1/chat/stream", bytes.NewReader(body))
		if err != nil {
			return nil, errors.New("EasyCode 模型地址无效")
		}
		out.Header.Set("Authorization", "Bearer "+token)
		out.Header.Set("User-Agent", "EasyAgent")
		out.Header.Set("Content-Type", "application/json")
		out.Header.Set("Accept", "text/event-stream")
		return t.upstream.RoundTrip(out)
	}
	resp, err := send(token)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		newToken, refreshErr := t.tokens.RefreshAfterRejection(req.Context(), token)
		if refreshErr != nil {
			return nil, refreshErr
		}
		token = newToken
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return nil, errors.New("EasyCode 登录凭据无效，请重新登录")
		}
		resp, err = send(token)
		if err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		// Server error text is useful, but it must not disclose bearer tokens.
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		_ = resp.Body.Close()
		data = bytes.ReplaceAll(data, []byte(token), []byte("[redacted]"))
		resp.Body = io.NopCloser(bytes.NewReader(data))
		return resp, nil
	}
	reader, writer := io.Pipe()
	upstream := resp.Body
	resp.Body = reader
	go func() {
		defer upstream.Close()
		err := translateDeepVStream(upstream, writer)
		_ = writer.CloseWithError(err)
	}()
	return resp, nil
}

type deepVRequest struct {
	Model    string         `json:"model"`
	Contents []deepVContent `json:"contents"`
	System   *deepVContent  `json:"systemInstruction,omitempty"`
	Config   deepVConfig    `json:"config"`
}
type deepVContent struct {
	Role  string      `json:"role,omitempty"`
	Parts []deepVPart `json:"parts"`
}
type deepVPart struct {
	Text             string       `json:"text,omitempty"`
	Reasoning        string       `json:"reasoning,omitempty"`
	InlineData       *deepVImage  `json:"inlineData,omitempty"`
	FileData         *deepVFile   `json:"fileData,omitempty"`
	FunctionCall     *deepVCall   `json:"functionCall,omitempty"`
	FunctionResponse *deepVResult `json:"functionResponse,omitempty"`
}
type deepVImage struct {
	Mime string `json:"mimeType"`
	Data string `json:"data"`
}
type deepVFile struct {
	Mime string `json:"mimeType"`
	URI  string `json:"fileUri"`
}
type deepVCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}
type deepVResult struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}
type deepVConfig struct {
	MaxTokens   int         `json:"maxOutputTokens,omitempty"`
	Temperature *float64    `json:"temperature,omitempty"`
	Tools       []deepVTool `json:"tools,omitempty"`
}
type deepVTool struct {
	Declarations []deepVDeclaration `json:"functionDeclarations"`
}
type deepVDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

func deepVImagePart(source *anthropicImageSource) *deepVPart {
	if source == nil {
		return nil
	}
	if source.Type == "base64" && source.Data != "" {
		return &deepVPart{InlineData: &deepVImage{Mime: source.MediaType, Data: source.Data}}
	}
	if source.Type == "url" && source.URL != "" {
		media := source.MediaType
		if media == "" {
			lower := strings.ToLower(strings.SplitN(source.URL, "?", 2)[0])
			switch {
			case strings.HasSuffix(lower, ".png"):
				media = "image/png"
			case strings.HasSuffix(lower, ".webp"):
				media = "image/webp"
			case strings.HasSuffix(lower, ".gif"):
				media = "image/gif"
			default:
				media = "image/jpeg"
			}
		}
		return &deepVPart{FileData: &deepVFile{Mime: media, URI: source.URL}}
	}
	return nil
}

func deepVFromAnthropic(source anthropicRequest) (deepVRequest, error) {
	out := deepVRequest{Model: source.Model, Config: deepVConfig{MaxTokens: source.MaxTokens}}
	if source.Model != "claude-haiku-5-5" {
		out.Config.Temperature = source.Temperature
	}
	if out.Config.MaxTokens > 32000 {
		out.Config.MaxTokens = 32000
	}
	if source.System != "" {
		out.System = &deepVContent{Parts: []deepVPart{{Text: source.System}}}
	}
	names := map[string]string{}
	for _, message := range source.Messages {
		role := message.Role
		if role == "assistant" {
			role = "model"
		}
		row := deepVContent{Role: role}
		var reasoning []deepVPart
		for _, block := range message.Content {
			switch block.Type {
			case "thinking":
				if role == "model" && block.Thinking != "" {
					reasoning = append(reasoning, deepVPart{Reasoning: block.Thinking})
				}
			case "text":
				if block.Text != "" {
					row.Parts = append(row.Parts, deepVPart{Text: block.Text})
				}
			case "image":
				if image := deepVImagePart(block.Source); image != nil {
					row.Parts = append(row.Parts, *image)
				}
			case "tool_use":
				args, err := deepVArgs(block.Input)
				if err != nil {
					return out, fmt.Errorf("工具 %s 的参数无效", block.Name)
				}
				names[block.ID] = block.Name
				row.Parts = append(row.Parts, deepVPart{FunctionCall: &deepVCall{ID: block.ID, Name: block.Name, Args: args}})
			case "tool_result":
				name := names[block.ToolUseID]
				if name == "" {
					name = block.ToolUseID
				}
				var contents []anthropicContentBlock
				encoded, _ := json.Marshal(block.Content)
				_ = json.Unmarshal(encoded, &contents)
				var text strings.Builder
				var images []deepVPart
				for _, item := range contents {
					switch item.Type {
					case "text":
						text.WriteString(item.Text)
					case "image":
						if image := deepVImagePart(item.Source); image != nil {
							images = append(images, *image)
						}
					}
				}
				result := text.String()
				if block.IsError {
					result = "Error: " + result
				}
				row.Parts = append(row.Parts, deepVPart{FunctionResponse: &deepVResult{ID: block.ToolUseID, Name: name, Response: map[string]any{"result": result}}})
				row.Parts = append(row.Parts, images...)
			}
		}
		if len(row.Parts) == 0 {
			continue
		}
		if role == "model" && len(reasoning) == 0 {
			reasoning = append(reasoning, deepVPart{Reasoning: "."})
		}
		row.Parts = append(reasoning, row.Parts...)
		out.Contents = append(out.Contents, row)
	}
	if len(source.Tools) > 0 {
		tool := deepVTool{}
		for _, input := range source.Tools {
			var params any
			if len(input.InputSchema) > 0 && json.Unmarshal(input.InputSchema, &params) != nil {
				return out, errors.New("工具定义格式无效")
			}
			tool.Declarations = append(tool.Declarations, deepVDeclaration{Name: input.Name, Description: input.Description, Parameters: params})
		}
		out.Config.Tools = []deepVTool{tool}
	}
	return out, nil
}

func deepVArgs(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return map[string]any{}, nil
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, errors.New("tool arguments must be a JSON object")
	}
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}
