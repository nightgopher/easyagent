# ADR: 运行中插话通道（steering channel）

状态：首版实现
日期：2026-11-11

## 问题

会话有任务在跑时，用户的新消息只能被拒绝：`AgentSession` 的 prompt 路径一律返回
`ErrAgentBusy`，server 准入层对 busy 会话返回 409 `{"code":"busy"}`，飞书第二条消息
在卡片上报「处理出错」，TUI 拒收输入。而核心层的 steering 机制
（`Agent.Steer` / `steeringQueue`，循环在 tool/model 边界取走消息）早已存在并有测试
覆盖，只是 session → server → UI 的管道没有接出来。这是 personal agent「跟手」体验
（随时插话、句句有回响）的关键缺口。

## 首版决策

1. **SDK**：`AgentSession.Steer(ctx, msg)`（`sdk/runtime/agent_session.go`）。
   只在 `agent.State() == StateRunning` 时委托给 `Agent.Steer`（RLock 下捕获 agent
   指针，运行中不会被 rebuild 换掉）；空闲返回新错误 `agent.ErrAgentNotRunning`，
   让调用方决定回退为普通 Prompt。不走 `busyLocked`/工具租约——插话恰恰发生在 busy 时。
2. **Server**：`POST /sessions/{id}/steer`（`internal/server/chat_handlers.go`）。
   刻意不走 `startChannelRun`（那是 busy 拒绝路径），直接 `SessionStore().Get` +
   `sess.Steer`。空闲返回 409 `{"code":"not_running"}`，未知会话 404。
   不做 request_id 收据/去重：插话不产生 run，幂等语义由调用方（飞书消息去重）保证。
3. **飞书**：`streamChat` 收到 409 `code=busy` 返回哨兵 `errAgentBusy`；
   `streamChatWithSteer` 先调 steer，成功则以「已插话」文案收尾当前卡片；
   收到 `not_running`（任务恰好结束的竞态）按普通消息重投一次。
   插话的「回响」自然出现在原任务卡片的后续流式输出里（同一 run）。
4. **TUI**：busy 时 Enter 把普通文本转 `session.Steer`，消息立刻上屏为 user message；
   `ErrAgentNotRunning` 竞态回退 `sendMessage`。斜杠命令与 scheduled loop 保持原有
   拒绝行为（它们是整任务投递，不是插话）。

### 复用的既有机制（零核心改动）

- steering 队列与边界 drain：`sdk/agent/loop.go`（含尾部补偿：最终回复流式期间到达的
  插话也会获得一轮，而不是留给下次 prompt）。
- 插话持久化：drain 后按普通 user message 走 `AppendMessage` 落盘。

## 已知限制

- **队列在内存**：插话入队成功后若进程退出，该消息不落盘（与普通 prompt 的持久化时点
  不同）。恢复要靠调用方重投；飞书侧消息本身有飞书服务端重投语义兜底。
- **尾窗竞态**：server 判定 Running 之后、enqueue 之前循环恰好退出的窗口极小
  （循环退出前最后会再 drain 一次）；残余情况下消息留在队列，并入下一次 prompt 的
  run，不会丢失。UI 收到的「已插话」回执在这种边界下可能延迟兑现。
- **不做排队**：插话只投递给正在运行的 run；没有运行中的任务时由调用方回退为普通
  消息（飞书重投 / TUI sendMessage）。已落盘的 message queue（`message_queue.go`）
  是「空闲后开新任务」语义，与插话互补，不混用。

## 后续方向

- send_message 工具（agent 主动向用户播报），补齐「跟手」的出方向。
- 若需要插话在 UI 上有显式标记（「用户插话已注入」），可增加 `EventSteered` 事件并
  透传到 SSE；首版以 HTTP 200 + 原卡片后续输出作为回执，保持事件面最小。
