# EasyAgent Android 手机端

EasyAgent 的 Android 原生客户端（Kotlin + Jetpack Compose，无 WebView），定位与 `desktop-native/` 对应：连接一台运行主机（远程 Go 服务），复用 core 的认证 REST + `/ws` WebSocket 协议，做"口袋里的 Agent 遥控器"——对话、下发任务、审批、查看状态。

桌面主宿主 MyGo（`desktop-native/`）当前只支持 macOS/Windows/Linux 桌面与浏览器，没有 Android 目标，因此手机端不与桌面端共用 UI 框架；两者共享的是同一套协议语义（`desktop-native/backend.go` 的连接模型与 `projection.go` 的信封投影，本目录 `backend/` 包下为等价移植并带 JVM 单元测试）。

## 功能（v1）

- 运行主机配置：服务地址 + 服务令牌（`EA_SERVER_API_KEY`，未设置时使用 `EA_API_KEY`），先测试后保存；令牌经 Android Keystore AES-GCM 加密落盘。
- 会话列表：拉取/新建/重命名/置顶/归档/删除，下拉刷新，置顶优先。
- 聊天：历史恢复（`GET /sessions/{id}/run` 快照）、流式文本与思考、工具调用卡片（参数/输出/详情、失败展开）、错误与上下文压缩提示、排队消息横幅。
- 任务控制：运行中排队发送（`POST /queue`）、停止当前任务、工具审批（批准/拒绝）。
- WebSocket 断线自动重连（1s 起指数退避，上限 15s），重连后按 `after_seq` 续订，不丢事件。

## 构建

需要 JDK 17 与 Android SDK（compileSdk 35，minSdk 26）：

```sh
cd mobile-native
JAVA_HOME=<jdk17路径> ANDROID_HOME=<sdk路径> ./gradlew assembleDebug
# 产物 app/build/outputs/apk/debug/app-debug.apk
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

首次构建会从 google()/mavenCentral() 拉取 AGP/Kotlin/Compose 依赖。协议层测试：

```sh
./gradlew --no-daemon :app:testDebugUnitTest :app:lintDebug :app:assembleDebug
```

Verify CI 与正式 Release 的验证阶段均执行上述单元测试、Lint 与 Debug APK 构建。当前手机端为源码预览，正式 Release 仍按 `docs/RELEASING.md` 发布核心、桥接及 macOS 原生客户端，不分发 Android 安装包；Android 正式签名发布尚未接入。

## 连接说明

手机需能访问运行主机：家庭局域网，或接入与主机相同的 tailnet 后使用其虚拟网地址。地址形如 `http://<主机>:8080`；tailnet 链路由 WireGuard 加密，应用内允许明文 HTTP（`usesCleartextTraffic`）。令牌与网页端登录用的服务令牌相同：优先使用 core 的 `EA_SERVER_API_KEY`，未配置时兼容使用 `EA_API_KEY`。

部署相关的地址与密钥不写入本仓库。

## 边界与已知限制

- Markdown 仅渲染围栏代码块（等宽样式），行内样式按原文显示。
- 未实现：附件上传、会话分支、文件工作台、模型切换、MCP/技能市场、用量页、本地核心进程模式（手机端只连远程主机）。需要时按 desktop-native 对应实现逐步补齐。
- 审批卡片的"问答（answers）"表单按批准/拒绝处理；带答案的问答场景待补。
- 明文 HTTP 任意开启：仅在私有网络/tailnet 内使用；接入公网主机时请配置 HTTPS。
