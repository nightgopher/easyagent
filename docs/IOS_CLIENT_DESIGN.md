# EasyAgent 苹果手机端设计（iOS / iPadOS）

状态：设计稿，未实现。范围是 iPhone 与 iPad 两个形态（一套 SwiftUI 代码适配两种尺寸），与 Android 客户端（mobile-native）能力对等。本文档是实现的评审基线，动工前按当时代码现状复核一遍服务端 API。

## 产品定位与上手路径

纯客户端"装了啥都干不了"——iOS 版的价值取决于主机侧的上手门槛。分两步走：

**v1（本项目）：连接到你自己的 Mac / 迷你主机。** 苹果用户大概率有一台 Mac，Mac 上的 EasyAgent 桌面端已内嵌核心，本地全套能力（文件、终端、模型）直接可用——iOS 端配对后就是"口袋里的 Mac 智能体"，比云端沙盒更强（真实在自己机器上执行）。跨网可达性推荐 Tailscale（免费、已在基础设施栈内），或反代 HTTPS。App Store 先例充分：Blink Shell、Termius 都是"客户端 + 用户自己的服务器"模式并成功上架。

**v2（可选，按需求投入）：托管会话/中继作为订阅功能。** 我们运营云端 runtime、用户自带模型 Key（BYOK），零门槛但要账号体系、计费与滥用治理——验证付费意愿后再投入。

上手路径设计（v1）：桌面端新增「远程访问」引导页，展示配对二维码（主机地址 + 一次性配对令牌），iOS 扫码即配；首次启动流程内置 Tailscale/反代指引。桌面端需要核心支持一次性配对令牌（小改动，见里程碑 M1）。

## 目标与非目标

目标：

- 与 Android 客户端对等的多主机远程客户端：保存并切换多台主机，跑在主机上的 easyagent 核心（serve）通过 HTTP/WebSocket 提供全部能力，手机端不内嵌核心。
- 流式对话、工具审批、附件、会话管理、模型切换、技能与后台任务可见性。
- 原生体验：SwiftUI、系统键盘与输入法、Keychain 凭据、iPad 分屏与键盘快捷键。

非目标（首版）：

- 不内嵌 easyagent 核心（与 Android 一致，核心永远在主机上）。
- 不做 watchOS / visionOS / Mac Catalyst。
- 不做应用内自更新：iOS 分发走 TestFlight / App Store，系统自带更新机制。
- 不承诺离线能力：无主机连接时只读历史缓存。
- v1 不做托管云服务（云端 runtime/账号体系）：留给 v2 按需求评审。

## 技术选型

Swift + SwiftUI，最低 iOS 17。

- 与组织偏好一致（各端原生：Go / Kotlin / Swift），不引入跨端运行时。
- 与 Android 的 Kotlin/Compose 不共享代码：客户端逻辑薄（主要是 API 编排与 UI），共享层（KMP）省下的代码量抵不上双端构建与调试成本。服务端 API 已由 Android 客户端验证，Swift 侧按同一契约实现即可。
- 已定稿（2026-10-25）：两端技术栈天然对称，iOS 按此映射实现——OkHttp ↔ URLSession、Kotlin 协程 ↔ Swift Concurrency（async/await + actor）、kotlinx-serialization ↔ Codable、Compose 声明式 UI ↔ SwiftUI 声明式 UI。目录边界同样对称：Android 的 `backend/`（API 编排，约 1400 行）对应 iOS 的 `Backend/`，与 `Chat/`、`Sessions/` 等 UI 目录分离，M1 骨架即按此建立。
- iPad 与 iPhone 同一 target，自适应布局：iPhone 单栏对话，iPad 会话列表 + 对话双栏（NavigationSplitView），外接键盘支持 ⌘N 新会话等快捷键。

## 与服务端的契约

完全复用 easyagent core 的 HTTP API 与 WebSocket，不新增服务端端点（首版原则：服务端零改动）。Android 客户端（mobile-native/backend/Wire.kt）是 API 用法的权威参考，iOS 侧按同一契约实现：

- 主机与认证：`GET /health` 探活；每个主机一个 API Key（`EA_SERVER_API_KEY`），Bearer 认证。
- 会话：`GET/POST /sessions`、`DELETE /sessions/{id}`、`GET /sessions/{id}/context`、`/sessions/{id}/model`、`/sessions/{id}/skills` 等。
- 对话流式：WebSocket 通道，delta 流式渲染，工具调用卡片（开始/结束/审批请求）。
- 工具审批：`POST` 审批决策端点；审批请求在流内以卡片形式出现，支持允许/拒绝。
- 附件：上传端点 + `GET /sessions/{id}/attachments/{attID}/raw`。
- 后台任务：`GET /sessions/{id}/background-tasks` 列表与详情。
- 技能：`GET /skills/local`（含 `installed_error` 错误透出）、`GET /sessions/{id}/capabilities`（`origin`/`overrides`）、`POST /sessions/{id}/skills/refresh`。

服务端侧需要注意的一个既有事实：core 监听 `0.0.0.0:8080` 明文 HTTP + API Key。iOS 的 ATS（App Transport Security）默认禁止明文 HTTP，设计上：

1. 远程主机（公网/HTTPS 反代）：正常直连。
2. 局域网/Tailscale 主机（`100.64.0.1` 等明文 HTTP）：`NSAllowsLocalNetworking` 例外 + 用户在 Info.plist 显式确认的主机白名单；不提供全局禁用 ATS 的开关。
3. Tailscale 主机可升级为 HTTPS（Tailscale 证书），作为推荐的进阶配置写进部署文档。

## 架构

```
ios/
  EasyAgent.xcodeproj
  EasyAgent/               # App target（iPhone + iPad 自适应）
    Hosts/                 # 主机管理：列表、增删改、连通性测试
    Chat/                  # 对话：流式渲染、工具卡片、审批、附件
    Sessions/              # 会话列表、搜索、归档
    Skills/                # 技能面板、后台任务列表
    Backend/
      APIClient.swift      # REST（URLSession，async/await）
      ChatSocket.swift     # WebSocket 流式（URLSessionWebSocketTask）
      HostStore.swift      # 多主机持久化（UserDefaults/文件）+ Keychain 凭据
  EasyAgentTests/
```

- 凭据只进 Keychain（kSecClassGenericPassword，per-host account）；主机地址等非敏感配置可持久化在 App 容器。
- 网络层与 Android 的 backend 包对齐：每个主机一个 APIClient 实例；连接切换时取消全部在途请求并清空会话缓存（Android 已修的重连缓存问题，iOS 侧从第一天就按此实现）。
- 流式渲染：增量 Markdown 渲染，工具调用以卡片分组显示（与桌面端工作区面板同构）。

## 安全设计

- 凭据：Keychain，永不入 UserDefaults、永不入日志。日志脱敏规则与 Android 一致（不打印 token、会话正文与参数全文）。
- 明文 HTTP 仅限用户显式添加的主机 + 本地网络/Tailscale 网段（见 ATS）；首次连接展示明文警示。
- 主机端 Owner 门禁与 API Key 认证沿用服务端既有机制，客户端不缓存任何绕过手段。
- 分发前：运行一次静态扫描（无硬编码密钥、无 ATS 全局豁免）。

## 通知与后台

- 首版不做 APNs 推送：长任务完成提醒依赖用户回到 App（服务端已有 easyagent-bridge 飞书通道兜底，飞书消息照发）。
- 后台运行限制如实告知用户：退到后台后 WebSocket 会被系统挂起，回来时重连并补拉会话增量（按 context 版本号）。
- 后续迭代可选：服务端接 APNs（需要推送证书与一个推送服务组件），作为独立设计再评审。

## 分发与更新

- TestFlight（内部 + 外部测试）→ App Store。不走 Android 的应用内自更新机制（Apple 政策要求经 App Store）。
- CI：GitHub Actions macOS runner，`xcodebuild test` + `xcodebuild archive`，用 App Store Connect API Key（.p8，只能下载一次，需在 App Store Connect → 用户和访问 → 集成 里创建）上传 TestFlight。p8 与现有 macOS/Developer ID 证书同库存放（密码管理器），不进仓库。
- 签名/描述文件：需要 iOS Distribution 证书 + App ID + Provisioning Profile（与现有 macOS 的 Developer ID 证书是不同证书，需在开发者账号里新建）。
- App 审核：AI 客户类 App 的审核要点是用户生成内容与远程模型输出——首版说明里写清内容来源与过滤责任在用户自己的模型服务。

## 里程碑

- M0（本文档）：设计评审通过，服务端确认零改动。
- M1：主机管理 + Keychain + 流式对话 + 会话列表（iPhone 优先），TestFlight 内部测试。含服务端小改动：`POST /pair/qr` 一次性配对令牌 + 桌面端「远程访问」引导页（配对二维码）。
- M2：工具审批卡片、附件、模型切换、技能与后台任务面板，iPad 双栏与键盘快捷键。
- M3：外部 TestFlight → App Store 提审；多语言（中文优先）。
- M4：可选迭代——APNs 推送、Siri/快捷指令、小组件。

## 已决策（2026-10-25）

1. 分发：**TestFlight 内测先行**，验证后再考虑 App Store 公开。
2. 项目目录名：**`mobile-ios/`**（与 `mobile-native` 对称）。

## 开放问题

1. 分发范围：先 TestFlight 内测，还是直接冲 App Store 公开上架？（App Store 审核对 AI 客户端有额外审核项，建议先 TestFlight。）
2. 是否需要 iPad 专属能力进首版（外接键盘必做，Apple Pencil 无场景）。
3. 主机明文 HTTP 的白名单交互形态（添加主机时的警示文案与确认方式）。
4. 远程可达性（已定向）：v1 内置 Tailscale 指引（免费、端到端加密，流量不过第三方）；**托管中继留作 v2 订阅功能候选**——主机主动连出、免安装免自建网络，是大众用户与付费点的自然形态，与 Tailscale 路线并存而非二选一。
4. 项目目录名：`ios/`（与 `mobile-native` 并列）还是 `mobile-ios/`。
