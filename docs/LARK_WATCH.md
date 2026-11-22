# lark 看门狗（easyagent watch）

飞书机器人长连接只能收到「发给机器人」的消息；**别人发给用户本人的私聊、审批待办**没有任何推送通道。看门狗补上这块：用 lark-cli 的用户身份廉价轮询主人的单聊，发现新消息才唤醒 EasyAgent 会话，让管家按技能规则处理（能直接回的直接回，拿不准的先请示），并把结果以机器人身份转达给主人。

设计原则沿用「没事不叫醒 Agent」：轮询本身不调模型（每轮只是几次 lark-cli 读），模型只在真有新消息时醒来一次。

## 与桥接的分工

| 通道 | 机制 | 覆盖 |
| --- | --- | --- |
| easyagent-bridge | 飞书长连接推送 | 用户↔机器人对话、机器人所在群、卡片回调 |
| easyagent watch | lark-cli 用户身份轮询 | 用户本人的单聊新消息（v1 不含群与审批） |

两者互不替代：桥接管「用户找管家」，看门狗管「管家管用户的收件箱」。

## 工作方式

1. 启动时用 `contact +get-user` 识别主人身份，`auth status` 识别机器人（用于找到「主人↔机器人」转达会话），然后**把现有消息记为基线**——首跑不触发任何任务。
2. 每个轮询周期：列出主人单聊（机器人会话与显式排除的会话不巡查），逐会话读最新几条，按会话内单调递增的 `message_position` 与上次位点比较。
3. 有新消息（排除主人自己发的与已删除的）才托付一次任务：`POST /feishu/relay`（带稳定 request_id，同一批消息重试不会重复执行）。核心进程内的飞书桥接以 `larkwatch` 渠道运行任务，由**扫码绑定的机器人**在主人平时对话的私聊里发流式卡片：青色标题栏「🐕 看门狗 · N 条新消息」，正文先列出谁说了什么，再是管家的处理过程与结果。审批卡片、`send_message` 播报和卡片按钮都走桥接现成链路，主人可以直接在同一个对话框里回复、接着聊。核心受理即返回，卡片在后台更新到任务结束；会话忙时由桥接改走插话。
4. 桥接不在核心进程内（如 systemd 托管的独立 `easyagent-bridge`，或核心版本不支持）时，`/feishu/relay` 返回 503/404，看门狗退回旧方式：`POST /chat` 注入（忙时 `POST /sessions/{id}/steer` 插话），最终文本由 lark-cli 应用的机器人以纯文本发到「主人↔机器人」会话，超长截断。
5. 托付或注入失败不推进位点，下一轮自动重试同样消息；旧方式下 409 request_conflict 视为已处理，正常推进。

## 安全边界

- 看门狗读取的是**主人授权给 lark-cli 的用户身份**数据（详见 lark-cli 的授权范围），只读消息列表与消息内容，外加两类写入：唤醒自家会话、以机器人身份给主人转达。**不会**以用户身份对外发送任何内容——代发只能由被唤醒的 agent 按其权限规则执行（建议配合管家铁律技能与「每次询问」级审批）。
- 机器人会话（含转达会话）恒不巡查，主人自己的消息不触发唤醒——看门狗不会把自己发的摘要当成新输入造成循环。
- 位点状态文件默认 `0600`，写在用户配置目录。

## 配置

环境变量（也可用同名 CLI flag，见 `easyagent watch --help`）：

| 变量 | 说明 | 默认 |
| --- | --- | --- |
| `EA_LARKWATCH_CORE_URL` | 核心 HTTP 地址（回退 `PI_AGENT_URL`、`http://127.0.0.1:8080`） | 同左 |
| `EA_SERVER_API_KEY` | 核心 Bearer 令牌（回退 `EA_API_KEY`） | 无 |
| `EA_LARKWATCH_SESSION_ID` | 要唤醒的会话 ID | **必填** |
| `EA_LARKWATCH_INTERVAL` | 轮询间隔（下限 10s） | `30s` |
| `EA_LARKWATCH_RUN_TIMEOUT` | 单轮巡查超时 | `10m` |
| `EA_LARKWATCH_STATE_FILE` | 位点状态文件 | `~/.config/EasyAgent/larkwatch-state.json` |
| `EA_LARKWATCH_EXCLUDE_CHATS` | 不巡查的 chat_id，逗号分隔 | 无 |
| `EA_LARKWATCH_MAX_PER_WAKE` | 单次唤醒携带消息上限 | `5` |
| `EA_LARKWATCH_LARK_CLI` | lark-cli 可执行文件路径 | `lark-cli` |

前提：本机已安装 lark-cli 并完成用户身份登录（`lark-cli auth login`），会话 ID 可在核心数据目录的 `feishu-routes.json` 里查到（owner open_id → session_id）。

## 部署

### 在 CLI 设置并通过 systemd 常驻

在交互式 CLI 输入 `/watch`，或进入 `/config` →「飞书看门狗」，即可查看状态、设置核心地址、会话 ID 和轮询间隔，并启停独立的 `easyagent-cli-watch.service` 用户服务。会话 ID 留空时尝试从当前 `EA_HOME` 下的飞书路由自动发现；多个候选时需明确填写。服务使用创建时的 EasyAgent 可执行文件和 PATH，升级或移动二进制后应重新启动服务。重新保存设置后需要在面板中再次启动以应用。

此功能需要 Linux 的 systemd 用户服务、已安装并完成用户授权的 `lark-cli`，以及**独立运行的核心服务**。看门狗不会替你启动核心；核心也需要由服务管理器持续运行。CLI 配置保存在 `EA_HOME/cli-watch.json`，不与桌面端托管的看门狗共享开关。面板仅管理自己创建的同名用户服务，不覆盖其他服务。macOS 目前请使用桌面端的飞书设置或自行托管 `easyagent watch serve`。

CLI 用户服务的巡查位点保存在 `EA_HOME/cli-watch-state.json`。如果桌面与 CLI 同时巡查同一个收件箱，它们可能各自处理同一条消息，应只保留一个巡查实例。

如果核心要求认证，在 `EA_HOME/cli-watch.env` 中配置 `EA_SERVER_API_KEY=...`，并将文件权限设为 `0600`；此文件由用户自行创建，不在面板中展示令牌。进程环境、该环境文件中的看门狗变量优先于面板保存的配置。

SSH 断开后，systemd 用户服务能否继续运行取决于用户管理器是否保持运行；如需退出登录后及开机后继续运行，可由有权限的管理员执行 `loginctl enable-linger <用户名>`。这不会自动启动核心服务，也不能保证飞书授权长期有效。可用 `systemctl --user status easyagent-cli-watch.service` 和 `journalctl --user -u easyagent-cli-watch.service` 排查状态。

```bash
# 前台试跑一轮（首跑只建基线，安全）：
easyagent watch once --session sess_xxx --core-url http://127.0.0.1:35157

# 常驻（systemd 用户服务示例，~/.config/systemd/user/easyagent-watch.service）：
#   [Service]
#   ExecStart=%h/.local/bin/easyagent watch serve
#   Environment=EA_LARKWATCH_CORE_URL=http://127.0.0.1:35157
#   Environment=EA_LARKWATCH_SESSION_ID=sess_xxx
#   Environment=EA_SERVER_API_KEY=...
#   Restart=on-failure 之外沿用 easyagent-core 的 linger 配置
systemctl --user enable --now easyagent-watch.service
```

系统级部署（deb 服务器形态）参考 `deploy/systemd/easyagent-watch.service`。

## 现有限制

- v1 只巡查**单聊**；群消息让机器人进群走 `im:message.group_msg` 推送（毫秒级），不需要轮询。
- 审批待办、妙记产出等尚未纳入巡查源；后续可作为新的 source 接入同一框架。
- 看门狗进程随核心重启无需特殊处理：位点在文件里，重启后继续；核心不可达时报错重试，不丢消息。
