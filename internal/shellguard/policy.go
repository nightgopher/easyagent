package shellguard

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/tools"
)

// Policy is the bash tool's allowlist of programs. It judges the parsed
// command, so a pipe, an `&&` chain, a substitution or a function body is
// checked as a whole, and a name or path it cannot resolve is refused.
//
// The policy guards against mistakes and naive attempts; it is not a sandbox.
// It limits which programs run and where they write, not what they read.
type Policy struct {
	allow     map[string]bool
	denyArgs  []string
	npx       map[string]bool
	writes    tools.WritePolicy
	workspace string
}

// defaultAllow lists the programs a policy may run. A deployment adds or
// removes names with bash_policy.allow and bash_policy.deny.
var defaultAllow = map[string]bool{
	// inspect
	"basename": true, "cat": true, "cmp": true, "column": true, "cut": true, "date": true, "df": true,
	"diff": true, "dirname": true, "du": true, "echo": true, "file": true, "find": true,
	"grep": true, "head": true, "hexdump": true, "jq": true, "ls": true, "md5": true, "md5sum": true,
	"nl": true, "od": true, "printf": true, "pwd": true, "readlink": true, "realpath": true,
	"rg": true, "seq": true, "sha1sum": true, "sha256sum": true, "shasum": true, "sleep": true,
	"sort": true, "stat": true, "strings": true, "tail": true, "test": true, "[": true, "tree": true,
	"tr": true, "true": true, "false": true, "uniq": true, "wc": true, "which": true, "xxd": true, ":": true,
	// change files inside the writable paths
	"cp": true, "ln": true, "mkdir": true, "mv": true, "rm": true, "touch": true, "tee": true,
	// project tooling; each has its own rule below
	"bash": true, "sh": true, "curl": true, "git": true, "npm": true, "npx": true,
}

// hijackEnv names variables that could change which program a name runs or
// what an interpreter loads before it. npm_config_* is refused by prefix for
// the same reason: it can replace the shell npm runs scripts with.
var hijackEnv = map[string]bool{
	"PATH": true, "ENV": true, "BASH_ENV": true, "SHELL": true, "IFS": true, "CDPATH": true,
	"PROMPT_COMMAND": true, "PS4": true, "SHELLOPTS": true, "BASHOPTS": true,
	"LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "DYLD_INSERT_LIBRARIES": true, "DYLD_LIBRARY_PATH": true,
	"PAGER": true, "EDITOR": true, "VISUAL": true,
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_SSH": true,
	"GIT_SSH_COMMAND": true, "GIT_EXTERNAL_DIFF": true, "GIT_PAGER": true, "GIT_EDITOR": true,
	"GIT_CONFIG": true, "GIT_CONFIG_COUNT": true, "GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_SYSTEM": true,
	"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true,
	"NODE_OPTIONS": true, "NODE_PATH": true, "PYTHONPATH": true, "PERL5LIB": true, "PERL5OPT": true,
	"RUBYOPT": true, "RUBYLIB": true,
}

// gitReadOnly may only read the repository. Committing, tagging and pushing
// belong to the submit tool, which gates them.
var gitReadOnly = map[string]bool{
	"blame": true, "branch": true, "cat-file": true, "config": true, "count-objects": true,
	"describe": true, "diff": true, "grep": true, "log": true, "ls-files": true, "ls-remote": true,
	"ls-tree": true, "merge-base": true, "name-rev": true, "remote": true, "rev-list": true,
	"rev-parse": true, "shortlog": true, "show": true, "show-ref": true, "status": true, "version": true,
}

var (
	// gitListFlags only list; --list additionally accepts fixed patterns.
	gitListFlags = map[string]bool{"-a": true, "-r": true, "-v": true, "-vv": true, "-l": true, "--list": true, "--show-current": true}
	// gitUnsafeFlags reach outside the repository's data: a pager, an editor,
	// an external diff, or a transport or config override.
	gitUnsafeFlags = map[string]bool{
		"-O": true, "--open-files-in-pager": true, "--ext-diff": true, "--textconv": true,
		"-P": true, "--paginate": true, "--upload-pack": true, "--exec": true,
		"--config": true, "--config-env": true,
	}
	gitConfigRead   = map[string]bool{"--get": true, "--get-all": true, "--get-regexp": true, "--get-urlmatch": true, "--list": true, "-l": true}
	gitConfigWrite  = map[string]bool{"--add": true, "--unset": true, "--unset-all": true, "--replace-all": true, "--edit": true, "-e": true, "--rename-section": true, "--remove-section": true}
	gitOptionFlags  = map[string]bool{"--no-pager": true, "--no-optional-locks": true, "--literal-pathspecs": true}
	npmAllowed      = map[string]bool{"run": true, "run-script": true, "ci": true, "test": true, "ls": true, "--version": true, "-v": true}
	curlOutputFlags = map[string]bool{"--output": true, "--output-dir": true, "--dump-header": true}
	curlUnsafeShort = "dDFTKXOJ"
	// deviceSinks are output targets that write nowhere.
	deviceSinks = map[string]bool{"/dev/null": true, "/dev/stdout": true, "/dev/stderr": true}
)

var (
	errNotFixedName = errors.New("bash 策略拒绝：命令必须是固定的程序名，不能是变量或命令替换")
	errNotFixedPath = errors.New("bash 策略拒绝：要写入的路径必须写成固定值，不能是变量或命令替换")
)

// New builds a policy for the workspace. writes is the deployment's file
// policy: commands may only write where the file tools may, and may only run
// scripts that sit outside it, which the model cannot have authored.
func New(cfg config.BashPolicy, workspace string, writes tools.WritePolicy) *Policy {
	allow := make(map[string]bool, len(defaultAllow)+len(cfg.Allow))
	for name, ok := range defaultAllow {
		allow[name] = ok
	}
	for _, name := range cfg.Allow {
		allow[name] = true
	}
	for _, name := range cfg.Deny {
		allow[name] = false
	}
	npx := make(map[string]bool, len(cfg.NPX))
	for _, name := range cfg.NPX {
		npx[name] = true
	}
	return &Policy{allow: allow, denyArgs: cfg.DenyArgs, npx: npx, writes: writes, workspace: cleanWorkspace(workspace)}
}

// cleanWorkspace normalises the workspace, keeping an unset one empty so the
// policy knows it cannot judge absolute paths.
func cleanWorkspace(workspace string) string {
	if workspace == "" {
		return ""
	}
	return filepath.Clean(workspace)
}

// Check returns the reason a command is refused, or nil when it may run.
func (p *Policy) Check(command string) error {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "command")
	if err != nil {
		return fmt.Errorf("bash 策略拒绝：无法解析命令（%v）", err)
	}
	var refusal error
	syntax.Walk(file, func(node syntax.Node) bool {
		if refusal != nil {
			return false
		}
		switch n := node.(type) {
		case *syntax.CallExpr:
			refusal = p.checkCall(n)
		case *syntax.Redirect:
			refusal = p.checkRedirect(n)
		case *syntax.DeclClause:
			refusal = errors.New("bash 策略拒绝：export、declare 等变量声明不在允许范围；需要环境变量时写成命令前缀 VAR=value cmd")
		}
		return refusal == nil
	})
	return refusal
}

// word is an argument: its text, and whether the text is fixed.
type word struct {
	text  string
	fixed bool
}

func (p *Policy) checkCall(call *syntax.CallExpr) error {
	for _, assign := range call.Assigns {
		if assign.Name == nil {
			continue
		}
		if name := assign.Name.Value; hijackEnv[name] || strings.HasPrefix(name, "npm_config_") {
			return fmt.Errorf("bash 策略拒绝：不允许设置环境变量 %s", name)
		}
	}
	if len(call.Args) == 0 {
		return nil
	}
	name, fixed := wordLiteral(call.Args[0])
	if !fixed {
		return errNotFixedName
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("bash 策略拒绝：请直接用程序名调用，不要带路径：%q", name)
	}
	if !p.allow[name] {
		return fmt.Errorf("bash 策略拒绝：%q 不在允许执行的程序清单内", name)
	}
	words := call.Args[1:]
	for _, w := range words {
		literal, _ := wordLiteral(w)
		for _, denied := range p.denyArgs {
			if strings.Contains(literal, denied) {
				return fmt.Errorf("bash 策略拒绝：参数包含受限内容 %q", denied)
			}
		}
	}
	switch name {
	case "git":
		return checkGit(words)
	case "npm":
		return checkNPM(words)
	case "npx":
		return checkNPX(words, p.npx)
	case "bash", "sh":
		return p.checkScript(words)
	case "curl":
		return p.checkCurl(words)
	case "find":
		return checkFind(words)
	case "sed":
		return checkSed(words)
	case "sort", "uniq":
		return p.checkOutputFlag(words)
	case "rg":
		return checkRipgrep(words)
	case "cp", "ln":
		return p.checkOperands(words, false)
	case "mv", "rm", "touch", "mkdir", "tee":
		return p.checkOperands(words, true)
	}
	return nil
}

func (p *Policy) checkRedirect(r *syntax.Redirect) error {
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrInOut,
		syntax.RdrAll, syntax.AppAll, syntax.RdrAllClob, syntax.AppAllClob:
		target, fixed := wordLiteral(r.Word)
		if fixed && deviceSinks[target] {
			return nil
		}
		return p.checkWrite(target, fixed)
	}
	return nil
}

func checkGit(words []*syntax.Word) error {
	rest := literals(words)
	for len(rest) > 0 && rest[0].fixed && strings.HasPrefix(rest[0].text, "-") && rest[0].text != "-" {
		if rest[0].text == "--" {
			rest = rest[1:]
			break
		}
		if rest[0].text == "-C" {
			if len(rest) < 2 {
				return errors.New("bash 策略拒绝：无法判断 git 子命令")
			}
			rest = rest[2:]
			continue
		}
		if !gitOptionFlags[rest[0].text] {
			return fmt.Errorf("bash 策略拒绝：git 不允许全局选项 %q", rest[0].text)
		}
		rest = rest[1:]
	}
	if len(rest) == 0 || !rest[0].fixed {
		return errors.New("bash 策略拒绝：无法判断 git 子命令")
	}
	sub, rest := rest[0].text, rest[1:]
	if !gitReadOnly[sub] {
		return fmt.Errorf("bash 策略拒绝：此部署中 git 只读，%q 不在只读子命令内；提交、打标签和推送请使用提交工具", sub)
	}
	for _, arg := range rest {
		// Long options may carry their value after an =.
		name, _, _ := strings.Cut(arg.text, "=")
		if arg.fixed && gitUnsafeFlags[name] {
			return fmt.Errorf("bash 策略拒绝：git %s 不允许选项 %q", sub, name)
		}
	}
	switch sub {
	case "branch":
		listing := false
		for _, arg := range rest {
			switch {
			case arg.fixed && arg.text == "--list":
				listing = true
			case arg.fixed && gitListFlags[arg.text]:
			case listing && arg.fixed && !strings.HasPrefix(arg.text, "-"):
				// a pattern of the branches to list
			default:
				return errors.New("bash 策略拒绝：git branch 在此只用于列出分支（-a、-r、-v、--list）")
			}
		}
	case "remote":
		for _, arg := range rest {
			if !arg.fixed || (arg.text != "-v" && arg.text != "--verbose") {
				return errors.New("bash 策略拒绝：git remote 在此只用于查看（无参数或 -v）")
			}
		}
	case "config":
		if len(rest) == 0 || !rest[0].fixed || !gitConfigRead[rest[0].text] {
			return errors.New("bash 策略拒绝：git config 在此只用于读取（--get、--list）")
		}
		for _, arg := range rest {
			if arg.fixed && gitConfigWrite[arg.text] {
				return errors.New("bash 策略拒绝：git config 不允许写入")
			}
		}
	}
	return nil
}

// npm runs its scripts through a shell and appends the arguments after the
// script name to that command line, so they must stay single plain words.
var shellWord = regexp.MustCompile(`^[-@%+=:,./0-9A-Za-z_]+$`)

func checkNPM(words []*syntax.Word) error {
	args := literals(words)
	i := 0
	for ; i < len(args); i++ {
		if !args[i].fixed {
			return errors.New("bash 策略拒绝：无法判断 npm 子命令")
		}
		if !strings.HasPrefix(args[i].text, "-") {
			break
		}
	}
	if i >= len(args) {
		return nil
	}
	// Only scripts that package.json already defines may run.
	if !npmAllowed[args[i].text] {
		return fmt.Errorf("bash 策略拒绝：npm 只允许 run、ci 等已有脚本，不允许 %q", args[i].text)
	}
	if args[i].text != "run" && args[i].text != "run-script" {
		return nil
	}
	if i++; i >= len(args) {
		return nil
	}
	// The script name and everything after it join the shell command line.
	for _, arg := range args[i:] {
		if err := checkShellWord(arg); err != nil {
			return err
		}
	}
	return nil
}

func checkNPX(words []*syntax.Word, allowed map[string]bool) error {
	args := literals(words)
	for i, arg := range args {
		if !arg.fixed {
			return errors.New("bash 策略拒绝：无法判断 npx 要运行的包")
		}
		if strings.HasPrefix(arg.text, "-") {
			if arg.text == "-p" || arg.text == "--package" {
				return errors.New("bash 策略拒绝：npx 不允许 -p/--package")
			}
			continue
		}
		if !allowed[arg.text] {
			return fmt.Errorf("bash 策略拒绝：npx 只允许运行配置中列出的包，不允许 %q", arg.text)
		}
		for _, rest := range args[i+1:] {
			if err := checkShellWord(rest); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("bash 策略拒绝：无法判断 npx 要运行的包")
}

func checkShellWord(arg word) error {
	if !arg.fixed || !shellWord.MatchString(arg.text) {
		return errors.New("bash 策略拒绝：传给 npm 或 npx 的参数必须是单个普通单词，不能含空白或 shell 符号")
	}
	return nil
}

func checkRipgrep(words []*syntax.Word) error {
	for _, arg := range literals(words) {
		if arg.fixed && strings.HasPrefix(arg.text, "--pre") {
			return errors.New("bash 策略拒绝：rg 不允许 --pre 预处理命令")
		}
	}
	return nil
}

// checkScript allows `bash <script>` only for a fixed script inside the
// workspace that the model cannot write, and never for inline code.
func (p *Policy) checkScript(words []*syntax.Word) error {
	for _, w := range words {
		text, fixed := wordLiteral(w)
		if !fixed {
			return errNotFixedPath
		}
		if text == "--" {
			continue
		}
		if text == "-" {
			return errors.New("bash 策略拒绝：解释器不允许从标准输入读取脚本")
		}
		if strings.HasPrefix(text, "-") {
			if strings.HasPrefix(text, "--") {
				if text == "--rcfile" || text == "--init-file" {
					return errors.New("bash 策略拒绝：解释器不允许 --rcfile/--init-file")
				}
				continue
			}
			// A short option cluster carrying c takes the program from its
			// argument; s reads it from stdin.
			if strings.ContainsAny(text[1:], "cs") {
				return errors.New("bash 策略拒绝：解释器不允许 -c 内联代码或 -s 标准输入脚本")
			}
			continue
		}
		rel, err := p.relativePath(text)
		if err != nil {
			return fmt.Errorf("bash 策略拒绝：脚本 %q 必须位于工作区内", text)
		}
		// Only run what the model cannot have written itself.
		if p.writes.Configured() && p.writes.Check(rel) == nil {
			return fmt.Errorf("bash 策略拒绝：脚本 %s 位于模型可写的路径，不允许执行", rel)
		}
		return nil
	}
	return errors.New("bash 策略拒绝：解释器必须带工作区内的脚本文件")
}

func (p *Policy) checkCurl(words []*syntax.Word) error {
	args := literals(words)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !arg.fixed {
			return errors.New("bash 策略拒绝：curl 的参数必须写成固定值")
		}
		text := arg.text
		if !strings.HasPrefix(text, "-") {
			continue
		}
		if strings.HasPrefix(text, "--") {
			name, value, hasValue := strings.Cut(text, "=")
			switch {
			case strings.HasPrefix(name, "--data"), strings.HasPrefix(name, "--form"),
				name == "--upload-file", name == "--config", name == "--next", name == "--request",
				name == "--remote-name", name == "--remote-header-name":
				return errors.New("bash 策略拒绝：curl 在此只用于读取：不允许上传数据、指定请求方法或按远程文件名落盘")
			case curlOutputFlags[name]:
				if !hasValue {
					if i+1 >= len(args) {
						return fmt.Errorf("bash 策略拒绝：curl %s 缺少输出目标", name)
					}
					i++
					value = args[i].text
				}
				if err := p.checkSink(value); err != nil {
					return err
				}
			}
			continue
		}
		// Short options may be clustered; an output flag takes the next argument.
		if strings.ContainsAny(text[1:], curlUnsafeShort) {
			return errors.New("bash 策略拒绝：curl 在此只用于读取：不允许上传数据、指定请求方法或按远程文件名落盘")
		}
		if strings.HasSuffix(text, "o") {
			if i+1 >= len(args) {
				return errors.New("bash 策略拒绝：curl -o 缺少输出文件")
			}
			i++
			if err := p.checkSink(args[i].text); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkFind(words []*syntax.Word) error {
	for _, arg := range literals(words) {
		for _, prefix := range []string{"-exec", "-ok", "-delete", "-fprint", "-fls"} {
			if arg.fixed && strings.HasPrefix(arg.text, prefix) {
				return fmt.Errorf("bash 策略拒绝：find 不允许 %s", arg.text)
			}
		}
	}
	return nil
}

func checkSed(words []*syntax.Word) error {
	for _, arg := range literals(words) {
		if arg.fixed && (strings.HasPrefix(arg.text, "-i") || strings.HasPrefix(arg.text, "--in-place")) {
			return errors.New("bash 策略拒绝：sed 不允许原地修改文件，请使用 edit 工具")
		}
	}
	return nil
}

// checkOutputFlag covers sort and uniq, whose -o writes a file. GNU sort can
// also compress through an external program, which must not run.
func (p *Policy) checkOutputFlag(words []*syntax.Word) error {
	args := literals(words)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !arg.fixed {
			continue
		}
		if strings.HasPrefix(arg.text, "--compress-program") {
			return errors.New("bash 策略拒绝：sort 不允许 --compress-program")
		}
		if value, ok := strings.CutPrefix(arg.text, "--output="); ok {
			if err := p.checkSink(value); err != nil {
				return err
			}
			continue
		}
		if arg.text == "-o" || arg.text == "--output" {
			if i+1 >= len(args) {
				return fmt.Errorf("bash 策略拒绝：%s 缺少输出文件", arg.text)
			}
			i++
			if err := p.checkSink(args[i].text); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkOperands requires the paths a file command changes to be writable.
// With everyOperand set, all operands are changed (rm, mv, tee); otherwise
// only the destination is (cp, ln), which is the last operand or -t DIR.
func (p *Policy) checkOperands(words []*syntax.Word, everyOperand bool) error {
	var operands, target []word
	endOfOptions := false
	args := literals(words)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case endOfOptions:
			operands = append(operands, arg)
		case arg.fixed && arg.text == "--":
			endOfOptions = true
		case arg.fixed && strings.HasPrefix(arg.text, "-") && arg.text != "-":
			if arg.text == "-t" || arg.text == "--target-directory" {
				if i+1 >= len(args) {
					return errors.New("bash 策略拒绝：-t 缺少目标目录")
				}
				i++
				target = args[i : i+1]
			}
			if value, ok := strings.CutPrefix(arg.text, "--target-directory="); ok {
				target = []word{{text: value, fixed: true}}
			}
		default:
			operands = append(operands, arg)
		}
	}
	if everyOperand {
		target = operands
	} else if target == nil && len(operands) >= 2 {
		target = operands[len(operands)-1:]
	}
	for _, arg := range target {
		if err := p.checkWrite(arg.text, arg.fixed); err != nil {
			return err
		}
	}
	return nil
}

func (p *Policy) checkSink(target string) error {
	if deviceSinks[target] {
		return nil
	}
	return p.checkWrite(target, true)
}

func (p *Policy) checkWrite(target string, fixed bool) error {
	if !fixed {
		return errNotFixedPath
	}
	rel, err := p.relativePath(target)
	if err != nil {
		return fmt.Errorf("bash 策略拒绝：%q 指向工作区之外，不允许写入", target)
	}
	if err := p.writes.Check(rel); err != nil {
		return fmt.Errorf("bash 策略拒绝：%v", err)
	}
	return nil
}

// relativePath expresses a target as a clean slash-separated path inside the
// workspace, or fails when it leads outside. Without a workspace only
// relative paths that stay in place can be judged.
func (p *Policy) relativePath(target string) (string, error) {
	if strings.HasPrefix(target, "~") {
		return "", errors.New("outside the workspace")
	}
	if p.workspace == "" {
		if filepath.IsAbs(target) {
			return "", errors.New("outside the workspace")
		}
		return insideClean(target)
	}
	abs := target
	if !filepath.IsAbs(target) {
		abs = filepath.Join(p.workspace, target)
	}
	rel, err := filepath.Rel(p.workspace, filepath.Clean(abs))
	if err != nil {
		return "", errors.New("outside the workspace")
	}
	return insideClean(filepath.ToSlash(rel))
}

func insideClean(slashPath string) (string, error) {
	cleaned := path.Clean(slashPath)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("outside the workspace")
	}
	return cleaned, nil
}

func literals(words []*syntax.Word) []word {
	result := make([]word, 0, len(words))
	for _, w := range words {
		text, fixed := wordLiteral(w)
		result = append(result, word{text: text, fixed: fixed})
	}
	return result
}

// wordLiteral returns the text of a word made only of fixed parts, and
// whether it is made only of fixed parts at all.
func wordLiteral(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", false
	}
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				return "", false
			}
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}
