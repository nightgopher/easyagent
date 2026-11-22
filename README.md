# EasyAgent

**Go 驱动的个人 AI 编程助手与 Agent 工作台 — 覆盖原生桌面、Android、Web、终端 TUI 与飞书。**

[English](README_EN.md) · 官方网站：[Easy Suite](https://nightgopher.github.io/easy-suite/) · 在线文档：[文档中心](https://nightgopher.github.io/easy-suite/docs/) · 仓库文档：[docs/](docs/)

在对话中阅读与修改代码、按步审批工具执行，并把每段任务保留为可继续的历史会话。日常在桌面或终端工作区使用，离开电脑也能通过 Android 手机或已配对的飞书账号远程指挥；底层能力通过公共 Go SDK 复用。

![EasyAgent 原生桌面客户端](https://nightgopher.github.io/easy-suite/screenshots/easyagent-desktop.webp)

## 核心能力

- **原生桌面客户端**：Go / MyGo 原生界面，支持本机与多台远程主机切换、Slash 命令、工具审批，连接凭据由系统钥匙串保存。
- **Android 手机端**：Kotlin / Jetpack Compose 原生客户端，保存并切换多个远程主机，支持流式对话、工具审批与消息排队，见 [手机端说明](mobile-native/README.md)。
- **Web 工作区**：浏览器访问完整工作台，支持流式对话、模型切换、工具结果展开、历史会话与飞书 / MCP 设置，断线恢复且保留草稿。
- **终端 TUI**：全屏界面、Markdown 渲染、工具分组折叠、鼠标选区复制，支持 `/loop` 循环任务与会话搜索。
- **飞书对话与交付**：长连接接收消息，卡片随生成过程实时刷新；首次配对鉴权，支持主动播报（`send_message`）与文件直达聊天（`send_file`）。
- **MCP 与技能市场**：官方 Go SDK 传输（stdio / Streamable HTTP / SSE / OAuth），内置技能市场搜索与安装（`~/.agents/skills`）。
- **可复用 Go SDK**：Provider、工具系统、会话持久化、上下文压缩、Skills、本地/SSH 执行与 `AgentSession`。

## 快速安装

### 一键安装命令行（macOS / Linux）

```bash
curl -fsSL https://raw.githubusercontent.com/nightgopher/easyagent/main/scripts/install.sh | bash
```

### 桌面端与手机端

前往 [GitHub Releases](https://github.com/nightgopher/easyagent/releases/latest) 或 [Easy Suite 下载页](https://nightgopher.github.io/easy-suite/download/) 获取安装包：

| 平台 | 安装产物 | 说明 |
|---|---|---|
| **macOS (Apple Silicon)** | `easyagent-native-darwin-arm64.dmg` | 拖入 Applications；可在「设置 → 通用 → 命令行」一键安装 `easyagent` |
| **Linux 桌面** | `easyagent-native-linux-<架构>.deb` | 或使用 `tar.gz` + 用户级安装脚本（支持应用内自动更新） |
| **Android (8.0+)** | [`easyagent-android.apk`](https://github.com/nightgopher/easyagent/releases/latest/download/easyagent-android.apk) | 备用包：[`easyagent-android.zip`](https://github.com/nightgopher/easyagent/releases/latest/download/easyagent-android.zip) |
| **Debian / Ubuntu 服务端** | `easyagent-server_<版本>-1_<架构>.deb` | 自动注册 `systemd`（`127.0.0.1:8080`）并生成 `/etc/easyagent/easyagent.env` |

## 配置模型

### 方式一：飞书登录 EasyCode（免配密钥）

- **桌面端**：打开「设置 → 模型连接 → EasyCode 飞书登录」，完成浏览器授权即可自动启用账号可用模型。
- **命令行**：运行 `easyagent easycode login`（远程 SSH 加 `--no-browser`），用 `easyagent easycode models` / `use <模型ID>` 切换。

### 方式二：直连模型或接入 EasyGateway

环境变量统一使用 `EA_*` 前缀。若配合 [EasyGateway](https://github.com/nightgopher/easygateway) 使用，只需将 `EA_BASE_URL` 指向网关地址（默认 `http://localhost:4001`）：

```dotenv
EA_PROVIDER=openai
EA_BASE_URL=http://localhost:4001
EA_MODEL=your-model-id
EA_API_KEY=your-api-key
```

可用 `EA_SERVER_API_KEY` 单独设置 HTTP API 的 Bearer 令牌（未设置时默认使用 `EA_API_KEY`）。更多 Provider、工具权限与路径策略见 [配置说明](docs/CONFIG.md)。

## 常用命令

```bash
easyagent                         # 交互式 TUI 对话（按步询问工具审批）
easyagent -y                      # 完全权限模式：自动批准 + 解除路径限制
easyagent run -p "分析这个项目"    # 执行单次任务并退出
easyagent serve --listen 127.0.0.1:8080   # 启动 Web 工作区与 HTTP API
easyagent update                  # 校验 SHA-256 并原子升级到最新 Release
```

### TUI 常用快捷键

| 按键 / 命令 | 作用 |
|---|---|
| `Enter` / `Ctrl+J` | 发送消息 / 换行 |
| `Ctrl+P` 或 `/models` | 打开可搜索模型选择器 |
| `Ctrl+G` 或 `/sessions` | 打开会话选择器与历史搜索 |
| `Ctrl+R` | 搜索当前会话输入历史并回填 |
| `Ctrl+O` / `Ctrl+T` | 展开最近工具组 / 进入工具浏览 |
| `F2` / `F3` | 复制最近回复 / 复制完整对话与工具结果 |
| `/loop 5m <任务>` | 在当前会话创建定时循环任务（`/loop clear` 停止） |

## 从源码构建

需要 Go（具体版本见 [go.mod](go.mod)）：

```bash
git clone https://github.com/nightgopher/easyagent.git
cd easyagent
make build
cp .env.example .env   # 填写 Provider、模型与密钥
./bin/easyagent
```

## 文档

| 需要做什么 | 文档 |
|---|---|
| 配置模型、权限、路径与认证 | [配置说明](docs/CONFIG.md) |
| 连接 Mac / 迷你主机、构建桌面应用 | [桌面客户端](docs/DESKTOP.md) |
| 用 Android 手机连接远程 Agent | [Android 手机端](mobile-native/README.md) |
| 扫码远程访问与中继设计 | [扫码远程访问](docs/RELAY_ACCESS_DESIGN.md) |
| 配置外部工具、项目授权与 OAuth | [MCP](docs/MCP.md) |
| 浏览与安装技能市场技能 | [技能市场](docs/SKILL_MARKET.md) |
| 接入飞书、配对与排错 | [飞书](docs/FEISHU.md) |
| 开机启动、同步 main、健康检查与回滚 | [部署运维](docs/MINI_DEPLOY.md) |
| 远程部署的隔离与 HTTPS | [安全模型](docs/SECURITY_MODEL.md) |
| 接入 HTTP / SSE / WebSocket | [API](docs/API.md) |
| 理解源码与 SDK 边界 | [架构](docs/ARCHITECTURE.md) |
| 开发、测试、合并与发布 | [贡献指南](docs/CONTRIBUTING.md) · [版本与发布](docs/RELEASING.md) · [更新记录](CHANGELOG.md) |

完整文档索引见 [docs/README.md](docs/README.md) 或访问 [Easy Suite 文档中心](https://nightgopher.github.io/easy-suite/docs/)。

## 许可与开源组件

项目代码使用 [MIT License](LICENSE)。第三方代码的来源与许可保留在各自目录，修改和发布时须保留对应许可及 NOTICE。
