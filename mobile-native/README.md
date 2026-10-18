# EasyAgent Android 手机端

EasyAgent 的 Android 原生客户端（Kotlin + Jetpack Compose，无 WebView），定位与 `desktop-native/` 对应：连接一台运行主机（远程 Go 服务），复用 core 的认证 REST + `/ws` WebSocket 协议，做"口袋里的 Agent 遥控器"——对话、下发任务、审批、查看状态。

桌面主宿主 MyGo（`desktop-native/`）当前只支持 macOS/Windows/Linux 桌面与浏览器，没有 Android 目标，因此手机端不与桌面端共用 UI 框架；两者共享的是同一套协议语义（`desktop-native/backend.go` 的连接模型与 `projection.go` 的信封投影，本目录 `backend/` 包下为等价移植并带 JVM 单元测试）。

## 功能（v1）

- 运行主机配置：服务地址 + 服务令牌（`EA_SERVER_API_KEY`，未设置时使用 `EA_API_KEY`），先测试后保存；令牌经 Android Keystore AES-GCM 加密落盘。
- 会话列表：拉取/新建/重命名/置顶/归档/删除，下拉刷新，置顶优先。
- 聊天：历史恢复（`GET /sessions/{id}/run` 快照）、流式文本与思考、工具调用卡片（参数/输出/详情、失败展开）、错误与上下文压缩提示、排队消息横幅。
- 任务控制：运行中排队发送（`POST /queue`）、停止当前任务、工具审批（批准/拒绝）。
- WebSocket 断线自动重连（1s 起指数退避，上限 15s），重连后按 `after_seq` 续订，不丢事件。

## 安装正式 APK

从 [最新 Release](https://github.com/hwj123hwj/easyagent/releases/latest) 下载 `easyagent-android.apk`（Android 8.0 / API 26 及以上），在 Android 上打开并允许本次来源安装，或使用 `adb install -r easyagent-android.apk`。下载后可对照同一 Release 的 `checksums.txt` 验证 SHA-256。

从 v0.19.0 起 APK 使用固定发布证书，后续正式 APK 可以覆盖更新并保留应用数据；此前自行构建的 Debug APK 签名不同，不能直接覆盖，迁移前需自行保留连接信息，再卸载旧 Debug 应用。正式签名与构建验证不代表已完成 Android 真机或模拟器 UI 验收。

## 从源码构建

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

Verify CI 与正式 Release 的验证阶段均执行上述单元测试、Lint 与 Debug APK 构建。开发版 versionName 从 `desktop-native/mygo.json` 读取，Debug versionCode 为 1；正式构建由发布脚本注入与 tag 对应的版本和递增 versionCode。

演练正式构建（需要设置 `JAVA_HOME`、`ANDROID_HOME` 并安装 Build Tools 35.0.1），从仓库根目录运行：

```sh
python3 scripts/build-android.py build /tmp/easyagent-android-check --test-signing
```

输出目录须不存在。演练使用一次性密钥，验证 Release APK 的版本、应用标识、签名和非 Debug 属性，不使用正式私钥。正式发布与证书配置见 [发布指南](../docs/RELEASING.md#android-签名与发布)。

## 源码边界

- `backend/`：REST / WebSocket、Wire 类型、事件投影和 Android Keystore 凭据存储。
- `ui/`：连接、会话、聊天与 Compose 组件；`MainActivity.kt` 为系统入口。
- `app/src/test/`：协议投影与 HTTP 交互的 JVM 回归测试，不要求真实模型或运行主机。

手机端通过同一套 [核心 API](../docs/API.md) 与服务通信。Go 服务、桌面模块与 Android 工程的构建和依赖相互独立，见 [架构](../docs/ARCHITECTURE.md#客户端与构建单元)。

## 连接说明

手机需能访问运行主机：家庭局域网，或接入与主机相同的 tailnet 后使用其虚拟网地址。地址形如 `http://<主机>:8080`；tailnet 链路由 WireGuard 加密，应用内允许明文 HTTP（`usesCleartextTraffic`）。令牌与网页端登录用的服务令牌相同：优先使用 core 的 `EA_SERVER_API_KEY`，未配置时兼容使用 `EA_API_KEY`。

部署相关的地址与密钥不写入本仓库。

## 边界与已知限制

- Markdown 仅渲染围栏代码块（等宽样式），行内样式按原文显示。
- 未实现：附件上传、会话分支、文件工作台、模型切换、MCP/技能市场、用量页、本地核心进程模式（手机端只连远程主机）。需要时按 desktop-native 对应实现逐步补齐。
- 审批卡片的"问答（answers）"表单按批准/拒绝处理；带答案的问答场景待补。
- 明文 HTTP 任意开启：仅在私有网络/tailnet 内使用；接入公网主机时请配置 HTTPS。
