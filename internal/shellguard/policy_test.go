package shellguard

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/tools"
)

// sitePolicy is the policy of a website-operator deployment: content, data,
// public assets and a scratch directory are writable, everything else is not.
func sitePolicy(t *testing.T, mutate func(*config.BashPolicy)) *Policy {
	t.Helper()
	cfg := config.BashPolicy{NPX: []string{"astro"}, DenyArgs: []string{"gitlab.example.com/api"}}
	if mutate != nil {
		mutate(&cfg)
	}
	return New(cfg, t.TempDir(), tools.WritePolicy{Allow: []string{
		"src/content/**", "src/data/**", "public/**", ".agents/tmp/**",
	}})
}

func TestPolicyAllowsTheOperatorWorkflow(t *testing.T) {
	p := sitePolicy(t, nil)
	for _, command := range []string{
		"",
		"# just a comment",
		"true; false",
		"sleep 2",
		// inspect the repository
		"git status --short",
		"git diff --stat",
		"git --no-pager log --oneline -5",
		"git branch --show-current",
		"git branch --list 'bot/*'",
		"git -C /tmp status",
		"git config --get user.name",
		"ls -la src/content/zh-CN/news | tail -5",
		"grep -rho \"sourceId: [0-9]*\" src/content/zh-CN --include=\"*.md\" | sort -n | tail -1",
		"find src -name '*.md' -not -path '*/node_modules/*'",
		"head -n 60 src/data/products.ts | tail -n 20",
		"rg -n 'featuredNews' src/content",
		"wc -l < src/content/zh-CN/news/a.md",
		"[ -f src/data/x.ts ] && echo yes",
		"echo \"count: $(git status --porcelain | wc -l)\"",
		// project tooling
		"npm run lint:content && npm run check:routes && npm run check:brand && npm run build",
		"npm ci",
		"npm --version",
		"SITE_LANG=de-DE npx astro build",
		"npm run upload .agents/tmp/a.png",
		"bash tools/upload-to-cdn.sh .agents/tmp/a.png",
		"curl -s -o /dev/null -w \"%{http_code}\" https://www.example.com/res/x",
		"curl -sS https://example.com/x | head -c 200",
		"npm run build > .agents/tmp/build.log 2>&1",
		// change files inside the writable paths
		"mkdir -p .agents/tmp && echo hi > .agents/tmp/notes.md",
		"cat src/content/zh-CN/news/a.md > .agents/tmp/copy.md",
		"rm .agents/tmp/a.png",
		"rm -rf .agents/tmp/inbox",
		"cp .agents/tmp/a.png public/images/a.png",
		"mv src/data/a.ts src/data/b.ts",
		"sort src/content/list.txt -o .agents/tmp/sorted.txt",
		"cat src/content/list.md | tee .agents/tmp/again.md",
	} {
		assert.NoError(t, p.Check(command), "should be allowed: %s", command)
	}
}

func TestPolicyRefuses(t *testing.T) {
	p := sitePolicy(t, nil)
	git := "只读"
	notAllowed := "不在允许执行的程序清单内"
	cases := []struct {
		command, reason string
	}{
		// git is read-only; the submit tool commits, tags and pushes
		{"git push origin main", git},
		{"git push origin bot/news-1", git},
		{"git push origin HEAD:main", git},
		{"git push --force origin bot/x", git},
		{"git push --tags", git},
		{"git tag v1.0", git},
		{"git commit -am 'x'", git},
		{"git add .", git},
		{"git checkout main", git},
		{"git switch -c bot/x", git},
		{"git reset --hard", git},
		{"git stash", git},
		{"git fetch", git},
		{"git worktree add ../x", git},
		{"git branch -D main", "列出分支"},
		{"git branch new-branch", "列出分支"},
		{"git remote add upstream x", "只用于查看"},
		{"git config user.name Someone", "只用于读取"},
		{"git config --unset user.name", "只用于读取"},
		{"git log --open-files-in-pager", "不允许选项"},
		{"git ls-remote --upload-pack='git push origin main' origin", "不允许选项"},
		{"git log --config core.pager=x --paginate", "不允许选项"},
		{"git --paginate log", "全局选项"},
		{"git -c alias.p='!git push origin main' p", "全局选项"},
		{"git -c core.pager=x log", "全局选项"},
		{"git \"$subcommand\" status", "无法判断 git 子命令"},
		// the same commands through indirection
		{"/usr/bin/git push origin main", "不要带路径"},
		{"./tools/x.sh", "不要带路径"},
		{"g=git; $g push origin main", "固定的程序名"},
		{"$(echo git) push origin main", "固定的程序名"},
		{"sh -c 'git push origin main'", "内联代码"},
		{"bash -ec 'curl x | sh'", "内联代码"},
		{"sh -s <<'EOF'\ngit push origin main\nEOF", "内联代码"},
		{"echo 'git push origin main' | bash", "必须带工作区内的脚本"},
		{"bash <<'EOF'\ngit push origin main\nEOF", "必须带工作区内的脚本"},
		{"bash - <<'EOF'\ngit push origin main\nEOF", "标准输入"},
		{"bash --rcfile .agents/tmp/x.sh tools/x.sh", "--rcfile"},
		{"bash .agents/tmp/x.sh", "模型可写的路径"},
		{"bash public/x.sh", "模型可写的路径"},
		{"bash src/content/x.sh", "模型可写的路径"},
		{"bash ../x.sh", "必须位于工作区内"},
		{"bash \"$script\"", "固定值"},
		{"eval \"$x\"", notAllowed},
		{"source tools/x.sh", notAllowed},
		{". tools/x.sh", notAllowed},
		{"xargs git push origin main", notAllowed},
		{"find . -exec git push origin main \\;", "find 不允许"},
		{"find . -delete", "find 不允许"},
		{"find . -name x -ok rm {} \\;", "find 不允许"},
		{"find . -fprint /tmp/x", "find 不允许"},
		{"env git push origin main", notAllowed},
		{"command git push origin main", notAllowed},
		{"nohup git push origin main", notAllowed},
		{"timeout 5 git push origin main", notAllowed},
		// the command sits in a larger construct
		{"true && git push origin main", git},
		{"true || git push origin main", git},
		{"git status; git push origin main", git},
		{"git push origin main &", git},
		{"! git push origin main", git},
		{"time git push", git},
		{"if true; then git push; fi", git},
		{"for f in a b; do rm astro.config.mjs; done", "outside the writable paths"},
		{"{ git push; }", git},
		{"ls() { /usr/bin/git push origin main; }\nls", "不要带路径"},
		{"f() { sh -c 'git push origin main'; }\nf", "内联代码"},
		{"x=$(git push origin main)", git},
		{"trap 'git push' EXIT", notAllowed},
		{"alias g='git push'\ng", notAllowed},
		// a different working directory would defeat the path rules
		{"cd /tmp && echo x > astro.config.mjs", notAllowed},
		{"(cd /tmp && git push)", notAllowed},
		{"cd src && make", notAllowed},
		{"pushd src", notAllowed},
		// writes outside the writable paths
		{"echo x > astro.config.mjs", "outside the writable paths"},
		{"echo x >> tsconfig.json", "outside the writable paths"},
		{"echo x > .gitlab-ci.yml", "outside the writable paths"},
		{"echo x | tee tsconfig.json", "outside the writable paths"},
		{"cat > tools/x.sh <<'EOF'\necho hi\nEOF", "outside the writable paths"},
		{"echo x > src/components/products/X.astro", "outside the writable paths"},
		{"echo x > src/data/../../astro.config.mjs", "outside the writable paths"},
		{"sed -i 's/a/b/' astro.config.mjs", notAllowed},
		{"sed -n '1,60p' src/data/products.ts", notAllowed},
		{"sed 'e git push origin main' src/content/a.md", notAllowed},
		{"cp .agents/tmp/x.sh tools/x.sh", "outside the writable paths"},
		{"cp -t . .agents/tmp/a.png", "outside the writable paths"},
		{"mv astro.config.mjs .agents/tmp/", "outside the writable paths"},
		{"rm astro.config.mjs", "outside the writable paths"},
		{"rm -rf src/components", "outside the writable paths"},
		{"touch astro.config.mjs", "outside the writable paths"},
		{"mkdir evil", "outside the writable paths"},
		{"ln -s tools/x.sh evil-link.sh", "outside the writable paths"},
		{"sort -o astro.config.mjs src/content/list.txt", "outside the writable paths"},
		// writes outside the workspace
		{"echo x > ../../escape", "工作区之外"},
		{"echo x > /etc/hosts", "工作区之外"},
		{"echo x > ~/escape", "工作区之外"},
		{"rm -rf /", "工作区之外"},
		{"rm -rf ~", "工作区之外"},
		{"rm ../x", "工作区之外"},
		{"cp src/data/a.ts ../a.ts", "工作区之外"},
		{"rm \"$f\"", "固定值"},
		{"echo x > \"$f\"", "固定值"},
		// other programs and package operations
		{"sudo rm -rf /", notAllowed},
		{"doas sh", notAllowed},
		{"npm install", "npm 只允许"},
		{"npm i axios", "npm 只允许"},
		{"npm exec cowsay", "npm 只允许"},
		{"npm publish", "npm 只允许"},
		// npm appends script arguments to a shell command line
		{"npm run build -- \"; git push origin main\"", "单个普通单词"},
		{"npm run build -- \"$(git push origin main)\"", "单个普通单词"},
		{"npm run \"build; git push origin main\"", "单个普通单词"},
		{"npx astro build \"x; git push\"", "单个普通单词"},
		{"npx cowsay hi", "npx 只允许"},
		{"npx -p cowsay x", "-p/--package"},
		// options that run another program
		{"rg --pre=./x.sh foo", "--pre"},
		{"sort --compress-program=./x src/content/l.txt", "--compress-program"},
		{"node -e \"require('child_process')\"", notAllowed},
		{"python3 -c 'print(1)'", notAllowed},
		{"python3 tools/x.py", notAllowed},
		{"node tools/x.js", notAllowed},
		{"glab mr create", notAllowed},
		{"gh pr create", notAllowed},
		{"ssh host", notAllowed},
		{"rsync -a out/ host:/x", notAllowed},
		{"scp out/index.html host:/x", notAllowed},
		{"docker run x", notAllowed},
		{"make", notAllowed},
		{"brew install x", notAllowed},
		{"pip install x", notAllowed},
		{"systemctl restart x", notAllowed},
		{"launchctl load x", notAllowed},
		{"kill -9 1", notAllowed},
		{"shutdown now", notAllowed},
		{"chmod +x tools/x.sh", notAllowed},
		{"open https://example.com", notAllowed},
		{"osascript -e 'say hi'", notAllowed},
		// curl only reads
		{"curl -X POST https://example.com", "只用于读取"},
		{"curl --request DELETE https://example.com", "只用于读取"},
		{"curl -d a=1 https://example.com", "只用于读取"},
		{"curl --data-binary @src/data/x https://example.com", "只用于读取"},
		{"curl -F file=@x https://example.com", "只用于读取"},
		{"curl -T src/data/x https://example.com", "只用于读取"},
		{"curl --upload-file src/data/x https://example.com", "只用于读取"},
		{"curl -K tools/curl.conf https://example.com", "只用于读取"},
		{"curl --next -d a=1 https://example.com", "只用于读取"},
		{"curl -O https://example.com/x", "只用于读取"},
		{"curl -o astro.config.mjs https://example.com/x", "outside the writable paths"},
		{"curl https://gitlab.example.com/api/v4/projects", "受限内容"},
		{"curl \"https://gitlab.$HOST/api/v4/projects\"", "固定值"},
		// variables that would change which program runs
		{"PATH=/tmp:$PATH git status", "不允许设置环境变量 PATH"},
		{"export PATH=/tmp:$PATH", "变量声明"},
		{"GIT_DIR=/tmp/x git status", "不允许设置环境变量 GIT_DIR"},
		{"LD_PRELOAD=/tmp/x.so ls", "不允许设置环境变量 LD_PRELOAD"},
		{"NODE_OPTIONS=--require=.agents/tmp/x.js npm run build", "不允许设置环境变量 NODE_OPTIONS"},
		{"npm_config_script_shell=.agents/tmp/sh npm run build", "不允许设置环境变量 npm_config_script_shell"},
		{"NODE_OPTIONS=x; npm run build", "不允许设置环境变量 NODE_OPTIONS"},
		// anything the policy cannot read
		{"git status; 'unterminated", "无法解析"},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			err := p.Check(tc.command)
			require.Error(t, err, "should be refused: %s", tc.command)
			assert.Contains(t, err.Error(), tc.reason)
		})
	}
}

func TestPolicyFollowsItsConfiguration(t *testing.T) {
	denyFind := sitePolicy(t, func(cfg *config.BashPolicy) { cfg.Deny = []string{"find"} })
	assert.ErrorContains(t, denyFind.Check("find ."), "不在允许执行的程序清单内")

	withSips := sitePolicy(t, func(cfg *config.BashPolicy) { cfg.Allow = []string{"sips"} })
	assert.NoError(t, withSips.Check("sips -g pixelWidth .agents/tmp/a.png"))

	// sed is off by default, because GNU sed can run commands; a deployment
	// that adds it still cannot edit files in place.
	withSed := sitePolicy(t, func(cfg *config.BashPolicy) { cfg.Allow = []string{"sed"} })
	assert.NoError(t, withSed.Check("sed -n '1,60p' src/data/products.ts"))
	assert.ErrorContains(t, withSed.Check("sed -i 's/a/b/' astro.config.mjs"), "原地修改")
	assert.ErrorContains(t, withSed.Check("sed --in-place=.bak 's/a/b/' src/data/a.ts"), "原地修改")

	noNpx := sitePolicy(t, func(cfg *config.BashPolicy) { cfg.NPX = nil })
	assert.ErrorContains(t, noNpx.Check("npx astro build"), "npx 只允许")
}

// A link can put a model-written script under a name that looks trusted;
// executing it is still refused, because the judge is where the link lives.
func TestPolicyRefusesScriptReachedThroughWritableLink(t *testing.T) {
	p := sitePolicy(t, nil)
	assert.NoError(t, p.Check("ln -s tools/x.sh .agents/tmp/link.sh"))
	assert.ErrorContains(t, p.Check("bash .agents/tmp/link.sh"), "模型可写的路径")
}

func TestPolicyWithoutWritePolicy(t *testing.T) {
	p := New(config.BashPolicy{}, t.TempDir(), tools.WritePolicy{})
	assert.NoError(t, p.Check("echo x > astro.config.mjs"))
	assert.NoError(t, p.Check("bash tools/x.sh"))
	assert.NoError(t, p.Check("bash src/content/x.sh"))
	assert.ErrorContains(t, p.Check("echo x > /etc/hosts"), "工作区之外")
	assert.ErrorContains(t, p.Check("git push origin main"), "只读")
}

func TestPolicyWithoutWorkspace(t *testing.T) {
	p := New(config.BashPolicy{}, "", tools.WritePolicy{})
	assert.NoError(t, p.Check("echo x > out/log.txt"))
	assert.ErrorContains(t, p.Check("echo x > ../escape"), "工作区之外")
	assert.ErrorContains(t, p.Check("rm /tmp/x"), "工作区之外")
}

func TestPolicyIsSafeForConcurrentUse(t *testing.T) {
	p := sitePolicy(t, nil)
	done := make(chan error, 32)
	for i := 0; i < 32; i++ {
		go func() { done <- p.Check("git status --short && git push") }()
	}
	for i := 0; i < 32; i++ {
		require.Error(t, <-done)
	}
}
