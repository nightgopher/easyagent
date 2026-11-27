package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The guard profile restricts one deployment to a single operator role, for
// example a bot that maintains one website repository. Every section is
// optional and absent by default: a deployment that configures none of them
// keeps the existing tool permissions.

// SkillsConfig narrows the skills a session offers and keeps required ones in
// the system prompt.
type SkillsConfig struct {
	// Dirs replaces the default skill discovery with explicit roots.
	Dirs []string `yaml:"dirs,omitempty"`
	// Deny hides the named skills from the model.
	Deny []string `yaml:"deny,omitempty"`
	// AlwaysInline puts the full body of the named skills into every system prompt.
	AlwaysInline []string `yaml:"always_inline,omitempty"`
}

// FilePolicy limits the file mutation tools to workspace-relative globs.
// A pattern segment of `**` matches any number of segments; `*` and `?`
// match inside one segment.
type FilePolicy struct {
	WriteAllow []string `yaml:"write_allow,omitempty"`
	WriteDeny  []string `yaml:"write_deny,omitempty"`
}

// BashPolicy turns the bash tool into an allowlist of programs. The policy
// inspects the parsed shell command, so pipes, substitutions and `&&` chains
// are checked as a whole, and anything it cannot resolve is refused.
type BashPolicy struct {
	// Allow and Deny add or remove program names on the built-in allowlist.
	Allow []string `yaml:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty"`
	// DenyArgs lists substrings that must not appear in any argument.
	DenyArgs []string `yaml:"deny_args,omitempty"`
	// NPX lists the packages `npx` may run; any other package is refused.
	NPX []string `yaml:"npx,omitempty"`
}

// MergeRequestConfig creates a merge request after a successful push.
type MergeRequestConfig struct {
	APIURL      string `yaml:"api_url,omitempty"`   // GitLab API root, e.g. https://gitlab.example.com/api/v4
	Project     string `yaml:"project,omitempty"`   // numeric project id or URL-encoded path
	TokenEnv    string `yaml:"token_env,omitempty"` // environment variable holding the API token
	TitlePrefix string `yaml:"title_prefix,omitempty"`
}

// SubmitConfig registers the submit_task tool, the only path from a session
// to the remote: it runs the gate, commits the allowed paths to a task branch
// and pushes that branch.
type SubmitConfig struct {
	// Gate runs in order before anything is committed; the first failure stops the submit.
	Gate               []string            `yaml:"gate,omitempty"`
	Include            []string            `yaml:"include,omitempty"`
	BranchPrefixes     []string            `yaml:"branch_prefixes,omitempty"`
	BaseBranch         string              `yaml:"base_branch,omitempty"`
	Remote             string              `yaml:"remote,omitempty"`
	Push               *bool               `yaml:"push,omitempty"`
	GateTimeoutSeconds int                 `yaml:"gate_timeout_seconds,omitempty"`
	MR                 *MergeRequestConfig `yaml:"mr,omitempty"`
	PreviewURL         string              `yaml:"preview_url,omitempty"`
	NextSteps          string              `yaml:"next_steps,omitempty"`
}

// DefaultSubmitBranchPrefix is used when no branch prefix is configured.
const DefaultSubmitBranchPrefix = "bot/"

const defaultSubmitGateTimeout = 15 * time.Minute

// TaskBranchPrefixes returns the prefixes a task branch may use.
func (s *SubmitConfig) TaskBranchPrefixes() []string {
	if len(s.BranchPrefixes) == 0 {
		return []string{DefaultSubmitBranchPrefix}
	}
	return s.BranchPrefixes
}

// GateTimeout bounds each gate command.
func (s *SubmitConfig) GateTimeout() time.Duration {
	if s.GateTimeoutSeconds > 0 {
		return time.Duration(s.GateTimeoutSeconds) * time.Second
	}
	return defaultSubmitGateTimeout
}

// PushEnabled reports whether commits are pushed; pushing is the default.
func (s *SubmitConfig) PushEnabled() bool {
	return s.Push == nil || *s.Push
}

var strictLine = regexp.MustCompile(`(?m)^\s*["']?strict_config["']?:\s*["']?true["']?\s*(#.*)?$`)

// strictRequested reads only the strict_config switch, so a file that asks for
// strict loading is still recognised when the rest of it fails to parse.
func strictRequested(data []byte) bool {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var only struct {
			StrictConfig bool `yaml:"strict_config"`
		}
		err := decoder.Decode(&only)
		if err == io.EOF {
			return false
		}
		if err != nil {
			return strictLine.Match(data)
		}
		if only.StrictConfig {
			return true
		}
	}
}

// ValidateContextScope keeps instruction scoping opt-in and rejects typos.
func ValidateContextScope(scope string) error {
	switch scope {
	case "", "all", "project":
		return nil
	default:
		return fmt.Errorf("context_scope must be all or project")
	}
}

// StrictConfigFile reports whether the config file asks for strict loading.
// Entrypoints use it to turn a failed strict load into a startup error: a
// guard profile must not start with its restrictions silently dropped.
func StrictConfigFile(configPath string) bool {
	data, err := os.ReadFile(configPath) // #nosec G304 -- the path is supplied by the operator.
	return err == nil && strictRequested(data)
}

// resolveConfigPath anchors a relative value to the config file's directory.
func resolveConfigPath(baseDir, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(baseDir, value)
}

func cleanList(values []string, key string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make([]string, 0, len(values))
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s[%d] must not be empty", key, i)
		}
		result = append(result, value)
	}
	return result, nil
}

// validatePathPatterns checks workspace-relative globs: absolute paths and
// parent references would escape the tree the pattern is matched against.
func validatePathPatterns(patterns []string, key string) error {
	for i, pattern := range patterns {
		if strings.HasPrefix(pattern, "/") || strings.HasPrefix(pattern, "~") || filepath.IsAbs(pattern) {
			return fmt.Errorf("%s[%d] must be a workspace-relative path: %q", key, i, pattern)
		}
		if slices.Contains(strings.Split(pattern, "/"), "..") {
			return fmt.Errorf("%s[%d] must not leave the workspace: %q", key, i, pattern)
		}
	}
	return nil
}

func validateProgramNames(names []string, key string) error {
	for i, name := range names {
		if strings.ContainsAny(name, "/ \t") {
			return fmt.Errorf("%s[%d] must be a program name without a path or spaces: %q", key, i, name)
		}
	}
	return nil
}

func (c *Config) applyGuardProfile(baseDir, promptFile string, skills *SkillsConfig, filePolicy *FilePolicy, bashPolicy *BashPolicy, submit *SubmitConfig) error {
	if promptFile != "" {
		promptFile = resolveConfigPath(baseDir, promptFile)
		data, err := os.ReadFile(promptFile) // #nosec G304 -- operator-configured path next to the config file.
		if err != nil {
			return fmt.Errorf("prompt_file: %w", err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return fmt.Errorf("prompt_file %s is empty", promptFile)
		}
		c.PromptFile, c.PromptTemplate = promptFile, string(data)
	}
	if skills != nil {
		var err error
		if skills.Dirs, err = cleanList(skills.Dirs, "skills.dirs"); err != nil {
			return err
		}
		for i, dir := range skills.Dirs {
			skills.Dirs[i] = resolveConfigPath(baseDir, dir)
		}
		if skills.Deny, err = cleanList(skills.Deny, "skills.deny"); err != nil {
			return err
		}
		if skills.AlwaysInline, err = cleanList(skills.AlwaysInline, "skills.always_inline"); err != nil {
			return err
		}
		c.Skills = skills
	}
	if filePolicy != nil {
		var err error
		if filePolicy.WriteAllow, err = cleanList(filePolicy.WriteAllow, "file_policy.write_allow"); err != nil {
			return err
		}
		if filePolicy.WriteDeny, err = cleanList(filePolicy.WriteDeny, "file_policy.write_deny"); err != nil {
			return err
		}
		if err := validatePathPatterns(filePolicy.WriteAllow, "file_policy.write_allow"); err != nil {
			return err
		}
		if err := validatePathPatterns(filePolicy.WriteDeny, "file_policy.write_deny"); err != nil {
			return err
		}
		c.FilePolicy = filePolicy
	}
	if bashPolicy != nil {
		var err error
		if bashPolicy.Allow, err = cleanList(bashPolicy.Allow, "bash_policy.allow"); err != nil {
			return err
		}
		if err := validateProgramNames(bashPolicy.Allow, "bash_policy.allow"); err != nil {
			return err
		}
		if bashPolicy.Deny, err = cleanList(bashPolicy.Deny, "bash_policy.deny"); err != nil {
			return err
		}
		if err := validateProgramNames(bashPolicy.Deny, "bash_policy.deny"); err != nil {
			return err
		}
		if bashPolicy.NPX, err = cleanList(bashPolicy.NPX, "bash_policy.npx"); err != nil {
			return err
		}
		if err := validateProgramNames(bashPolicy.NPX, "bash_policy.npx"); err != nil {
			return err
		}
		if bashPolicy.DenyArgs, err = cleanList(bashPolicy.DenyArgs, "bash_policy.deny_args"); err != nil {
			return err
		}
		c.BashPolicy = bashPolicy
	}
	if submit != nil {
		var err error
		if submit.Gate, err = cleanList(submit.Gate, "submit.gate"); err != nil {
			return err
		}
		if len(submit.Gate) == 0 {
			return fmt.Errorf("submit.gate must list at least one command")
		}
		if submit.Include, err = cleanList(submit.Include, "submit.include"); err != nil {
			return err
		}
		if err := validatePathPatterns(submit.Include, "submit.include"); err != nil {
			return err
		}
		if submit.BranchPrefixes, err = cleanList(submit.BranchPrefixes, "submit.branch_prefixes"); err != nil {
			return err
		}
		for i, prefix := range submit.BranchPrefixes {
			if !strings.HasSuffix(prefix, "/") || strings.ContainsAny(prefix, " \t:*?[]\\~^") {
				return fmt.Errorf("submit.branch_prefixes[%d] must end with %q and contain no wildcard or whitespace: %q", i, "/", prefix)
			}
		}
		if submit.GateTimeoutSeconds < 0 {
			return fmt.Errorf("submit.gate_timeout_seconds must not be negative")
		}
		if mr := submit.MR; mr != nil && (mr.APIURL == "" || mr.Project == "" || mr.TokenEnv == "") {
			return fmt.Errorf("submit.mr needs api_url, project and token_env")
		}
		c.Submit = submit
	}
	return nil
}
