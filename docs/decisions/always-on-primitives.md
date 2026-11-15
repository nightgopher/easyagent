# 常驻 Agent 原语：收件箱、定时器、记忆、权限与发件箱

状态：设计提案，未实施。2026-11-15。

参考 [Building always-on agents](https://claude.com/resources/articles/building-always-on-agents)（Anthropic，2026-11-13）。

本文只做取舍与分阶段计划，不改代码。现状事实以 `95b4456e` 的源码为准。一处事实只在一个文档维护：插话通道见 [steering-channel](steering-channel.md)，指挥台见 [personal-command-center](personal-command-center.md)，协议见 [API](../API.md)，执行细节见 [INTERNALS](../INTERNALS.md)。

## 1. 结论

EasyAgent 现在是一条**被动的、但已经"跟手"的管路**：用户发消息 → 准入 → run → 流式回给当前连接的客户端；任务在跑时还能插话（`POST /sessions/{id}/steer`）。文章描述的常驻 Agent 缺的不是核心循环，而是**"睡眠中醒来"的三件事**：

1. **定时器不持久**：`/loop` 只在内存里，重启即停。这是"常驻"最硬的一处缺口。
2. **收件箱只有一半**：`queue.json` 由客户端显式写入；会话忙时新事件直接 409 丢弃，连 `/loop` 的 tick 也会静默丢失。
3. **发件箱只有投递点、没有队列**：`send_message` 已经能给用户主动播报（飞书独立消息、TUI 独立气泡），但投递跟当次 run 的活连接走，失败只记日志、离线即丢，也没有节奏控制。

建议顺序：**持久化定时器 → 通用事件收件箱 → 每次 run 的进度记忆 → 可恢复的确认（deferred tool call）→ 发件箱与播报节奏**。前三项能把"常驻"从"进程活着"变成"跨重启活着"，后两项才谈得上无人值守。

## 2. 文章要点

常驻 Agent 并不真的"常开"：它大部分时间在睡觉，被事件叫醒后跑一个 **run**，run 在 **session**（消息与工具调用的流水）里工作，结束后写回状态再睡。围绕这个循环有五个原语：

| 原语 | 文章职责 | 关键约束 |
|---|---|---|
| 事件收件箱 | 所有事件源（webhook / 定时 / 用户消息）先落库再由 worker 取走 | 单个 inbox 同时只有一个 worker；事件去重；批量合并；worker 挂了能重取 |
| 定时器 | 两类：follow-up（agent 给自己设的检查点）与 schedule（已知节奏的重复任务） | 独立持久存储；调度器轮询到期项再投进收件箱，不直接跑 |
| 记忆 | 跨 run 的连续性：用户指令、用户笔记、已告知内容、任务进度 | 每轮开头只加载精简索引，正文按需取；持久存储要在 worker 之外 |
| 权限 | 无人值守下的自治：凭据代理、auto 模式、**deferred tool call**（挂起 run，等答复再恢复） | 挂起期间的答复作为新事件回到收件箱，worker 不被阻塞 |
| 发件箱 | 后台任务的产出有节奏地送达用户 | 消息表 + dispatcher；宁可先安静，再逐步争取注意力 |

## 3. 现状映射

| 原语 | 已有实现 | 落点 | 差距 |
|---|---|---|---|
| 事件收件箱 | 会话级 `queue.json`（32 条上限、revision 广播）；larkwatch 位点游标；`steer` 插话 | [message_queue.go](../../internal/server/message_queue.go)、[larkwatch/state.go](../../internal/larkwatch/state.go)、[chat_handlers.go](../../internal/server/chat_handlers.go) | 队列只由客户端写；server 从不主动入队；忙时入口一律 409；重启后队列强制 paused，不重放 |
| 定时器 | `/loop` 周期提示（每会话一个、最小 1 分钟、默认 3 天 TTL、每次 fire 5 分钟超时）；larkwatch 轮询；指挥台 autoscan | [scheduler/loop.go](../../internal/scheduler/loop.go)、[larkwatch/watch.go](../../internal/larkwatch/watch.go)、[command/autoscan.go](../../internal/command/autoscan.go) | 全部是内存 ticker；无一次性 follow-up；重启丢失；fire 撞忙即丢；无统一调度器 |
| 记忆 | `agentdata` 记忆库（SQLite，agent/workspace 作用域、版本号、过期）；`save_memory` → `~/.easyagent/AGENTS.md`；`todo_write` → `todo.json`；`work_get`/`work_report` 工作项 | [agentdata/memory.go](../../internal/agentdata/memory.go)、[tools/memory.go](../../sdk/tools/memory.go)、[tools/todo.go](../../sdk/tools/todo.go)、[agents/business/memory.go](../../internal/agents/business/memory.go) | 四处分散、作用域各不相同；每轮只自动加载 AGENTS.md/CLAUDE.md 与技能索引，没有"任务进度"这一层，`work_get` 要 agent 主动调用 |
| 权限 | 工具确认门（2 分钟超时，`ask_user_question` 不超时）、access mode、插话 | [agent/loop.go](../../sdk/agent/loop.go)、[server/run_registry.go](../../internal/server/run_registry.go)、[run_admission.go](../../internal/server/run_admission.go) | 确认是**阻塞式**的：run 在等，会话被判忙；确认不持久，重启把 `waiting_confirmation` 改成 `interrupted`；没有 auto/分类器 |
| 发件箱 | `send_message` 主动播报（飞书独立消息 / TUI 气泡）；WS fan-out（256 条 / 1 MiB 重放环）、SSE、飞书卡片、指挥台桌面通知 | [tools/send_message.go](../../sdk/tools/send_message.go)、[feishu/handler.go](../../internal/feishu/handler.go) `maybeDeliverBroadcast`、[command/notify_desktop.go](../../internal/command/notify_desktop.go) | 投递是"活连接上的 best-effort special case"：无消息表、无重试、无节奏控制、无已读水位；客户端不在就只能靠 receipt 与历史重建 |

## 4. 关键差距

### 4.1 定时器是易失的，且 tick 会丢

- `LoopManager` 只有 `map[sessionID]*loopEntry`，没有 `os` 依赖（[scheduler/loop.go](../../internal/scheduler/loop.go)）；接口注释直接写着 "live in memory until stopped, expired or the server restarts"（[server/loops.go](../../internal/server/loops.go)），CHANGELOG 也把持久化和失败退避记为"保持现状"。
- fire 时调用 `startRun(sessionID, prompt, "")`（[server.go](../../internal/server/server.go) `injectLoopPrompt`）：**requestID 为空**，既不写 receipt 也不去重；会话正忙时该 tick 只留下一条 `loop trigger failed` 日志，等下一次 tick 就跳过了一个周期。
- 只有"重复执行"，没有文章里的 follow-up（一次性检查点）；每会话只能有一个 loop，replace 即覆盖。

### 4.2 收件箱只做了一半

- 忙判断在准入层：`sessionBusy` → 409 `busy`（[run_admission.go](../../internal/server/run_admission.go)）。所有入口（`/chat`、WS prompt、飞书、`/loop`）都走这条路，因此"事件在 run 中途到达"没有缓冲层，只能失败。
- `queue.json` 是**客户端驱动的待发箱**，语义是"空闲后开新任务"（[steering-channel](steering-channel.md) 已明确与插话互补、不混用）。重启时 `q.Paused = len(q.Items) > 0 || f.Paused`——这是刻意的，"不静默重放外部动作"，但代价是没有"重放 agent 自己的待办"的通道。

### 4.3 larkwatch 已经跑通了正确模式，值得提炼

`internal/larkwatch` 是目前最接近文章形态的一块，且是小规模验证过的：

- **位点即 ack**：`State.Chats[chatID].LastPosition` 原子落盘（临时文件 + rename），只在注入成功后 `advanceAndSave`；失败**不动位点**，下一轮自然重试（[watch.go](../../internal/larkwatch/watch.go) `Poll`）。
- **批量合并**：一轮扫到的所有新消息合并成一条 prompt（`capSightings` + `buildPrompt`），超量时只带最新 N 条并注明省略数。
- **稳定去重**：`requestID(items)` 由消息坐标（`chatID:position:messageID`）排序后取哈希，重启后重投同一批消息会得到同一个 ID，被核心判为 `request_conflict`，做到跨进程的幂等。
- **忙时折叠**：注入遇 `ErrBusy` 时降级为 `Steer`，把事件并进正在跑的 run，而不是排队或丢弃。

这条链路证明"事件游标 + 批量 + 去重 + 忙时折叠"在 EasyAgent 里可行。差距是它绑在飞书看门狗上，只是一个应用，不是可复用的原语。

### 4.4 记忆四处分散，缺"任务进度"

| 存储 | 位置 | 作用域 | 每轮自动加载 |
|---|---|---|---|
| 记忆库 | `agentdata`（SQLite） | agent / workspace，带版本与过期 | 否（模型调 `memory_search`） |
| 全局记忆 | `~/.easyagent/AGENTS.md` | 全局 | 是（作为全局上下文文件） |
| 工作区上下文 | 工作区及祖先目录的 `AGENTS.md` / `CLAUDE.md` | 工作区 | 是 |
| 待办 | `todo.json` | 全局单文件 | 否（模型调 `todo_list`） |
| 工作项 | `work_items` 表 | 会话 | 否（模型调 `work_get` / `work_report`） |

自动加载的部分（[prompt/context.go](../../sdk/prompt/context.go)）只覆盖"用户长期指令"，没有文章强调的 **task progress**：一个跨多次唤醒的长任务，新 run 醒来后没有任何"上次做到哪、下一步是什么"的默认上下文，只能靠模型自己在历史里翻或主动调 `work_get`。常驻场景里这恰恰是最常走的一条路。

### 4.5 确认门是阻塞的，且不可恢复

确认在工具调用点内联等待（[agent/loop.go](../../sdk/agent/loop.go) 的 confirmation gate），server 侧 `pendingConfirmation` 等 2 分钟，`ask_user_question` 则无限等（[run_registry.go](../../internal/server/run_registry.go)）。后果：

- `waiting_confirmation` 也算 `runActive`，所以**等人**期间整个会话是忙的，其它事件全被拒。
- 等待中的 run 只存在内存里；重启时 `restoreReceipt` 把 `running` / `waiting_confirmation` 一律改成 `interrupted`（[run_receipts.go](../../internal/server/run_receipts.go)），确认请求消失，飞书卡片上的按钮点下去只会得到 `stale_run`。

文章的 deferred tool call 正好补这个洞：**挂起 run、持久化待答请求、把答复当成一条新事件再次唤醒**。这也顺带解决"等人时会话不能干别的"。

### 4.6 发件箱只有投递点，没有队列

`send_message` 的注释写得很清楚：工具只把正文放进 `ToolResult.UserFacing`，真正的投递由各 UI 在 `tool_end` 事件上按工具名特判——飞书 `SendMarkdown` 发一条独立消息，TUI 上屏一个独立气泡（[tools/send_message.go](../../sdk/tools/send_message.go)、[feishu/handler.go](../../internal/feishu/handler.go) `maybeDeliverBroadcast`）。因此：

- 投递依赖**当时还连着的流**。客户端断线、进程重启、飞书 SSE 断开，这条播报就只剩日志里一行 `send_message delivery failed`。
- 没有重试、没有已读水位、没有节奏控制：连续多次播报会原样连发，而文章恰恰强调"先安静，再逐步争取注意力"。
- run 的最终结果同样只 fan-out 给在线订阅者（[run_registry.go](../../internal/server/run_registry.go) `publishLocked`），离线场景只能靠 receipt + 会话历史重建。

也就是说"出方向"已经有了通道，缺的是一张**持久消息表 + dispatcher**：谁要发、发到哪个通道、发成功没有、用户看过没有。

## 5. 目标形态

```
事件源：用户消息 / 定时器到期 / webhook / 指挥台 / 用户对挂起任务的答复
        │  写入（带 dedupe_key）
        ▼
   收件箱 inbox ──► 每会话单 worker ──► run（session）
        ▲                                   │
        │                                   ├─► 记忆（instructions / index / progress）
        │                                   ├─► 权限（确认门 / deferred 挂起）
        │                                   └─► 会话历史（现有 JSONL）
        │
   定时器 timers（follow-up / schedule）──到期只投递，不直接起 run──┘

run 之外：定时任务产出 / 长任务完成 / 需要人
        └─► 发件箱 outbox ─► dispatcher ─► 通道（WS / 飞书 / 通知）
```

与原设计的对齐点：**定时器只负责把事件投递进收件箱，不直接启动 run**；**收件箱是唯一的事实来源**，worker 每次醒来先读库；**每会话只有一个 worker**，与现有 `sessionBusy` 的串行语义一致；**发件箱只承载播报**（`send_message` 与 run 收尾结论），run 内的流式输出仍走现有事件流。

## 6. 分阶段落地

每个阶段都可以独立 PR、独立回滚。**P0 之前的共同前提**：新增的持久层都要有"临时文件 + rename"的原子写，与 `queue.json`、`larkwatch` 状态一致。

### P0 持久化定时器（建议第一个 PR）

- 数据（单表或单文件均可，字段一致）：

  | 字段 | 说明 |
  |---|---|
  | `id` | 定时器 ID |
  | `session_id` | 目标会话 |
  | `kind` | `followup`（一次性）/ `schedule`（周期） |
  | `prompt` | 到期注入的文本 |
  | `next_fire_at` | **唯一真相**，不用"上次时间 + 间隔"推算 |
  | `interval` / `expires_at` | 仅 schedule |
  | `state` | `pending` / `fired` / `cancelled` |
  | `attempts` | 失败退避用 |
  | `dedupe_key` | 默认 `timer:<id>:<fire_at_unix>` |

- 调度：一个全局调度循环轮询到期项（可 sleep 到最近的 `next_fire_at`），到期后**投递到收件箱**（P1 之前可直接复用 `injectLoopPrompt`，但要带 `dedupe_key` 作为 requestID）。
- 恢复：`server.New` 里一次性加载（对齐 `restoreRunReceipts` 的位置与"单文件损坏跳过不致命"的处理方式）。
- 重启语义：未来的 `next_fire_at` 保留；已过期未执行的，默认**丢弃并记一条"错过 N 次"**，不自动补发（可配置）。
- `/loop` 契约不变：仍然写一个 schedule 定时器，`GET`/`DELETE /sessions/{id}/loop` 语义与现有一致，只为响应增加字段。
- 验收：重启后 schedule 继续 fire；同一 `fire_at` 重复投递只执行一次；`/loop clear` 与过期清理不留孤儿行。

### P1 通用事件收件箱

- 数据：`id / source / dedupe_key / payload / state(pending|running|done|failed|dead) / attempts / next_attempt_at / created_at`。
- 入口改造：`/chat`、WS prompt、飞书、定时器到期统一改为"写收件箱"，由 worker 串行消费；忙不再返回 409，而是落库等待（保留 409 作为"队列已满/不可恢复"的唯一失败路径）。指挥台的目标派发（当前走 GUI 自动化）后续再接同一条入口。
- 批量：一轮醒来把 pending 合并成一次 run（沿用 larkwatch 的 `buildPrompt` 思路），减少"每来一条就跑一次"。
- 重试：失败按指数退避，超上限进 `dead`，以"需要人"的形态进发件箱。
- 与插话的分工不变：插话是"并进正在跑的 run"，收件箱是"下一轮"，不互相替代。
- 验收：重启不丢事件；重复投递不重复执行；并发两路投递不产生并行 run。

### P2 每次 run 的进度记忆

- 位置：`{DataDir}/sessions/{id}/memory/`，`instructions.md`（用户对该会话的长期要求）与 `index.md` 每轮进上下文；`progress/<task>.md` 只在索引里出现一行，正文按需用 `read` 取。
- 分工：长期事实仍留在 `agentdata`（有版本与过期，适合"用户偏好"）；文件层只放"这次任务做到哪"，因为它要频繁整段改写，且人和 agent 都要读。
- 写回：run 结束（或压缩/切分支）时由 agent 自己写或由收尾钩子写，不引入第二套工作流。
- 验收：新 run 在没有任何用户消息的情况下，仅凭自动加载的内容就能回答"上次做到哪、下一步是什么"。

### P3 可恢复的确认（deferred tool call）

- 触发：工具显式声明可延后，或等待超过阈值。
- 行为：把待答请求持久化到会话目录，run 以 `waiting_input` 结束并落 receipt；会话**不再算忙**，worker 可以接别的事件。
- 恢复：用户答复作为收件箱事件，唤醒同一 session 继续（复用现有 `run/continue` 的语义边界：只重发模型请求，不重放已完成的工具）。
- auto 模式：先只做规则白名单（按工具 + 参数模式），不做 LLM 分类器。
- 验收：确认请求跨重启仍可回答；等人期间会话能继续处理无冲突的事件。

### P4 发件箱与播报节奏

- 前置：`send_message` 已经在用，P4 不改工具接口，只补投递的耐久性与节奏。
- 数据：`outbox` 表（`kind=message|ask|report`、来源 run、目标通道、状态、尝试次数、已读水位）。
- 写入：`send_message` 与"run 收尾结论"先落 outbox，再由 dispatcher 逐通道投递（飞书 / 桌面通知 / WS），失败退避重试；现有 `tool_end` 特判收敛为 dispatcher 的一个适配器。
- 节奏：默认安静，只报完成 / 失败 / 需要人，其余合并成摘要；按文章的"先安静，再争取注意力"用回放样例评估，而不是拍脑袋定频率。
- 验收：断线期间的播报在重连或下次启动后送达；同一播报不重复发；用户标记已读后不再补发。

## 7. 取舍

| 议题 | 选择 | 理由 |
|---|---|---|
| 存储形态 | 定时器与收件箱用 SQLite（`{DataDir}` 下新库，或扩展 `agentdata`）；记忆用文件 | 到期扫描与"原子 claim 一条"用 `UPDATE … WHERE state='pending'` 最省事；记忆要被人读、被 agent 整段改写，文件更合适 |
| 并发模型 | 每会话单 worker，串行 | 与 `sessionBusy`、steering 的既有假设一致；并行只会把会话写冲突提前引爆 |
| 定时器与收件箱的关系 | 定时器投递事件，不直接启动 run | 所有起点汇成一条路径，重试与去重只需要实现一次 |
| 去重键 | 事件层自带 `dedupe_key` | `request_id` 语义是"一次模型请求"，事件是"一件待办"，两者生命周期不同，不能硬套 |
| 重启后补发 | 默认不补，记录错过次数 | "不静默重放外部动作"是既有原则；定时器补发属于 agent 自己的意图，可以作为显式配置项，但默认关闭 |
| 幂等承诺 | at-least-once 投递 + 去重 → 效果上 at-most-once | 与 [workflow-engine](workflow-engine.md) 记录的"不承诺恰好执行一次"口径一致 |
| 无人值守的权限 | 恢复的定时/收件箱任务按最小权限跑，敏感工具仍然要确认（走 P3 的 deferred） | 常驻不等于放开；自动恢复不应绕过确认 |
| 客户端契约 | `GET`/`DELETE /sessions/{id}/loop` 不改语义，只加字段 | Android 与桌面已经依赖（`LoopClientTest`、`LoopDialog`） |

## 8. 不做的

- 不做多租户/多用户收件箱：当前是单用户个人 agent，先不引入隔离维度。
- 不引入 cron 依赖：`first_fire_at` + `interval` 已覆盖 follow-up 与 schedule 两类。
- 不做跨设备分布式 worker：单核心、单 worker，符合现有部署事实（迷你主机一个核心）。
- 不在首版做 LLM 分类器的 auto mode：先用规则白名单。
- 不把收件箱做成第二个会话历史：事件完成即从 pending 里消失，正文留在会话里。

## 9. 开放问题

1. follow-up 的入口先给谁：模型工具（agent 自己设检查点）还是用户命令（`/remember`）？建议先给模型工具——无人值守的价值主要来自 agent 自己判断"什么时候该回来看一眼"。
2. 定时任务在 `readonly` access mode 下是否允许 fire？建议允许 fire，但工具集降级，并在注入文本里说明当前权限。
3. 每会话的定时器上限与收件箱上限各是多少？建议定时器 8、收件箱 32（与 `queue.json` 对齐）。
4. 错过窗口的处理是否需要"合并成一条"而不是丢弃？需要真实使用数据再定。
5. `queue.json` 是保留为客户端待发箱，还是并入收件箱统一语义？建议短期保留，P1 合流时再评估。

## 10. 建议的 PR 拆分

| PR | 范围 | 相对体量 |
|---|---|---|
| 1 | P0 定时器持久化 + 恢复 + `dedupe_key`，`/loop` 契约不变 | 中 |
| 2 | tick 撞忙不再丢弃（先最小改动：落进 `queue.json` 语义之外的事件表） | 小 |
| 3 | P1 收件箱与单 worker，入口逐个切换 | 大 |
| 4 | P2 会话进度记忆 | 小 |
| 5 | P3 deferred tool call + 确认持久化 | 中 |
| 6 | P4 outbox：`send_message` 与 run 结论改为先落库再投递 | 中 |

每个 PR 的验收按上面的"验收"列写进 PR 说明；涉及重启恢复的必须有"构造两次、中间不清理目录"的回归测试，且不给测试加长 sleep（用可注入的时钟）。
