# 开发指南

EasyAgent 的主要入口是桌面、Android 客户端、Go 服务内嵌 Web、终端 TUI 和飞书桥接。先阅读 [AGENTS.md](../AGENTS.md) 与 [架构](ARCHITECTURE.md)，当前使用说明从 [文档索引](README.md) 查找。

## 环境与目录

Go 版本以 [go.mod](../go.mod) 为准（当前 1.26.10）。

| 目录 | 用途 |
|---|---|
| `cmd/` | 核心与飞书桥接入口 |
| `sdk/` | 可由其他 Go 模块导入的公共能力 |
| `internal/` | EasyAgent 应用、协议、TUI、Web 与领域实现 |
| `internal/web/static/` | 主 Web 界面的原生 HTML / CSS / JavaScript，嵌入 Go 程序 |
| `third_party/bubbletea/` | TUI 依赖的本地模块及测试 |
| `desktop-native/` | Go / MyGo 原生桌面客户端 |
| `mobile-native/` | Kotlin / Jetpack Compose Android 客户端（JDK 17、Android SDK 35） |
| `deploy/`、`scripts/` | 服务模板、部署与验证脚本 |

构建 Go 程序使用 `make build`，产出 `bin/easyagent` 和 `bin/easyagent-bridge`。`make install` 仅安装核心 CLI，不安装桥接。桌面打包见 [桌面客户端](DESKTOP.md)。

## 本地验证

从仓库根目录执行，与 [CI](../.github/workflows/verify.yml) 对齐：

```bash
(cd desktop-native && go test ./... && go vet ./...)
bash scripts/build-native.sh darwin-arm64 # Apple Silicon macOS
go test ./...
go vet ./...
(cd third_party/bubbletea && go test ./...)
node --test scripts/web-test.mjs scripts/mcp-settings-test.mjs
python3 scripts/test_update_mini.py
python3 scripts/test_desktop_registration.py
python3 -m unittest discover -s scripts -p "test_release*.py"
(cd mobile-native && ./gradlew --no-daemon :app:testDebugUnitTest :app:lintDebug :app:assembleDebug)
git diff --check
```

质量门禁在 PR、main 推送和每周定时检查中运行：`golangci-lint v2.15.0`（errcheck/staticcheck/govet/gosec/bodyclose）、SDK/服务端 race 检测、71% 加权语句覆盖率下限，以及核心与原生客户端的 `govulncheck v1.9.0`。`verify` 汇总上述检查，任意失败或跳过都会阻止通过现有必需检查。

本地可运行：

```bash
python3 -m unittest discover -s scripts -p "test_quality_gates.py"
GOOS=linux golangci-lint run --issues-exit-code=0 --output.json.path=/tmp/lint-report.json
python3 scripts/check_lint_baseline.py /tmp/lint-report.json
go test -race -count=1 -covermode=atomic -coverprofile=/tmp/coverage.out ./sdk/... ./internal/server/...
python3 scripts/check_coverage.py /tmp/coverage.out --minimum 71
govulncheck ./...
```

覆盖率初始基线为 71.4%（SDK/服务端范围），门槛只升不降：补测试使实际值稳定高出一个百分点以上时，在同一 PR 把门槛提到实际值向下取整。lint 存量清单见 [`.golangci-baseline.json`](../.golangci-baseline.json)：每条记录明确文件、规则、消息、原始行号及源码哈希（建立时 Linux 完整报告共 1,287 条；启用 `std-error-handling` 预设、关闭与 errcheck 重复的 gosec G104 并修复 QF1012 后降至 566 条；保留同一行的不同规则报告），只豁免这一份历史清单；相同行号移动不误报，新增相同问题的第二处实例仍会失败。它记录待整治的存量问题，不代表已完成安全审计。基线不能在 CI 自动刷新；扩大豁免必须单独解释并评审。基线只减不增：报告里已不存在的条目会让检查失败，修复存量问题的 PR 须用 Linux 报告（CI 的 `lint-report` 产物或上面的 `GOOS=linux` 命令）运行 `check_lint_baseline.py <报告> --prune` 删去对应条目，`--prune` 从不新增条目。历史格式问题单独处理，不与功能修复混合提交。

测试失败不自动重跑成绿；保留 race 日志、覆盖率和 lint 全量报告 7 天。遇到 flaky 测试先用失败日志定位和修复；暂时豁免必须指向 issue 并注明原因、负责人和恢复条件，不静默 skip。漏洞扫描无豁免，升级受影响依赖和 Go 补丁后重新验证。Dependabot 每周提交依赖与 Actions 更新 PR，不自动合并。

`make test` / `make vet` 只检查根 Go 模块。三个独立构建单元与本地 TUI 依赖需要各自验证，见 [架构](ARCHITECTURE.md#客户端与构建单元)。修改 Android 或发布脚本时，额外执行 `python3 scripts/build-android.py build <临时输出目录> --test-signing` 演练 Release APK 与签名校验；此模式使用一次性测试密钥，不生成可用于正式更新的安装包。

单元测试通过依赖注入使用 mock；CLI 联调使用本地模拟的 OpenAI / Anthropic HTTP 接口，不调用真实模型。CLI 不支持 `EA_PROVIDER=mock`。只针对文档的改动可先验证链接、配置名和示例；合并仍需通过仓库要求的 CI。涉及流式传输、取消、工具权限、会话持久化时，应添加能覆盖失败路径的回归测试。

`sdk/` 不得 import `internal/`；领域能力留在应用层，公共接口通过 `sdk/arch_test.go` 验证。Go 代码用 gofmt，禁止把真实密钥、配对码、运行数据或本机构建产物写入提交。

## 提交、合并与分支收尾

1. 在真实项目副本检查工作区和远端状态，保留已有改动。长期分支只有 `main`。
2. 当前任务可使用短期 `feat/`、`fix/` 或 `docs/` 分支。提交标题描述实际改变，例如 `docs: refresh configuration and deployment guides`。
3. PR 说明问题、最终行为、验证和必要限制；修改配置或 API 时同步更新文档。
4. CI 通过后合并。回到并同步 `main`，确认提交已包含在 main 中，再删除对应本地与远端任务分支，运行 `git fetch --prune origin`。
5. 收尾检查本地与远端分支。未合并提交、未提交改动或仍在使用的 worktree 必须保留并说明，不能强制删除来制造干净状态。

## 发布与数据

迷你主机更新器跟踪 `main`，使用确定 SHA 独立构建并验证核心和桥接；健康检查失败回滚。它不覆盖运行配置和会话目录。更新会重启服务，长任务期间需暂停更新 timer，详见 [部署运维](MINI_DEPLOY.md)。

飞书配对、HTTP 认证、路径开关和工具确认分别验证。涉及这些功能的改动必须保持默认限制与授权检查；不得用关闭认证或跳过 owner 校验来修复接入问题。

正式版本的 tag、CHANGELOG、构建与上传规范见 [版本与发布](RELEASING.md)，不使用历史一次性上传脚本或移动 tag 来重发版本。
