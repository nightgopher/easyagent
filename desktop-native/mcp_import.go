package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type mcpImportEntry struct {
	Name   string
	Config map[string]any
}

var mcpImportNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Importing only prepares data. Connections are created by the existing save API.
func parseMCPImport(text string) ([]mcpImportEntry, bool, error) {
	if len(text) > 1024*1024 {
		return nil, false, errors.New("配置不能超过 1 MiB")
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(text), &root); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			line := strings.Count(text[:min(len(text), int(syntax.Offset))], "\n") + 1
			return nil, false, fmt.Errorf("JSON 第 %d 行附近有语法错误，请检查引号、逗号和括号。", line)
		}
		return nil, false, errors.New("请粘贴完整的 JSON 对象")
	}
	if root == nil {
		return nil, false, errors.New("配置必须是 JSON 对象")
	}
	raw, wrapped := root["mcpServers"]
	if !wrapped {
		return []mcpImportEntry{{Config: root}}, false, nil
	}
	servers, ok := raw.(map[string]any)
	if !ok || len(servers) == 0 {
		return nil, true, errors.New("mcpServers 中至少需要一个服务对象")
	}
	entries := make([]mcpImportEntry, 0, len(servers))
	for name, raw := range servers {
		if !mcpImportNamePattern.MatchString(name) {
			return nil, true, errors.New("服务名称须为 1–64 位字母、数字、下划线或短横线")
		}
		config, ok := raw.(map[string]any)
		if !ok || config == nil {
			return nil, true, fmt.Errorf("服务 %s 的配置必须是 JSON 对象", name)
		}
		entries = append(entries, mcpImportEntry{Name: name, Config: config})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, true, nil
}

func (s *settingsState) selectedMCPImport() (mcpImportEntry, int, error) {
	entries, wrapped, err := parseMCPImport(s.JSON)
	if err != nil {
		return mcpImportEntry{}, 0, err
	}
	if !wrapped {
		entries[0].Name = strings.TrimSpace(s.MCPName)
		return entries[0], 1, nil
	}
	for _, entry := range entries {
		if entry.Name == s.MCPImportName {
			return entry, len(entries), nil
		}
	}
	s.MCPImportName = entries[0].Name
	return entries[0], len(entries), nil
}

func validateMCPImport(config map[string]any, partial bool) error {
	for _, key := range []string{"command", "url", "httpUrl", "type", "cwd", "description"} {
		if value, exists := config[key]; exists && value != nil {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s 必须是字符串", key)
			}
		}
	}
	for _, key := range []string{"args", "includeTools", "excludeTools"} {
		if raw := config[key]; raw != nil {
			values, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("%s 必须是字符串数组", key)
			}
			for _, value := range values {
				if _, ok := value.(string); !ok {
					return fmt.Errorf("%s 必须是字符串数组", key)
				}
			}
		}
	}
	for _, key := range []string{"env", "headers"} {
		if raw := config[key]; raw != nil {
			values, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s 必须是字符串键值对象", key)
			}
			for _, value := range values {
				if _, ok := value.(string); !ok {
					return fmt.Errorf("%s 的值必须是字符串", key)
				}
			}
		}
	}
	for _, key := range []string{"trust", "enabled"} {
		if value := config[key]; value != nil {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s 必须为 true 或 false", key)
			}
		}
	}
	if value := config["timeout"]; value != nil {
		n, ok := value.(float64)
		if !ok || n < 0 || n != float64(int64(n)) {
			return errors.New("timeout 必须是非负整数毫秒")
		}
	}
	command, endpoint, alternate, transport := strings.TrimSpace(str(config["command"])), str(config["url"]), str(config["httpUrl"]), str(config["type"])
	if transport != "" && transport != "stdio" && transport != "http" && transport != "streamable-http" && transport != "sse" {
		return errors.New("协议支持 stdio、http、streamable-http 或 sse")
	}
	if endpoint != "" && alternate != "" {
		return errors.New("url 和 httpUrl 只能填写一个")
	}
	if endpoint == "" {
		endpoint = alternate
	}
	if command != "" && (endpoint != "" || transport != "" && transport != "stdio") || endpoint != "" && transport == "stdio" {
		return errors.New("本机命令与 HTTP 服务配置不能混用")
	}
	if endpoint != "" && !validServiceURL(endpoint) {
		return errors.New("服务地址须为有效的 HTTP(S) URL")
	}
	if !partial && command == "" && endpoint == "" {
		return errors.New("配置需要启动命令 command 或服务地址 url/httpUrl")
	}
	return nil
}

func (s *settingsState) importMCPToForm() error {
	entry, _, err := s.selectedMCPImport()
	if err != nil {
		return err
	}
	if err = validateMCPImport(entry.Config, s.MCPEditName != ""); err != nil {
		return err
	}
	if s.MCPEditName != "" && entry.Name != s.MCPEditName {
		return errors.New("修改配置时请保留原服务名称；新服务请通过「添加服务」创建。")
	}
	config := entry.Config
	next := mcpForm{Workspace: s.Workspace, Scope: s.Scope, MCPName: entry.Name, JSON: s.JSON, MCPBase: config,
		Command: str(config["command"]), URL: str(config["url"]), Cwd: str(config["cwd"]), Description: str(config["description"]), Trust: boolean(config["trust"])}
	if next.URL == "" {
		next.URL = str(config["httpUrl"])
	}
	next.Transport = "stdio"
	if next.URL != "" || str(config["type"]) == "http" || str(config["type"]) == "streamable-http" {
		next.Transport = "http"
	}
	if str(config["type"]) == "sse" {
		next.Transport = "sse"
	}
	if s.MCPEditName != "" {
		if _, ok := config["description"]; !ok {
			next.Description = s.Description
		}
		if _, ok := config["trust"]; !ok {
			next.Trust = s.Trust
		}
		if next.Command == "" && next.URL == "" {
			next.Transport = s.Transport
		}
	}
	var args []string
	for _, raw := range list(config["args"]) {
		if strings.ContainsAny(str(raw), "\r\n") {
			return errors.New("命令参数包含换行，请在 JSON 中继续编辑以保留完整内容。")
		}
		args = append(args, str(raw))
	}
	next.Args = strings.Join(args, "\n")
	key := "env"
	if next.Transport != "stdio" {
		key = "headers"
	}
	pairs := object(config[key])
	var lines []string
	for _, k := range sortedKeys(pairs) {
		if strings.ContainsAny(k+str(pairs[k]), "\r\n") || strings.Contains(k, "=") || strings.TrimSpace(k) != k {
			return errors.New("变量或请求头无法按行显示，请在 JSON 中继续编辑以保留完整内容。")
		}
		lines = append(lines, k+"="+str(pairs[k]))
	}
	next.Variables = strings.Join(lines, "\n")
	if value, ok := config["timeout"].(float64); ok {
		next.Timeout = fmt.Sprintf("%.0f", value)
	}
	s.mcpForm = next
	return nil
}
