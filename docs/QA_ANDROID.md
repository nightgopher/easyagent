# Android 发布验收

每次发布记录候选 SHA、APK 版本/签名、测试设备/API、检查结果和报告链接。失败项修复后重跑，未执行项标为未执行；构建成功不等于 UI 验收通过。

## 自动检查

```sh
cd mobile-native
./gradlew --no-daemon :app:testDebugUnitTest :app:lintDebug :app:assembleDebug
./gradlew --no-daemon :app:connectedDebugAndroidTest
```

第二条命令需要已启动的模拟器或测试设备。`Android UI` 工作流自动运行 Android 12/API 31、14/API 34、15/API 35；发布流程复用相同工作流并检验候选 SHA。报告在 `app/build/reports/androidTests/connected`，XML 在 `app/build/outputs/androidTest-results`。API 31 以上的截图由测试写入 `/data/local/tmp/easyagent-ui`，CI 收集到对应 API 的 artifact 中。

`RuntimeUiTest` 使用真实 MainActivity 和本地 HTTP/WebSocket fixture，检查：

- 错误令牌得到 401，修正后成功连接；Android Keystore 加密和重新读取凭据。
- 从界面保存、切换两台主机；各主机草稿互不覆盖。
- 历史恢复、实时文本、批准与拒绝审批，校验实际发出的确认请求。
- 任务运行时消息入队、停止请求；后台恢复和 WebSocket 重连。
- 横屏、深色模式，并输出实际屏幕截图。

测试不调用实际模型或飞书服务。它覆盖界面与客户端协议，真实运行主机的部署检查另见 [systemd 验证](SECURITY_MODEL.md#验证-systemd-服务包)。

## 每次发布补充检查

| 项目 | 操作与通过条件 |
|---|---|
| 安装与升级 | 在测试设备安装候选 APK，用同证书新版本覆盖升级；保存的主机及凭据可恢复，版本号正确 |
| 重启恢复 | 强制结束进程再打开；主机配置可恢复，令牌可解密，重新获取会话状态 |
| 网络切换 | 断网、Wi-Fi/移动网络切换后可恢复；离线发送保留草稿，没有重复执行 |
| 真实服务 | 连接测试运行主机完成一次流式对话、审批拒绝/批准、排队与停止；不使用生产目录做破坏性测试 |
| 屏幕与输入 | 至少手机与平板各一组，检查横竖屏、软键盘和深浅色；按钮、审批内容和输入框可用 |
| 发布身份 | 正式 APK 的签名、版本与 SHA-256 校验通过；Debug 或临时证书 APK 不作为正式升级结论 |

## v0.22.0 模拟器基线

2026-10-22，功能基线为 `cc76ac4e7636e4ff2aebdd6b7f560c301522243d`，使用 Debug APK 与模拟运行主机。[API 31/34/35 CI 报告与截图](https://github.com/hwj123hwj/easyagent/actions/runs/39828974883)均通过；正式发布另对 tag 指向的确定 SHA 重跑相同矩阵，最终提交与资产校验值记录在 Release 的 `release.json` 和 `checksums.txt` 中。

| 环境 | 结果 |
|---|---|
| Android 12/API 31，arm64 手机模拟器 | 4 项 UI 用例通过 |
| Android 14/API 34，arm64 手机模拟器 | 4 项 UI 用例通过，截图已检查 |
| Android 15/API 35，arm64 平板模拟器 | 4 项 UI 用例通过，深色横屏截图已检查 |

Android 14 另完成 Debug APK 的初次安装、同版本 `adb install -r` 覆盖安装、强制结束进程再启动；主机名称和配置文件保持不变，重启后的 HTTP 请求仍使用解密出的正确测试令牌。此项不代替正式签名的跨版本升级验收。

## v0.22.0 正式签名升级检查

2026-10-22，在独立 Android 14/API 34 arm64 模拟器上，从 GitHub 下载并校验正式 v0.21.1 APK，覆盖安装使用相同固定发布证书签名的本机 v0.22.0 候选 APK（versionCode `2200300`）。候选使用上述功能基线及 v0.22.0 版本元数据，连接本地 HTTP/WebSocket 模拟主机。

| 检查 | 结果 |
|---|---|
| 发布身份 | 两个版本均通过固定证书、应用标识、版本号及非 Debug 属性校验；旧 APK 下载 SHA-256 与 GitHub 资产一致 |
| 跨版本覆盖安装 | `adb install -r` 成功；主机名称、配置及加密令牌文件逐字节保持一致，升级后发出的会话请求包含正确解密后的测试令牌 |
| 强退与重启 | 强制结束进程后重开，所选主机、会话列表和已解密的测试凭据均恢复 |
| 同设备重复安装与截图 | Debug UI 测试重复运行通过，截图保存在 shell 临时目录，不依赖被卸载应用的共享存储归属 |

实体手机的蜂窝网络切换和真实模型/业务账号仍是每次 Release 的人工检查项，本次未执行。模拟器检查不将这些项目记为通过。
