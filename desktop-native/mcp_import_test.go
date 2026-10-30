package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMCPImportRecognizesStandardFormats(t *testing.T) {
	for _, tc := range []struct {
		name, source, wantName string
	}{
		{"stdio", `{"mcpServers":{"filesystem":{"command":"npx","args":["-y","example"],"cwd":"/tmp/project","env":{"KEY":"value"}}}}`, "filesystem"},
		{"http", `{"mcpServers":{"docs":{"url":"https://example.com/mcp","headers":{"Authorization":"Bearer fixture"}}}}`, "docs"},
		{"httpUrl", `{"httpUrl":"https://example.com/mcp","type":"streamable-http"}`, "raw-service"},
		{"sse", `{"mcpServers":{"events":{"type":"sse","url":"https://example.com/sse"}}}`, "events"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSettings()
			s.MCPName, s.JSON, s.Advanced = "raw-service", tc.source, true
			entry, count, err := s.mcpSubmission()
			if err != nil || count != 1 || entry.Name != tc.wantName {
				t.Fatalf("import = %+v, %d, %v", entry, count, err)
			}
			if _, wrapped := entry.Config["mcpServers"]; wrapped {
				t.Fatal("the API must receive the selected service, not the wrapper")
			}
		})
	}
}

func TestMCPImportRejectsInvalidInput(t *testing.T) {
	for _, source := range []string{
		`[]`, `null`, `{`, `{"mcpServers":{}}`, `{"mcpServers":{"bad name":{"command":"tool"}}}`,
		`{"mcpServers":{"x":null}}`, `{"args":["only"]}`, `{"command":true}`, `{"command":"npx","args":[7]}`,
		`{"command":"npx","env":{"KEY":42}}`, `{"url":"https://example.com","timeout":-1}`,
		`{"url":"https://example.com","timeout":1.5}`, `{"url":"https://example.com","trust":"true"}`,
		`{"url":"file:///tmp/server"}`, `{"url":"https://example.com","command":"npx"}`,
		`{"url":"https://example.com","httpUrl":"https://other.example.com"}`,
	} {
		t.Run(source, func(t *testing.T) {
			s := newSettings()
			s.MCPName, s.JSON, s.Advanced = "fixture", source, true
			if _, _, err := s.mcpSubmission(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestMCPImportFormRoundTripPreservesOtherServicesAndExtraFields(t *testing.T) {
	s := newSettings()
	s.Advanced, s.MCPImportName = true, "first"
	s.JSON = `{"mcpServers":{"first":{"command":"npx","args":["-y","tool"],"cwd":"/project","env":{"B":"two","A":"one"},"includeTools":["read_*"],"timeout":2000},"second":{"url":"https://example.com/mcp","oauth":{"clientId":"public-client"}}}}`
	s.toggleAdvanced()
	if s.Advanced || s.Error != "" || s.MCPName != "first" || s.Cwd != "/project" || s.Variables != "A=one\nB=two" {
		t.Fatalf("JSON did not populate the form: %+v / %s", s.mcpForm, s.Error)
	}
	s.Args = "-y\nupdated-tool"
	s.toggleAdvanced()
	if !s.Advanced || s.Error != "" {
		t.Fatal("form could not return to JSON", s.Error)
	}
	entries, wrapped, err := parseMCPImport(s.JSON)
	if err != nil || !wrapped || len(entries) != 2 {
		t.Fatal("multi-service document was lost", s.JSON, err)
	}
	if entries[0].Config["cwd"] != "/project" || !reflect.DeepEqual(entries[0].Config["includeTools"], []any{"read_*"}) || object(entries[1].Config["oauth"])["clientId"] != "public-client" {
		t.Fatal("round trip dropped fields", s.JSON)
	}
	if !reflect.DeepEqual(entries[0].Config["args"], []any{"-y", "updated-tool"}) {
		t.Fatal("form changes were not reflected in JSON")
	}
	s.toggleAdvanced()
	s.MCPName = "second"
	before := s.JSON
	s.toggleAdvanced()
	if s.Error == "" || s.Advanced || s.JSON != before {
		t.Fatal("renaming a service overwrote another entry in the imported document")
	}
}

func TestMCPImportPreservesEmptyArgument(t *testing.T) {
	s := newSettings()
	s.MCPName, s.Advanced, s.JSON = "fixture", true, `{"command":"tool","args":[""]}`
	s.toggleAdvanced()
	config, err := s.mcpConfig()
	if err != nil || !reflect.DeepEqual(config["args"], []string{""}) {
		t.Fatal("form round trip dropped a meaningful empty argument", config, err)
	}
}

func TestMCPImportInvalidFormSwitchKeepsTheDraft(t *testing.T) {
	s := newSettings()
	s.MCPName, s.Command, s.Args = "existing", "original-command", "original-arg"
	s.Advanced, s.JSON = true, `{"command":"changed","args":[42]}`
	before := s.mcpForm
	s.toggleAdvanced()
	if s.Error == "" || !reflect.DeepEqual(before, s.mcpForm) {
		t.Fatal("failed conversion partially overwrote the form")
	}
}

func TestMCPEditPatchPreservesOmittedSecrets(t *testing.T) {
	s := newSettings()
	s.mcpForm = mcpForm{Scope: "个人", MCPName: "existing", Transport: "http", Description: "updated", Trust: true}
	s.MCPEditName = "existing"
	entry, _, err := s.mcpSubmission()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"headers", "env", "command", "url", "args", "oauth", "timeout"} {
		if _, exists := entry.Config[key]; exists {
			t.Fatalf("empty editor must preserve saved %s", key)
		}
	}
	s.URL = "https://updated.example.com/mcp"
	entry, _, err = s.mcpSubmission()
	if alias, included := entry.Config["httpUrl"]; err != nil || !included || alias != nil || entry.Config["url"] != s.URL {
		t.Fatal("editing a URL could leave a conflicting saved httpUrl", entry.Config, err)
	}
	s.URL = ""
	s.Advanced, s.JSON = true, `{"headers":null}`
	s.toggleAdvanced()
	s.toggleAdvanced()
	var config map[string]any
	if err := json.Unmarshal([]byte(s.JSON), &config); err != nil {
		t.Fatal(err)
	}
	if value, exists := config["headers"]; !exists || value != nil {
		t.Fatal("explicit credential clearing was lost", s.JSON)
	}
	if config["description"] != "updated" {
		t.Fatal("switching a partial patch to the form erased the saved description")
	}
	s.JSON = `{"mcpServers":{"different":{"url":"https://example.com"}}}`
	if _, _, err := s.mcpSubmission(); err == nil || !strings.Contains(err.Error(), "原服务名称") {
		t.Fatal("editing an existing service silently switched identity")
	}
}

func TestMCPImportKeepsMultilineValuesInJSON(t *testing.T) {
	for _, source := range []string{
		`{"command":"tool","args":["one\ntwo"]}`,
		`{"command":"tool","env":{"KEY":"line1\nline2"}}`,
	} {
		s := newSettings()
		s.MCPName, s.Advanced, s.JSON = "fixture", true, source
		before := s.mcpForm
		s.toggleAdvanced()
		if s.Error == "" || !reflect.DeepEqual(before, s.mcpForm) {
			t.Fatal("form conversion changed a multiline value", source)
		}
	}
}
