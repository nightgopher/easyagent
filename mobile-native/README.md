# EasyAgent Android 手机端

EasyAgent 的 Android 原生客户端（Kotlin + Jetpack Compose，无 WebView），定位与 `desktop-native/` 对应：保存并切换多台运行主机（远程 Go 服务），复用 core 的认证 REST + `/ws` WebSocket 协议，做"口袋里的 Agent 遥控器"——对话、下发任务、审批、查看状态。

桌面主宿主 MyGo（`desktop-native/`）当前只支持 macOS/Windows/Linux 桌面与浏览器，没有 Android 目标，因此手机端不与桌面端共用 UI 框架；两者共享的是同一套协议语义（`desktop-native/backend.go` 的连接模型与 `projection.go` 的信封投影，本目录 `backend/` 包下为等价移植并带 JVM 单元测试）。

## 功能（v1）

- 运行主机配置：可命名并保存多个服务地址 + 服务令牌（`EA_SERVER_API_KEY`，未设置时使用 `EA_API_KEY`），支持测试、编辑和移除；令牌按主机经 Android Keystore AES-GCM 加密落盘。
- 主机切换：会话列表顶部的下拉按钮快速切换，或从「更多 → 管理运行主机」添加和管理。各主机独立连接、重连、会话缓存与本次运行中的草稿；切换不清空草稿或断开其他连接。保存的主机及所选主机在重启后恢复；旧版单主机配置自动迁移，无需重新输入令牌。
- 会话列表：拉取/新建/重命名/置顶/归档/删除，下拉刷新，置顶优先。
- 聊天：历史恢复（`GET /sessions/{id}/run` 快照）、流式文本与思考、工具调用卡片（参数/输出/详情、失败展开）、错误与上下文压缩提示、排队消息横幅。
- 任务控制：运行中排队发送（`POST /queue`）、停止当前任务、工具审批（批准/拒绝）。
- 附件与图片：输入框「+」可拍照、从相册多选或选择文件，上传到 `POST /sessions/{id}/attachments` 后随下一条消息发送；输入框上方显示缩略图，可移除。照片超过 4 MiB 或边长超过 2560 像素时自动缩放压缩为 JPEG，其他文件超过 4 MiB 直接提示。会话流里的用户图片显示缩略图，点按查看大图。
- 语音输入：点麦克风开始录音（16 kHz WAV，最长 2 分钟），可取消；结束后由运行主机的 `POST /asr/transcribe` 转写，文字填入输入框供修改，不会直接发送。转写失败可重试或放弃；主机未配置 ASR 时显示服务端错误。
- WebSocket 断线自动重连（1s 起指数退避，上限 15s），重连后按 `after_seq` 续订，不丢事件。
- 通知：打开过的会话出现待审批，或运行结束（完成、失败、中断）时发通知。正在前台查看该会话时不打扰。待审批通知可以直接点「批准」「拒绝」，点通知本身会切到对应主机并打开会话。首次启动（Android 13 及以上）会请求通知权限，拒绝后不再发送。
  - 限制：通知依赖 App 进程和 WebSocket 存活。App 被系统回收后收不到；服务端对无人处理的审批两分钟后自动拒绝。只覆盖本次运行中打开过的会话（订阅过的会话）。

## 安装正式 APK

下载 [正式 APK](https://github.com/hwj123hwj/easyagent/releases/latest/download/easyagent-android.apk)（Android 8.0 / API 26 及以上）。如果手机浏览器直接下载 APK 反复中断，可改用 [ZIP 备用包](https://github.com/hwj123hwj/easyagent/releases/latest/download/easyagent-android.zip)（从 v0.19.2 起提供），解压后打开其中的 `easyagent-android.apk` 安装；ZIP 内的 APK 与同一 Release 的独立 APK 完全一致。

在 Android 上打开 APK 并允许本次来源安装，或使用 `adb install -r easyagent-android.apk`。两种下载格式均可对照同一 [Release](https://github.com/hwj123hwj/easyagent/releases/latest) 的 `checksums.txt` 验证 SHA-256。

从 v0.19.0 起 APK 使用固定发布证书，后续正式 APK 可以覆盖更新并保留应用数据；此前自行构建的 Debug APK 签名不同，不能直接覆盖，迁移前需自行保留连接信息，再卸载旧 Debug 应用。

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

UI 验收使用 `./gradlew --no-daemon :app:connectedDebugAndroidTest`，需已启动 Android 模拟器或连接测试设备。`Android UI` 工作流覆盖 API 31/34/35，正式 Release 的 Android 构建等待该矩阵通过。用例启动实际 Activity，连接带认证的本地 HTTP/WebSocket 模拟服务，覆盖凭据、主机切换、草稿、流式对话、审批、排队、停止、重连、横屏与深色模式；不调用真实模型。发布前按 [Android 验收清单](../docs/QA_ANDROID.md) 记录候选版本与人工项目。

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

首次启动没有配置时会显示「连接运行主机」。可先测试，再「保存并连接」；暂时离线的主机也可保存，客户端会持续重试。令牌无法解密时，该主机不会发送无认证请求，可在「运行主机」页重新编辑令牌。移除连接只删除此手机的配置、凭据与缓存，不删除服务器会话；修改地址或令牌会重建该连接的会话缓存，以免跨主机复用状态。

## 边界与已知限制

- Markdown 仅渲染围栏代码块（等宽样式），行内样式按原文显示。
- 聊天草稿及投影缓存保留在应用进程内；切换主机不会丢失，结束应用进程后不保证恢复。
- 未实现：附件上传、会话分支、文件工作台、模型切换、MCP/技能市场、用量页、本地核心进程模式（手机端只连远程主机）。需要时按 desktop-native 对应实现逐步补齐。
- 审批卡片的"问答（answers）"表单按批准/拒绝处理；带答案的问答场景待补。
- 明文 HTTP 任意开启：仅在私有网络/tailnet 内使用；接入公网主机时请配置 HTTPS。
