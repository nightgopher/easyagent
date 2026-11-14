> 历史参考：本文仅反映记录时的外部项目或接口版本，不代表 EasyAgent 当前实现。旧路径与建议仅供追溯；当前使用方式见 [文档索引](../README.md)。

# Codex（ChatGPT）登录对齐审计 — EasyAgent vs pi vs 官方 Codex CLI

> 调研日期：2026-11-13
> 审计对象：`EA_PROVIDER=codex` 的登录（`internal/codex/`）与模型请求（`sdk/ai/providers/codex.go`）
> 对标实现：[earendil-works/pi](https://github.com/earendil-works/pi)（`packages/ai/src/auth/oauth/openai-codex.ts`、`packages/ai/src/api/openai-codex-responses.ts`）
> 参照基准：[openai/codex](https://github.com/openai/codex)（`codex-rs/login/`、`codex-rs/core/src/client.rs`，官方 Rust 客户端）
> 性质：**仅说明问题，不做代码修改**。修复建议见 [第六节](#六修复建议不在本-pr-范围)，供后续 PR 实施。

---

## 0. 结论摘要

- **OAuth 登录流程本身与 pi 高度一致**：client_id、authorize/token 端点、PKCE(S256)、loopback 回调、state 校验、token 交换与刷新表单全部对齐，未发现会导致登录失败或凭据泄露的缺陷。
- **偏差集中在"登录之后"用 token 调 `chatgpt.com/backend-api` 的请求指纹**，共 4 处 gap：
  - 🔴 P1：`originator` 头发送 `codex_cli`，既不是官方精确值（`codex_cli_rs`），也不是自己的名字，属"半冒充"指纹，且与自家 authorize URL 自报的 `originator=easyagent` 自相矛盾。
  - 🔴 P1：模型请求未设 `User-Agent`，落到 Go 默认 `Go-http-client/1.1`；pi 与官方均发送描述性 UA。
  - 🟡 P2：authorize URL 缺 `id_token_add_organizations=true`（官方与 pi 均发送）。
  - 🟡 P2：模型请求缺 `session-id` / `x-client-request-id`（官方与 pi 均发送；官方据此做 prompt cache 亲和）。
- 另有 1 处与 pi 相同的"较官方更窄"项（scope 少 `api.connectors.*`），仅记录，不构成对齐 pi 的缺口。

## 1. 登录流程对比

三方均复用 Codex CLI 注册的公开 OAuth client（`app_EMoamEEZ73f0CkXaXp7hrann`，PKCE 保护交换，client 本体即携带该值），不注册独立应用。

| 项 | 官方 codex CLI | pi | easyagent | 判定 |
|---|---|---|---|---|
| client_id | `app_EMoamEEZ73f0CkXaXp7hrann` | 同 | 同 | ✅ 一致 |
| authorize 端点 | `{auth.openai.com}/oauth/authorize` | 同 | 同 | ✅ |
| token 端点 | `/oauth/token` | 同 | 同 | ✅ |
| 回调地址 | `http://127.0.0.1:1455/auth/callback` | `http://localhost:1455/auth/callback` | `http://localhost:1455/auth/callback` | ✅ 可接受（pi 同款） |
| PKCE | S256 | S256（32 字节 verifier） | S256（48 字节 verifier） | ✅ 均符合 RFC 7636（43–128 字符） |
| state | 随机、回调校验 | 随机 16B hex、回调校验 | 随机 16B、常量时间比较且缺失即拒绝 | ✅ easyagent 更严 |
| scope | `openid profile email offline_access api.connectors.read api.connectors.invoke` | 前 4 项 | 前 4 项 | ⚠️ 与 pi 一致；比最新官方窄（见 P3） |
| `codex_cli_simplified_flow=true` | 发送 | 发送 | 发送 | ✅ |
| `id_token_add_organizations=true` | 发送 | 发送 | **未发送** | ⚠️ P2-1 |
| `originator` 查询参数 | `codex_cli_rs` | `pi`（可用 agentName 覆盖） | `easyagent` | ✅ 三方均报自己，模式一致 |
| code 交换 | `authorization_code` + `code_verifier`，无 client_secret | 同 | 同 | ✅ |
| 刷新 | `refresh_token` + client_id | 同 | 同 | ✅ |
| chatgpt_account_id 提取 | 解析 **id_token** 的 `https://api.openai.com/auth` claim | 解析 access token 的同一 claim | 解析 id_token 的同一 claim（另先尝试 token 响应顶层字段） | ✅ 与官方一致 |

补充：easyagent 的凭据存储（0600 文件 + 0700 目录 + 跨进程锁 + 原子写 + 非 Windows 下的权限校验）比 pi 的凭据管理更严格，此方向无 gap。

## 2. 模型请求指纹对比

请求均为 `POST https://chatgpt.com/backend-api/codex/responses`，Responses API 流式，`OpenAI-Beta: responses=experimental`，`chatgpt-account-id` 头两边都携带。差异如下：

| 请求头 | 官方 codex CLI | pi | easyagent | 问题 |
|---|---|---|---|---|
| `originator` | `codex_cli_rs`（`default_client.rs: DEFAULT_ORIGINATOR`） | `pi`，源码注释："Codex expects an originator on every request; it is not attribution" | **`codex_cli`** | 🔴 P1-1 |
| `User-Agent` | `codex_cli_rs/<ver> (<os> <ver>; <arch>) <terminal>` | `pi/<ver> (<platform>; <runtime>/<ver>; <arch>)` | **未设置 → Go 默认 `Go-http-client/1.1`** | 🔴 P1-2 |
| `session-id` | 发送（注释："ChatGPT derives cache affinity from the Responses session-id header"） | 发送（有 sessionId 时） | **未发送** | 🟡 P2-2 |
| `x-client-request-id` | 发送（thread id） | 发送（同 session 值） | **未发送** | 🟡 P2-2 |
| 401 处理 | 刷新重试 | 到期刷新 | 401 → 刷新一次 → 重试；未换发新 token 判登录失效 | ✅ 语义一致 |
| 代理 | 系统/PAC 感知 | 支持 | `EA_CODEX_PROXY` 注入（登录与模型流量一致） | ✅ |
| 请求体压缩 | WebSocket + zstd | SSE 可选 zstd | 未压缩 JSON | 无风险，纯性能差异 |

## 3. 问题清单

### P1-1 请求头 `originator` 半冒充官方客户端

- **现状**：`sdk/ai/providers/codex.go` 的 `Stream()` 固定设置 `originator: codex_cli`。
- **对照**：官方精确值是 `codex_cli_rs`（`codex-rs/login/src/auth/default_client.rs` 的 `DEFAULT_ORIGINATOR`，`is_first_party_originator` 只认 `codex_cli_rs`/`codex-tui`/`codex_vscode`/`Codex *`）；pi 报自己的 `pi`。
- **自相矛盾**：easyagent 的 authorize URL 已自报 `originator=easyagent`（`internal/codex/auth.go` `newAuthorizationFlow`），同一产品的登录侧与请求侧身份不一致。
- **风险**：风控视角下"声称是官方客户端但指纹（UA/协议细节）对不上"比"老实自报第三方"更容易被标记为异常客户端。pi 的选择（自报 + 注释说明 originator 非归因）是已验证的第三方姿势。
- **建议**：请求头改为 `easyagent`，与登录侧一致。

### P1-2 模型请求缺少 User-Agent

- **现状**：codex provider 及其共享流式客户端均未设置 `User-Agent`，实际发送 Go 默认的 `Go-http-client/1.1`（全仓仅 `deepv.go` 设置了 `EasyAgent`）。
- **对照**：官方与 pi 均发送"产品名/版本 (平台; 运行时; 架构)"格式的描述性 UA。
- **建议**：格式对齐 pi，如 `easyagent/<version> (<GOOS>; go/<GOVERSION>; <GOARCH>)`，登录流量（`internal/codex`）与模型流量（provider）统一。

### P2-1 authorize URL 缺 `id_token_add_organizations=true`

- **现状**：`internal/codex/auth.go` `newAuthorizationFlow` 的参数表没有该项；官方与 pi 都发送。
- **影响**：id_token 不携带组织信息。不影响 `chatgpt_account_id` 获取（已在 `https://api.openai.com/auth` claim），也不影响登录成败，但与参照基准不一致。
- **建议**：参数表补一行。

### P2-2 模型请求缺 `session-id` / `x-client-request-id`

- **现状**：`sdk/ai/providers/codex.go` 未发送；`ai.StreamRequest` 也无会话标识字段可透传。
- **对照**：官方与 pi 均发送。官方注释明确 ChatGPT 用 `session-id` 做缓存亲和；pi 以同一值同时填 `session-id` 与 `x-client-request-id`。
- **影响**：功能上无硬失败；缓存亲和（同会话前缀复用）与请求可关联性缺失。
- **建议**：把会话级稳定 ID 透传进 provider（例如扩展 `StreamRequest` 或 provider 构造参数），按 pi 方式同值发送两个头。改动面比前三项大，需评估透传路径。

### P3（记录项）scope 比最新官方窄

- 官方现行 scope 含 `api.connectors.read api.connectors.invoke`，pi 与 easyagent 均为前 4 项。
- **对齐 pi 的目标下不构成缺口**，仅记录。若未来要接 Codex connectors 能力再补齐，需同步更新授权与刷新测试。

## 4. 已对齐项（无需改动）

- client_id、authorize/token 端点、loopback `:1455/auth/callback`（含端口被占时手动粘贴兜底，pi 同款）。
- PKCE S256、state 生成与校验（easyagent 为常量时间比较且 state 缺失即拒绝，比 pi 更严）。
- token 交换/刷新表单（无 client_secret；刷新携带 client_id；刷新失败区分"需重登"与"网络重试"）。
- `chatgpt-account-id`、`OpenAI-Beta: responses=experimental`、`Accept: text/event-stream`。
- 401 → 单次刷新 → 重试，token 未换发即判登录失效（避免死循环刷新）。
- 凭据 0600 私有保存、0700 目录、跨进程串行刷新、原子替换写入。
- 代理：登录与模型流量走同一解析结果（`EA_CODEX_PROXY` / 标准代理变量），与 pi 的 proxy 支持等效。
- account ID 从 id_token 的 `https://api.openai.com/auth` claim 提取（与官方一致；pi 用 access token 属其自身变体）。

## 5. 封号风险评估

- 触发封号的通常是**配额滥用与异常流量模式**（高频、批量、多设备复用凭据），而非单个请求头的拼写差异。
- easyagent 的凭据模型是"用户自己登录、自己消费自己的 ChatGPT 订阅额度"，与官方客户端的授权语义一致；client_id 复用 Codex CLI 的公开值 + PKCE 的第三方做法 pi 已长期对外发布，未见 client_id 层面的封禁记录。
- 本审计发现的偏差属于**指纹一致性**问题：当前组合（冒充官方 originator + 裸 Go UA + 无 session 头）正是风控里最可能被归类为"不规整第三方客户端"的形态。按 pi 的方式自报身份后，指纹自洽，风险面收窄。
- 结论：**当前实现不预期直接导致封号，但 P1 两项是明确的"不该这样"，应在下一个功能 PR 里一并修正。**

## 6. 修复建议（不在本 PR 范围）

| 项 | 改动点 | 规模 |
|---|---|---|
| P1-1 | `sdk/ai/providers/codex.go`：`originator` 头 `codex_cli` → `easyagent`；同步更新断言该头的测试 | 小 |
| P1-2 | provider 与 `internal/codex` 的 HTTP 客户端统一设置描述性 `User-Agent` | 小 |
| P2-1 | `internal/codex/auth.go` `newAuthorizationFlow` 参数表补 `id_token_add_organizations=true` | 小 |
| P2-2 | 会话 ID 透传进 provider，发送 `session-id` + `x-client-request-id` | 中（需定透传路径） |

按仓库 TDD 规范实施：先改/补断言再动实现；`sdk/` 不得 import `internal/`，User-Agent 的版本来源需走已有公共入口。
