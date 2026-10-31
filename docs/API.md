# HTTP API 参考

> EasyAgent Server 模式（`easyagent serve`）的主要 API 参考。请求结构以 [`internal/server`](../internal/server/) 中对应处理器为准。

---

## 快速开始

```bash
easyagent serve
# Server running on http://127.0.0.1:8080
```

### 认证

三级访问控制模型（`/health` 始终开放）：

1. **设置了 `EA_SERVER_API_KEY` 或兼容的 `EA_API_KEY`** → 普通 API 请求必须带 `Authorization: Bearer <服务令牌>`；独立令牌优先。WebSocket 用 `?token=<服务令牌>` 查询参数。
2. **未设置（默认）** → 仅放行 loopback 来源；非回环请求返回 401。本机消费方（网页 UI、飞书 bridge、桌面端）零配置可用。
3. **未配置 Key 且 `EA_ALLOW_NO_AUTH=1`** → 放开普通 API（仅限本机调试，切勿暴露端口）。

飞书设置与部署管理接口始终要求显式 Bearer 认证；静态页面可加载不代表 API 已授权。

CORS 默认不返回跨域头；需要浏览器跨域访问时配置 `EA_ALLOWED_ORIGINS`（逗号分隔白名单）。

```bash
curl -H "Authorization: Bearer $EA_API_KEY" http://127.0.0.1:8080/health
```

---

## 业务 Agent

业务 Agent 是持久化配置，复用同一会话核心，不为每个角色部署独立程序。
配置保存在当前主机 `<data_dir>/agents/agents.sqlite`，由该主机的服务令牌管理。

- `GET /agents`：返回 `agents` 和 `default_tools`。
- `POST /agents`：创建，返回 201 和完整配置。
- `GET /agents/{agent}`：读取。
- `PUT /agents/{agent}`：完整更新；必须带上读取到的 `version`，并发修改返回 409。

```json
{
  "name": "客户反馈助手",
  "instructions": "整理客户反馈，区分事实与推测。缺少依据时先向用户确认。",
  "model": "",
  "tools": ["read", "write", "ask_user_question", "local_time", "search_tools"],
  "skills": [],
  "disabled": false
}
```

`tools` 未提供时使用上述精简默认值，显式 `[]` 表示不授权业务工具。
主机级工具限制仍生效；MCP 工具只有名称被明确授权后才可发现和调用。
技能按名称选择，以摘要展示，`read_skill` 按需读取正文；绑定技能不增加其他工具权限。
业务 Agent 不自动继承全局 `AGENTS.md`、个人画像或项目编码提示词。

`workspace` 可指定当前主机的绝对路径；省略时使用专属工作目录。
文件工具限制在该目录内。角色配置使用当前主机的模型服务，`model` 为空时继承默认模型。

通过 `POST /sessions` 发送 `{"agent_id":"agent-…"}` 创建会话。
`application` 可省略或为 `business`；`cwd` 必须省略或等于该 Agent 的工作目录。
会话列表和 `/sessions/{id}/info` 返回 `agent_id`。分叉默认保留归属，显式切换到旧应用时清除归属。
模型选择保存在会话中；角色说明、技能和工具权限在下次执行前重新读取。
修改工作目录后需要新建会话。设 `disabled: true` 停止后续执行和新会话创建。
旧会话没有 `agent_id` 时沿用原来的 Coding/Music/KB 行为。


## 对话

### 同步对话

```
POST /chat
```

```json
{
  "prompt": "帮我看看这个项目结构",
  "session_id": "sess_123"
}
```

### SSE 流式对话

```
POST /chat/stream
```

Server-Sent Events 流式输出，持续返回文本增量、工具与任务状态事件；增量块不保证对应单个 token。

先返回 `session_id` 与 JSON `accepted`（`session_id`、`run_id`、`request_id`、`duplicate`），再发送具名 Agent 事件。正常结束有 `done`；失败或取消可能携带部分结果，但不能仅凭 `done` 或 HTTP 200 判断成功，应查询 `GET /sessions/{id}/run` 的 `run.state`（如 `completed`、`failed`、`cancelled`、`interrupted`）。取消时部分消息的 `stop_reason` 为 `aborted`，持久化失败仍会返回错误。

关闭 SSE 仅停止订阅。主动停止任务需另发 `POST /sessions/{id}/run/cancel`，body 为接受时取得的 `{"run_id":"run_..."}`；返回 `{"cancelled":true}` 表示取消信号已提交，随后读取 run 状态确认收尾。不能用旧 run ID 取消后续任务。

```bash
curl -N -X POST http://127.0.0.1:8080/chat/stream \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $EA_API_KEY" \
  -d '{"prompt": "你好"}'
```

### WebSocket

```
GET /ws
```

全双工 WebSocket 连接，支持对话、恢复订阅、取消、工具审批、模型切换和流式回复。需要认证时使用 `ws://host:8080/ws?token=<URL 编码后的令牌>`；HTTPS 部署使用 `wss://`。

客户端消息示例（继续已有会话；新会话可省略 `session_id`）：

```json
{"type":"prompt","session_id":"sess_123","prompt":"分析项目结构","request_id":"client-generated-unique-id"}
```

服务先落盘接受凭据，再返回 `accepted`（含 `request_id`、`run_id`、`state`、`duplicate`）。客户端在接受前保留草稿；响应不确定时以同一 `request_id` 和原始 prompt 重试。相同 ID 的不同请求内容返回冲突；同一会话一次只能执行一个任务。

会话的历史读取、工作区快照与工具准备也属于受理占位期，该阶段拒绝另一条消息和删除、分叉、权限切换等会话变更。并发重试同一请求等待这次受理的结果，准备失败且未执行的请求可继续以原 ID 重试。准备工作不持有所有会话共用的运行锁，其他会话仍可取消或审批已接受的任务。

```json
{"type":"subscribe","session_id":"sess_123","run_id":"run_123","after_seq":12}
{"type":"cancel","session_id":"sess_123","run_id":"run_123"}
{"type":"confirm","session_id":"sess_123","run_id":"run_123","confirmation_id":"confirmation_123","approved":true}
```

完成的 run 在 `status.run` 与恢复快照的 `run` 中携带 `user_entry_id` 和 `assistant_entry_id`（若有已落盘的最终文字回答）；后者可用于从回答分叉，包含该回答及此前工具结果。历史 assistant 消息包含 `entry_id` 与 `stop_reason`，带工具调用或错误/取消的中间输出不作为桌面回答分叉入口。分叉只复制会话记录，不回退工作目录文件。

`event` 包含递增 `seq`、`run_id` 和 Agent `event`。重连发送 `subscribe`：可重放时返回 `replay`，否则返回 `snapshot`（历史 `messages`、当前 run、已投影事件及 `pending_confirmations`）。客户端按 seq 去重，不将恢复内容当成新请求。序号缺口应重新订阅；`reset:true` 表示历史完整重建，允许序号重新开始。`confirmation` 表示任务等待批准，超时拒绝。旧 run 的取消与批准请求会被拒绝，避免影响后续任务。

历史消息的 `images` 保留数组形状，每张图片通过 `{"attachmentId":"history_...","sessionId":"sess_..."}` 引用会话内已保存的图片，避免在 HTTP / WebSocket 快照中累计内联图片数据。客户端按需请求带认证的 `/sessions/{sessionId}/attachments/{attachmentId}/raw?format=data_url`；引用只在该会话当前分支的祖先记录（包括压缩前记录及保留片段）包含图片时有效，不允许读取兄弟分支。分叉快照使用分叉会话自己的 `sessionId`。图片哈希不能替代认证。客户端仍须兼容旧服务返回的 `data_url` 图片；实时新消息继续使用已有上传附件引用。

任务由会话持有，WS / SSE 连接关闭不会取消任务。服务重启后已接受而未完成的 run 标为 `interrupted`，相同 request ID 不重新执行。凭据保存于 `EA_DATA_DIR/requests`，完整聊天历史仍来自会话 JSONL；硬断电下的外部副作用不保证恰好一次。

任务互斥及幂等恢复覆盖同一核心服务及其重启。多个核心进程不得同时共用同一数据目录；当前没有跨进程的会话锁或执行互斥。桌面托管核心使用独立数据目录，多个客户端应连接同一核心来共享会话。

其他客户端类型为 `unsubscribe`、`switch_model`（`model`，可选 `provider`）、`ping`。服务端还返回 `session_id`、`status`、`model_info`、`error`、`pong`；错误说明在 `message`。SSE `/chat/stream` 同样接受 `request_id`，并增加 `accepted` 与 `confirmation` 事件；HTTP 断线后可通过下面的 run 接口恢复。结构定义见 [websocket.go](../internal/server/websocket.go)。

---

## 会话管理

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/sessions` | 列出所有会话；运行中的会话额外带 `run_state`（`running` / `waiting_confirmation`）和 `pending_confirmations` |
| `GET` | `/sessions/search` | 搜索当前主机的完整历史标题、用户消息与助手正文，含未打开、归档及压缩前的原始消息 |
| `GET` | `/sessions/{id}/history?entry_id=...` | 只读原始消息及前后各两条可搜索消息，不改变当前分支或模型上下文 |
| `POST` | `/sessions` | 创建新会话 |
| `GET` | `/sessions/{id}/messages` | 获取会话消息（含压缩记录、消息 `entry_id` 与图片） |
| `POST` | `/sessions/{id}/fork` | 分叉会话：空闲时省略参数复制完整分支；`entry_id` 保留至指定历史消息，空字符串创建空分叉；或 `before_message_index` 在第 N 条用户消息之前分叉（0 从空开始），两者互斥。运行中只能选择本轮开始前的历史点；分叉点与 `kept_messages` / `kept_user_messages` / `dropped_messages` 均基于同一历史快照，不计压缩摘要或当前轮消息 |
| `GET` | `/sessions/{id}/attachments/{attID}/raw` | 认证读取工作区内附件；`?format=data_url` 返回受支持图片的 JSON `data_url`，供桌面认证通道预览 |
| `GET` | `/sessions/{id}/info` | 获取会话信息、`access_mode` 及 `context_usage` |
| `POST` | `/sessions/{id}/permissions` | 切换空闲会话权限，body 为 `{"mode":"ask"}` 或 `{"mode":"full"}` |
| `DELETE` | `/sessions/{id}` | 删除会话 |
| `POST` | `/sessions/{id}/model` | 切换会话模型 |
| `POST` | `/sessions/{id}/compact` | 压缩会话上下文 |
| `POST` | `/sessions/{id}/command` | 执行斜杠命令 |
| `GET` | `/sessions/{id}/run` | 当前 run 与恢复快照 |
| `POST` | `/sessions/{id}/run/continue` | 从已保存的工具结果继续失败任务，body 为 `{"run_id":"..."}`（可选幂等 `request_id`），返回 202 |
| `GET` | `/sessions/{id}/run-files` | 最近最多 50 个任务的文件变更汇总，含 `workspace`、`incomplete`、`skipped` 与来源 |
| `GET` | `/sessions/{id}/runs/{run}/files` | 单个任务文件变更详情及可用的修改前后内容 |
| `POST` | `/sessions/{id}/runs/{run}/undo` | 撤回单个任务的文件修改，body 为 `{"path":"...","after_hash":"..."}`；存在运行中任务时返回 409 |
| `GET` | `/sessions/{id}/queue` | 读取待发消息队列（`items`、`paused`、`revision`） |
| `POST` | `/sessions/{id}/queue` | 队列操作：body 为 `{"action":"add\|edit\|remove\|pause\|resume\|send_now","id":"...","prompt":"...","inputs":{...}}`，冲突返回 409 |
| `POST` | `/sessions/{id}/attachments` | 上传工作区附件，body 为 `{"name":"...","data":"<base64>"}`（1 字节–4MiB），图片校验尺寸上限 32M 像素 |
| `GET` | `/sessions/{id}/capabilities` | 会话实际加载的技能与工具清单（含来源、`scope`=`project`/`personal`、项目技能的 `origin` 与所覆盖个人技能的 `overrides`、调用方式） |
| `POST` | `/sessions/{id}/skills/refresh` | 重新扫描技能目录，使会话启动后新增或修改的技能从下一轮起生效；任务运行中返回 `409` |
| `GET` | `/skills/local` | 主机全局技能目录里的全部技能（手写的、其他工具装的、市场安装的），含来源目录、是否市场安装、启用状态（enabled）、被同名技能覆盖的提示和加载警告；目录与会话加载时一致 |
| `PUT` | `/skills/local` | 设置全局技能开关，JSON 为 `{"path":"绝对 SKILL.md 路径","enabled":false}`；返回更新后的全局技能列表。状态持久化到当前主机，仅影响 EasyAgent，不修改共享技能文件。新会话生效，已有会话需刷新技能；关闭优先技能不会自动加载被覆盖的同名技能 |
| `PATCH` | `/sessions/{id}` | 更新会话偏好：`title`（1–160 字符）、`pinned`、`archived` |
| `POST` | `/sessions/{id}/run/cancel` | body 为 `{"run_id":"..."}` |
| `POST` | `/sessions/{id}/run/confirm` | body 为 `{"run_id":"...","confirmation_id":"...","approved":true}`；提问须同时提交 `answers`，见下文 |
| `GET` | `/sessions/{id}/diff` | 获取会话 Git diff |
| `GET` | `/sessions/{id}/file` | 获取会话文件内容 |
| `PUT` | `/sessions/{id}/file` | 写入会话文件 |

文件变更来自任务前后快照，范围限定在该任务的工作区。`attribution:file_tool` 表示可确认的文件工具修改，`observed` 表示任务期间观察到但无法可靠归因的变化（包括 Bash、其他进程）；它们不是当前 Git 状态。快照截断时 `incomplete:true`，汇总与详情都过滤无法核对的单侧增删，不能把数量视为完整改动面。Git 工作区及多仓库工作区中的子仓库遵循 Git 忽略规则，已跟踪文件仍纳入；非 Git 目录跳过常见产物目录及 `.noindex`，未被规则忽略的用户文件仍会记录。

桌面只汇总当前工作区最近最多 20 个有变更的任务，按路径去重，以最新记录的来源区分文件工具修改和其他变化；最新任务已撤回时标注“已撤回”。行数使用首次修改前到最后修改后的内容，内容不连续或无法计算时显示“部分”。历史观察记录不能证明全部变化由助手造成；旧服务的截断汇总也在桌面兼容过滤。记录缺失时不反推或伪造工具归属。

`ask_user_question` 的待回答请求通过 `confirmation` 事件和 snapshot 的 `pending_confirmations` 返回，`args.questions` 包含问题、选项及 `multiSelect`。即使会话为完全权限模式，也必须等待用户回答；提问不会被普通审批的短超时自动跳过，直到回答、跳过或取消任务。提交到同一 `/run/confirm` 接口：

```json
{"run_id":"...","confirmation_id":"...","approved":true,"answers":[{"selected":["选项标签"]},{"selected":[],"text":"自定义回答"}]}
```

`answers` 按问题顺序排列，每题必须有答案；单选题只允许一个选项或自定义文本，多选题可组合。无效答案返回 400 并保留待回答请求；过期/重复提交返回 409。`approved:false` 表示跳过，不会伪造答案。答案作为工具结果回传模型；无交互通道的入口不会直接返回提问成功。

`access_mode` 为 `ask`（确认危险工具）或 `full`（自动批准普通工具）；仅在当前服务进程的会话内保留，活跃任务返回 409。不会解决已经等待的批准，MCP 独立审批及工作区边界仍有效。

工具被拒绝时，`tool_details.approval` 为 `declined`（流及历史均保留）；`is_error` 仍为 false，以避免将用户拒绝当作系统故障重试。

Agent 流新增 `thinking_delta`（`text_delta` 携带思考片段、`timestamp` 为毫秒）、`thinking_end`（`duration_ms`）和 `context_usage`（同名对象字段）。历史消息包括 `thinking_duration_ms`。只有上游返回的思考片段才产生思考事件。

`context_usage` 包含 `estimated_tokens`、`messages`、`system`、`tools`、`context_window`、`window_known`、`model` 及 `last_request`。前三类相加得到当前估算占用，图片未计入；`window_known:false` 表示默认运行预算。`last_request` 为最近一次请求的实际 `input_tokens` / `output_tokens`，可选 `cached_input_tokens` 区分未报告与零命中，不累加多轮用量。

`context_usage.tool_discovery` 是可选字段，包含 `mode`（当前请求的有效模式）、`catalog_revision`、`resident`（含搜索入口）、`loaded`、`searchable`（尚未加载的候选数）、`definition_tokens`（与 `tools` 相同的文本估算）以及可选 `fallback_reason`。`resident + loaded` 等于该请求的定义数；不会把宿主完整注册表大小当成模型输入。旧客户端可忽略新增字段。

### 工具加载设置

`GET /settings/tool-discovery` 读取当前运行主机策略；`POST /settings/tool-discovery` 接收 `{"mode":"local"}` 或 `{"mode":"off"}`，成功后返回相同结构：

```json
{"mode":"local","revision":2,"effective_from":"next_run"}
```

沿用 API 认证。POST 拒绝未知字段、多个 JSON 值、空/非法模式和超过 1 KiB 的请求。写入失败返回 500，内存策略保持原值；成功后下一次任务采用新策略，活跃任务保持原快照。策略版本是当前进程的修订号，不作为跨重启的永久 ID。旧服务端没有此接口时，客户端提示更新主机。

`search_tools` 沿用现有 `tool_start` / `tool_end` 事件。只有结果成功提交后才发送其完成事件；成功选择工具时 `tool_details` 含 `kind: "tool_discovery"`、`version: 1`、`catalog_revision`、`selected: [{name, definition_hash}]`。浏览、零命中、失败结果没有加载事实。下一请求使用当前目录的完整 schema；加载记录不提供额外执行权限。

### 切换模型

```
POST /sessions/{id}/model
```

```json
{
  "model": "your-model-id",
  "provider": "openai"
}
```

---

### 历史全文搜索

全文搜索使用 `q`（1–256 个 Unicode 字符，忽略大小写的连续文本匹配），可选 `workspace`（完整项目路径）、`archived=all|true|false`（默认全部）、`since`（会话最近活动时间下限，Unix 毫秒）、`limit`（默认 30，最多 100）、`offset`（默认 0，最多 100000）。响应 `{hits, total, unavailable_sessions}` 中，每条 hit 对应一个标题或一条原始消息，包含 `session_id`、`title`、`workspace`、`archived`、`updated_at`、`entry_id`、`role`、`timestamp`、`snippet` 和片段内按 Unicode 字符计算的 `match_start` / `match_end`；标题命中的 `entry_id` 为空。标题优先，其余按会话最近活动、消息时间排序。`total` 是匹配条目数，不是会话数。

搜索不调用模型，不索引思考过程、工具日志或图片；用户消息有 `display_text` 时索引该可见文本。索引位于主机数据目录的 `session-search/index-v1.sqlite`，权限限制在当前用户；第一次搜索建立 SQLite FTS5 trigram 索引，之后检查文件大小和修改时间，只重建变化的会话。1–2 字符的查询扫描已缓存正文。会话重命名、归档和删除在下一次搜索时同步；损坏或正在写入的不完整记录会排除该会话并增加 `unavailable_sessions`，下次搜索重试。索引可在停止服务后删除该目录重建，原始会话仍以 `sessions/*/session.jsonl` 为准。

原始历史接口返回 `{entry_id, messages}`，每条消息包含 `entry_id`、`role`、`timestamp`、`text`。目标消息保留全文，相邻消息最多展示 4000 字，截断时带 `truncated=true`。找不到该会话中的指定消息返回 404。两条接口均使用现有 Bearer 认证，响应设置 `Cache-Control: no-store`。

## 模型与工具

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/models` | 列出可用模型；已知上下文窗口的模型带 `context_window`（token），未知模型不带该字段 |
| `GET` | `/sessions/{id}/background-tasks` | 列出会话的后台 bash 任务（ID、命令、状态、是否运行、开始/结束时间） |
| `GET` | `/sessions/{id}/background-tasks/{task}` | 查看任务最近的输出（`?tail=` 字节数，最多 64KB）；不影响模型 `bash_output` 的读取位置 |
| `POST` | `/sessions/{id}/background-tasks/{task}/stop` | 停止任务及其进程组 |
| `GET` | `/sessions/{id}/loop` | 查看会话的 `/loop` 循环任务（内容、间隔秒数、开始/到期/上次执行时间），没有时为 `null` |
| `DELETE` | `/sessions/{id}/loop` | 停止会话的循环任务；与 `/loop clear` 不同，运行中也可以停止 |
| `GET` | `/tools` | 列出已注册工具 |
| `POST` | `/tools/register` | 注册外部工具 |
| `GET` | `/applications` | 列出可用应用 |
| `GET` | `/commands` | 共享 Slash 命令和子命令目录 |

---

## 知识库

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/kb/stats` | 知识库统计 |
| `GET` | `/kb/entries` | 列出条目 |
| `GET` | `/kb/categories` | 分类列表 |
| `GET` | `/kb/tags` | 标签列表 |
| `GET` | `/kb/health` | 健康报告 |
| `GET` | `/kb/read` | 读取条目内容 |

---

## 用户画像

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/profile` | 获取用户画像（所有分类+摘要） |
| `DELETE` | `/profile` | 删除指定画像条目 |

---

## 用量与电脑控制

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/usage/summary` | Token 用量汇总（默认近 30 天，`?since=`/`?until=` 为 RFC3339），返回总计与按会话排行 |
| `GET` | `/computer/settings` | 电脑控制开关、审批策略、可用性、系统权限与占用状态 |
| `POST` | `/computer/settings` | 更新电脑控制配置：`enabled`、`approval_policy`（`ask`/`auto`）、`clear_approved_apps` |
| `POST` | `/computer/permissions` | 在运行主机上请求辅助功能与屏幕录制等系统权限 |

`usage.summary` / `GET /usage/summary` 的总量包含子 agent；`total.subagent_usage` 和 `sessions[].subagent_usage` 是其中的子任务小计（输入、输出、缓存 token）。执行收据同时保存 `usage` 总量与 `subagent_usage` 小计；上下文接口的 `subagent_usage` 仅作独立用量展示，不计入 `estimated_tokens`。

技能市场端点（`/skills/market/*`，含安装任务与 SSE 进度）见 [技能市场](SKILL_MARKET.md)，受 `EA_SKILL_MARKET` 开关约束。

---

## 文件操作

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/workspace/list-dir` | 列出工作目录内容 |
| `GET` | `/workspace/search-files` | 模糊搜索文件 |
| `GET` | `/workspace/read-file` | 读取文件内容 |
| `GET` | `/workspace/read-file-base64` | 以 base64 读取文件 |
| `PUT` | `/workspace/write-file` | 写入文件内容 |


---

## Web 控制台

serve 模式内嵌网页控制台（浏览器打开 `http://<host>:<port>/`），无需独立前端：

- **对话**：原有聊天界面（WebSocket 流式 + 会话侧栏 + 模型切换）。
- **设置**：工具加载方式、托管飞书配置、配对、服务状态与 MCP 工具配置。
- **会话**：会话列表、消息回看（含工具调用）、删除。

需要令牌时（非本机访问或已配置服务令牌），页面会弹出登录框，令牌保存在浏览器 localStorage；WebSocket 连接自动附带 `?token=`。

---

## 健康检查

```
GET /health
```

```json
{
  "status": "ok"
}
```


## Slash 命令

`GET /commands` 返回 `commands` 数组，各项包含 `name`、`description`、可选 `subcommands`。`POST /sessions/{id}/command` 的 body 为 `{"command":"/help"}`。响应包含 `output`、`should_query`、可选 `query_prompt` 和切换后的 `session_id`；若 `should_query` 为 true，客户端还需通过对话接口提交一次 `query_prompt`，不能把命令解析成功当成任务已经执行。

## MCP 管理

沿用 API 认证，服务与工具名须 URL 编码。`workspace` 查询参数选择服务主机上的工作区，空值使用默认工作区；`scope` 为 `user` 或 `project`。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/mcp?workspace=...` | 状态、配置来源、工具、授权与错误，不返回秘密；服务的 `always_load` 表示是否始终加载定义 |
| PUT / DELETE | `/mcp/servers/{name}?workspace=...&scope=user` | 保存字段补丁 / 删除该作用域定义 |
| POST | `/mcp/servers/{name}/{action}` | enable、disable、trust、untrust、reconnect、refresh、login、logout |
| POST | `/mcp/servers/{name}/tools/{tool}` | body 为 `{"enabled":true}` |
| PUT | `/mcp/project-trust` | body 为 `{"workspace":"...","trusted":true}` |
| POST | `/mcp/servers/{name}/login` | body 为 `{"redirect_url":"..."}`，返回授权地址、state 和到期时间 |
| POST | `/mcp/servers/{name}/login/complete` | body 为 `{"state":"...","code":"..."}` |

未授权的项目配置编辑返回 403，活动任务期间修改返回 409。配置保存省略秘密字段会保留旧值，显式空对象或 null 清除。传输格式与登录说明见 [MCP](MCP.md)。

## 飞书管理与语音

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/settings/feishu` | 托管配置与服务状态，或本机 `local_mode` / `bridge` 状态，不返回 Secret |
| PUT | `/settings/feishu` | 保存 `app_id` / `app_secret` 并重启桥接；空 Secret 保留旧值 |
| GET | `/settings/feishu/pairing` | 获取当前可用的私聊配对信息；响应禁止缓存 |
| POST | `/settings/feishu/qr/begin` | 非托管主机开始扫码注册，返回授权 URL、设备码及轮询间隔 |
| POST | `/settings/feishu/qr/poll` | 使用 `device_code` 轮询注册结果，不自动保存凭据 |
| POST | `/settings/feishu/qr/confirm` | 使用当前 `device_code` 确认保存已完成注册的凭据 |
| POST | `/settings/feishu/bridge/restart` | 非托管主机启动/重启进程内桥接；systemd 托管主机返回 409 |
| POST | `/asr/transcribe` | 上传音频并转写，需配置语音服务 |

飞书管理接口要求服务令牌（`EA_SERVER_API_KEY`，兼容 `EA_API_KEY`）；systemd 托管模式还需要托管文件路径，非托管模式使用已保存本机凭据。扫码注册不用于托管主机。使用前参见 [飞书接入](FEISHU.md)。

## 部署控制

`GET /admin/deploy` 返回 `active` 和 `draining`。`POST /admin/deploy` 仅在无活动任务时返回 `lease` 与 `expires_at`，忙碌时返回 409；租约期间新执行请求返回 503。`DELETE /admin/deploy` 的 body 为 `{"lease":"原租约"}`，释放该租约。三个操作均要求显式服务令牌认证，详见 [部署保护](MINI_DEPLOY.md)。

### 检查当前上下文

`GET /sessions/{id}/context` 遵循现有 Bearer 认证。响应包含 `model`、`system`、`messages`（每项为 `{role, message}`）、`tools`、`usage` 和 `compactions`。这是当前分支的逻辑模型输入，尚未执行供应商格式转换，也不包含未发送草稿与待发消息；并非上一轮实际 HTTP 请求。`usage` 计入消息、系统和工具定义的文本估算，不计图片等多模态成本。

`compactions` 按时间顺序包含 `id`、毫秒 `timestamp`、完整 `summary` 和可选 `info`。新记录的 `info` 包含 `trigger`（`manual` / `automatic`）、可选 `instructions`、`messages_before/after` 与 `tokens_before/after`（仅消息估算）。历史记录可能没有 `info`。微压缩将旧工具输出的清理结果持久化为当前分支上的独立记录，后续请求及重启恢复会沿用；原始工具输出保持不变，聊天历史仍可查看。其数量和估算变化通过 `micro_compacted` 事件传递，不作为全量摘要记录。

运行中或有其他会话修改操作时返回 `409`，避免展示不一致的快照。响应禁止缓存。`POST /sessions/{id}/compact` 接收 `{ "custom_instructions": "保留接口约定与未完成测试" }`，空要求使用默认摘要策略。压缩生成或持久化失败不替换原上下文；成功后重连和重启保留摘要及近期完整消息。

### 收件箱与成果

- `GET /inbox`：返回当前服务主机上显式保存的记录（按时间倒序）。收件箱与会话历史分开存放于 `EA_DATA_DIR/inbox`。
- `POST /inbox`：`{"id":"32位小写十六进制随机值","text":"想法或链接","source":"desktop"}`。文本 1–65536 字节，来源最多 2048 字节。同一 ID 和内容重试返回原记录；同一 ID 不同内容返回 409。成功后才可清空客户端草稿。接口沿用服务认证。
- `POST /sessions/{id}/artifacts`：`{"path":"notes/方案.md","content":"# 方案"}`。在会话工作区新建 Markdown，正文最多 1 MiB；路径必须为相对路径，限制符号链接越界。同名文件返回 409，不覆盖。后续编辑复用 workspace 文件接口。
- `inputs.files` 可增加 `"scope":"knowledge"`，路径按服务配置的知识库根目录解析，仅支持 Markdown。`workspace` 仍需与当前会话一致。省略 scope 时保持原有工作区引用行为；未知 scope 拒绝。引用内容发送时读取，标记为不可信上下文；不改变 Agent 的文件写入范围。

Coding Agent 的 `capture_note` 工具可将用户明确要求保存的文本或链接写入同一收件箱，仍经过工具白名单/黑名单过滤。手机、飞书及语音使用此 Agent 时可以请求保存；成功回执表示记录已经持久化，不依赖语音会话历史。
