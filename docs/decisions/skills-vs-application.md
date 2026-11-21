# Skills 与 Application 的边界

两者解决不同层面的问题。

- **Skill**：为已有 Agent 提供任务方法、提示、参考文件和脚本，复用当前会话与工具权限。适用于规范、流程和领域知识。
- **Application**：需要独立工具集、系统提示或会话扩展状态时，使用 `runtime.Application` 的 `BuildTools`、`BuildPrompt`、`NewSessionExt`。当前入口注册 coding、kb。
- **Workflow**：供编程 Agent 动态组织多角色会话，按中间结果分支、循环或并行；工具权限继承父会话。旧版 2026-10-17 下线后，本次重新接入动态工作流。

先复用已有工具与 Skill；只有独立运行状态和能力边界足以支撑时才增加 Application。只是绑定特定工具和说明的领域助手，用业务 Agent 定义配合 MCP、CLI 或 Skill，取舍见[下线音乐应用](remove-music-application.md)。无论入口如何变化，文件策略、API 认证和确认回调应保持明确。接口位置见 [架构](../ARCHITECTURE.md)，工作流取舍见 [工作流设计](workflow-engine.md)。
