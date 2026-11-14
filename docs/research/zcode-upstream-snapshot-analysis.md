> 历史参考：本文仅反映记录时的外部项目或接口版本，不代表 EasyAgent 当前实现。旧路径与建议仅供追溯；当前使用方式见 [文档索引](../README.md)。

# ZCode (zai-org) 调研报告 — 开源以来更新快照与可借鉴设计

> 调研日期：2026-11-14
> 来源：https://github.com/zai-org/ZCode （Apache-2.0，2026-09-20 开源）
> 调研基线：main @ v3.18.2 快照（commit `fec97590`，2026-11-13）
> 调研目标：梳理智谱开源的 AI 编程工作台（桌面端 + Web + 终端 Agent/CLI，GLM-5.3 官方 harness）开源以来的更新方向，精选值得 EasyAgent 借鉴的设计，仅作说明、不涉及实现

---

## 1. 概述

### 项目定位

| 项目 | 角色 | 技术栈 | 定位 |
|------|------|--------|------|
| **ZCode (zai-org)** | AI 编程工作台 | TypeScript pnpm monorepo（Node 24 + Electron 桌面壳） | 桌面应用 + 浏览器界面 + 终端 Agent 三形态，`zcode` 单命令三态（无参 TUI / `--web` / Agent CLI），8.7k stars |
| **EasyAgent** | 通用 Agent 框架 | Go | 可扩展 Agent 底座 + 可插拔应用层，当前 coding-agent + 原生桌面 + 移动端 |

### 仓库形态：sync-bot 快照

main 分支 55 天只有 **11 个 commit**——官方内部开发，每个版本把整份源码 squash 成一个 `chore: sync ZCode source vX.Y.Z` 推上来。没有细粒度提交历史，活跃度要靠版本间 diff 和 docs/ 变化判断。

| 日期 | 版本快照 | 备注 |
|---|---|---|
| 2026-09-20 | v3.14.x 基线 | "feat: open source" |
| 2026-09-24 | v3.14.3 | 唯一带功能文案的 GitHub Release（工作流并发热调整等） |
| 2026-10-10 | v3.15.1 | **内置插件套件一次新增 13 个包** + UI Plugin / 内联 Gen UI |
| 2026-10-19 | v3.16.x | 平台化扩展 |
| 2026-10-30 / 11-04 | v3.17.1 / v3.17.2 | 会话协议 v4 相关大改（close 语义重写） |
| 2026-11-11~13 | v3.18.0 / .1 / .2 | 新增 `android` / `plugin-api` / `session-control` / `design-runtime` 四个顶层包，发版节奏加快 |

无 CHANGELOG.md；官方用「README 更新节 + docs/ 每特性一篇设计文档」当 changelog。docs/ 约 300 篇，`apps/zcode-cli/docs/design/v2/` 是 CLI Agent 的完整架构文档树（137 篇，含带日期的验证记录），文档密度是本项目调研的主要依据。

### 核心发现摘要

1. **从 Agent 到平台**：v3.15.1 一次新增 13 个内置插件（documents/pdf/presentations/spreadsheets/browser-use/computer-use 等），配合 `zai-org/zcode-plugins` 官方市场，形成「agent + 官方插件生态」形态。
2. **UI Plugin + MCP Apps**（MCP 1.29 / SDK 2.0）：Agent 可在桌面沙箱内渲染交互式 HTML surface（内置 Excalidraw 白板为标杆），工具结果从纯文本升级为可交互 UI。
3. **公开兼容 Claude Code 插件生态**：marketplace/manifest/hooks/MCP 命名空间全套实现并有真实插件验证记录。
4. **文档即规格的文化**：每个特性一篇设计文档，含非目标、契约、超时/取消语义、最低测试集、带日期的验证记录。
5. **安全文档密度极高**：permission broker、CUA 权限代理（8 篇）、危险命令审批、凭证脱敏、hook 信任收口，全部有独立规格。
6. **多端覆盖**：桌面 / Android 手机控制面（QR 配对）/ WSL / SSH / 飞书 Bot，消费同一份 V4 会话投影。

---

## 2. 精选借鉴点（8 项）

> 筛选标准：与 EasyAgent 当前架构（Go、JSONL 会话、Provider 注册制、飞书桥、刚落地的 CLI 自升级）相关性高，且描述足够具体可评估。每项给出建议优先级：**[可立即抄]** = 语言无关、一次小改动可落地；**[中期]** = 需要设计节奏；**[观望]** = 方向参考。

### 2.1 会话持久化：三表模型 + compaction-as-part + 崩溃恢复语义

**是什么**：ZCode 的会话存储把 `session / message / part` 分成三张表（SQLite），业务 shape 放 JSON `data`，只有可索引字段做列。**参考 `apps/zcode-cli/docs/design/v2/session-persistence.md`**。

- Part 是统一抽象：text / reasoning / tool / compaction / retry / patch 等都是 part；tool part 用同一 part id 走 `pending→running→completed/error` 状态机 upsert，避免更新单个 part 重写整条消息。
- **压缩 = 普通 part，不是独立机制**：插入 `compaction` part + summary 消息，`tail_start_id` 指向保留尾部，旧 tool 输出打标记；恢复时 active chain 从最后一个 boundary 开始。
- **崩溃恢复语义**：不建 run 表；恢复时把 `pending/running` 的工具一律转为失败 tool result（`[Tool execution was interrupted before resume]`），避免 provider 收到 dangling tool call。
- **三层版本兼容规则表**：strict 解析字段永不扩展 / ignore-unknown 字段放新数据 / safeParse 字段可加但写明退化——保证旧版本读新数据「打不开会话」优于「丢正文」。

**对 EasyAgent**：JSONL append-only 存储（`sdk/session/jsonl.go`）与这套语义完全兼容——message/part 分离对应 entry 粒度设计，中断工具转失败 result 可以直接加到 `BuildContext()` 重建逻辑，兼容规则表可写入 session-format.md。全部语言无关。

**建议优先级**：[可立即抄]（崩溃恢复语义与兼容规则表），[中期]（part 状态机化）。

### 2.2 Permission broker 二分 + fail-closed

**是什么**：**参考 `apps/zcode-cli/docs/design/v2/permission-broker.md`（已核对实现）**。

- **PermissionService（同步策略）与 PermissionBroker（异步客户端协商）拆开**：决策枚举 `allow / deny / modify（完整替换输入）/ escalate`。
- **无 broker 时默认 fail-closed 立即 deny**：headless / 测试环境永不悬挂。
- Hook 与 broker 竞速先到先得；权限记忆（once / always-in-project / deny）在执行前先落盘。
- **Mode（plan/build/edit/yolo）只影响权限门判定，不改变工具目录**——避免 tools schema 变化打爆 provider prompt cache。
- 每个工具必须声明「信封」：只读性 / 破坏性 / 并发安全 / 超时 / 取消 / 权限需求 / 最大输出 / 副作用范围（session/workspace/git/network/system）。

**对 EasyAgent**：审批体系（逐批询问等）可对照这三条检查：无审批器时是否 fail-closed、modify 是否完整替换、mode 切换是否影响工具 schema。工具「声明信封」与现有 `agent.Tool` 可选接口（`ToolWithMode`、`ConcurrencySafeChecker`）是同方向延伸，可作 `ToolDescriptor` 演进参考。

**建议优先级**：[可立即抄]（fail-closed 与 mode/tools 解耦两条设计决策），[中期]（声明信封）。

### 2.3 Subagent：fork-for-cache + 工具池过滤链 + 子 agent 即持久化 session

**是什么**：**参考 `apps/zcode-cli/docs/design/v2/tool/07-subagent.md`（约 500 行，最完整的一篇）**。

- 模型可见工具名 `Agent`（别名 `Task`），五种形态：fresh / **fork** / background / worktree isolation / remote（未实现）。
- **fork 型子代理继承父会话已渲染的 system prompt + 精确 tool array + thinking config，byte-identical 以命中 prompt cache**。
- **ToolPoolResolver 过滤链**：worker permission context → 候选工具 → agent 声明过滤 → 全局 subagent 过滤（禁递归 `Agent`、禁 `AskUserQuestion`/`TaskOutput` 等交互工具）。
- 子 agent 是同进程新执行上下文（非 OS 子进程）；**持久化为独立 session（`taskType=subagent_child`），工具事件镜像到父 live sink 但不写父 event store**（避免误计为父的工具调用）。
- 后台任务：>2s 出现 background hint，可 foreground→background 转换（同 task id 重入队）；完成以通知重入主循环。

**对 EasyAgent**：多 Agent 是规划中能力。三个可直接吸收的决策：fork-for-cache（复用渲染结果而非重新构造）、过滤链写成显式测试集、子会话事件镜像不落父库。ZCode 明确没有 team/swarm 运行时（`team_name` 只在 schema 里被识别），说明对话式「团队协作」并非业界已验证的必选项。

**建议优先级**：[中期]（做 subagent 时按此清单对齐）。

### 2.4 Provider capability-first 契约：OptionSpecs + map 单一权威

**是什么**：**参考 `docs/working-memory/provider-refactor/design/model/contract.md` 与 `v2/model/glm-5.2-reasoning-depth.md`**。

- Model = identity + 能力属性 + **OptionSpecs**（`reasoningLevel` 按语义强度排序、`maxOutputTokens` limit；每个 spec 带 `map` 字段 = 请求字段映射的唯一权威）。
- **缺值在网络 I/O 前失败，不从 spec 猜默认值**；Option 集合封闭——provider 不能塞任意参数。
- **能力由显式声明规则给出**（按模型/Provider/API 类型组合匹配），禁止按 modelId 前缀临时推断（旧实现被明确废弃）。
- **GLM 教训**：思考档位映射必须按传输层分别定义（OpenAI-compat 走 `reasoning_effort`，Anthropic-compat 走 `thinking.budgetTokens`）——复用 openaiCompatible options 到 anthropic 传输会出现「UI 显示开思考但请求里没字段」。

**对 EasyAgent**：刚合入的智谱接入（#125）在通用 OpenAIProvider 里按 `glm-` 前缀特判——这正是 ZCode 明确废弃的「前缀临时推断」路线。中期演进方向是把模型能力（thinking 格式、reasoning 字段、max_tokens 字段名）收进显式声明的模型配置规则，Provider 实现只读结论。**双传输映射的教训直接适用**。

**建议优先级**：[中期]（provider 数量增长前重构），教训部分[可立即抄]（新增 provider 时按传输层隔离选项映射）。

### 2.5 自更新：immutable release + readiness + startup recovery

**是什么**：**参考 `packages/zcode-server-cli/src/runtime/`（模块名即设计）**。

- 流水线：`releaseDownload → updatePreparation → releaseInstaller → updateHandoff → updateReadiness`，`releaseManager` 总控；外围 `lock.ts`（防并发）、`installationOwnership.ts`（安装目录归属）、**`immutableRelease.ts`（release 不可变，切换用 launcher 指针）**、`startupRecovery.ts`（启动时恢复/回退）、`uninstallGuard.ts`。
- 校验 = checksum + 「6 个打包目标资源合并时重复路径必须 hash 一致否则构建失败」；未发现更新包代码签名。
- release 后在仓库外用 pty 跑真包 smoke test（解压实际产物、验证 TUI 渲染/键盘/WS 握手）。

**对 EasyAgent**：刚合入的 `easyagent update`（#126）是「下载→校验→原地替换 + .bak 回滚」的单步模型。ZCode 的「release 不可变 + 指针切换 + 新版本健康检查通过才切指针（readiness）+ 启动时恢复」是更抗变砖的形态——Go 单二进制做这套比 Node SEA 更简单，可作为 update 命令的中期演进（保留多版本目录 + launcher 指针 + 启动自检）。

**建议优先级**：[中期]（update 稳定使用一段时间后演进）。

### 2.6 插件生态兼容策略：「兼容但不继承高权限语义」

**是什么**：**参考 `docs/claude-code-marketplace-plugin-compat.md`（含真实插件验证记录）**。

- 最大努力兼容 Claude Code Marketplace：marketplace add/install/update/remove 生命周期、manifest 识别、skills/commands/hooks/mcpServers 运行投影。
- **分阶段安全边界**：安装闭环 → 低风险运行投影 → 依赖解析 → 高权限能力（plugin agents/LSP/channels）放到最后阶段。
- **缺少安全模型的能力必须可诊断，不能静默运行**：识别出 npm/pip source 但返回 unsupported diagnostic，绝不执行。
- MCP server key 强制 `plugin:name:name` 命名空间防覆盖；`permissionMode` 只能收紧；sensitive userConfig 禁止进入模型可见文本。
- 市场分发：CDN `marketplace.json` listing 与 zip 包解耦、本地 seed 分片合并（ADR 0001–0004）。

**对 EasyAgent**：未来做插件/技能生态时的安全边界范本；EasyAgent 已有 skillmarket（官方 store 搜索安装），「cache 与 data 分离」「unsupported diagnostic 化」两条成本极低。

**建议优先级**：[观望]（生态扩张前研读），存储布局部分[可立即抄]。

### 2.7 Rewind/Compact 协同状态模型

**是什么**：**参考 `apps/zcode-cli/docs/design/v2/rewind-compact.md`**。

- Rewind 不删历史：`session.revert` JSON + snapshot + patch part；**跨 compact boundary 的回退用 append-only branch cut**（`keptMessageIDs + branchCutAfterMessageID + branchGeneration`）。
- workspace checkpoint 独立于 compact 覆盖（file-only rewind 仍可用）；checkpoint artifact 记录每文件修改前内容 + 结构化 diff。
- Auto compact 触发**优先用 provider 真实 usage.inputTokens**，本地估算只补增量；`model_context_window_exceeded` finish reason 归一为 `ModelContextExceeded` 触发 reactive compact。

**对 EasyAgent**：已有 `MoveTo(entryID)` 分支导航与 compaction entry（JSONL），与「append-only branch cut」同源；可直接吸收的是「真实 usage 驱动 auto-compact」与「context window exceeded 归一化触发 reactive compact」两个触发器语义。

**建议优先级**：[可立即抄]（两个触发器语义），[中期]（checkpoint artifact）。

### 2.8 Docs-as-specs + architecture-policy CI（元实践）

**是什么**：根目录 `architecture-policy.yaml` + `pnpm architecture:check --changed` 把依赖方向、模块边界变成 **CI 里机器可检查的声明式策略**；每篇特性文档必须包含非目标、契约、超时/取消语义、最低测试集清单和**带日期的真实验证记录**（如「2026-09-20 open-source branch: node --test 5 项通过」）；接口成熟度分级 L0 Concept→L3 Stable，禁止提前写死接口。

**对 EasyAgent**：已有 `sdk/arch_test.go`（依赖方向检查）与 docs/decisions/——可对比的两步：① 把「设计文档必须带验证记录」写进 CONTRIBUTING 检查项；② arch test 的规则外置成声明式配置，随架构增长而增长。ZCode 还有一个 `formal-proof` 包（形式化证明），属过度超前，不建议跟进。

**建议优先级**：[可立即抄]（验证记录要求），[中期]（策略外置）。

---

## 3. ZCode 也没有的东西（防止盲目跟进）

| 领域 | 现状 |
|---|---|
| OS 级沙盒 | 只有边界设计（SandboxPort 处于 spike、Bash AST 权限分析、Windows Job Object containment），**无成品实现**。EasyAgent 走 Seatbelt/Bubblewrap 路线需自行推进 |
| Agent team/swarm 运行时 | `team_name` 在 schema 中被识别，但文档明确按独立工具处理，无运行时 |
| 更新包代码签名 | 仅 checksum + 打包 hash 一致性约束，与 EasyAgent update 现状同级 |
| Remote agent 执行 | subagent 的 remote 形态在文档中标注未实现 |

## 4. 推荐阅读清单

| 路径（zai-org/ZCode 仓库内） | 一句话说明 |
|---|---|
| `apps/zcode-cli/docs/design/v2/session-persistence.md` | 会话存储完整 schema（本文 §2.1） |
| `apps/zcode-cli/docs/design/v2/permission-broker.md` | 权限二分架构（本文 §2.2） |
| `apps/zcode-cli/docs/design/v2/tool/07-subagent.md` | 多 agent 最完整规格（本文 §2.3） |
| `docs/working-memory/provider-refactor/design/model/contract.md` | Provider 契约（本文 §2.4） |
| `packages/zcode-server-cli/src/runtime/` | 自更新运行时（本文 §2.5） |
| `docs/claude-code-marketplace-plugin-compat.md` | 插件生态兼容规格（本文 §2.6） |
| `apps/zcode-cli/docs/design/v2/rewind-compact.md` | Rewind/Compact 协同（本文 §2.7） |
| `docs/dangerous-command-approval-v1-plan.md` | 危险命令审批 v1 |
| `docs/shared-task-cli.md` | 单 CLI host 多任务进程模型（含 Windows Job Object 细节） |
| `docs/bot-channel-runtime-split.md` + `docs/bots-feishu-*.md`（8 篇） | 飞书渠道与核心拆分、群协作信任模型（EasyAgent 飞书桥直接相关） |
| `docs/mobile-remote-control-android.md` | Android 控制面架构（QR 配对、relay） |
| `docs/architecture/architecture.md` | 全局架构总览 |

## 5. 调研方法与局限

- 基于 main @ `fec97590`（v3.18.2 快照，2026-11-13）的目录/文档快照、3 个 GitHub Release 文案、docs/ 300+ 篇设计文档与版本间目录对比；**sync-bot 快照模式无细粒度 commit 历史**，v3.15.1→v3.17.1 区间 compare 因文件数过多被截断，该窗口结论以 packages/ 目录快照对比与 release 文案为准。
- 「已核对实现」仅针对文档中自述含实现核对记录的模块（permission broker、dynamic workflow）；其余设计未经源码逐行验证。
- ZCode 是 TypeScript monorepo，文中架构结论均已翻译为与语言无关的表述；涉及 Node 特有机制（SEA 打包等）仅作背景。
