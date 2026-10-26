# Go 原生桌面客户端

EasyAgent 的唯一维护中桌面客户端是 `desktop-native/`，使用 Go / MyGo 原生界面，Bundle ID 为 `com.hwj.easyagent`。界面不使用 Electron、React、HTML 或 WebView；macOS 电脑控制通过随包提供的 Swift 系统助手实现。核心服务、TUI 和桌面应用均由 Go 构建；服务内嵌 Web 界面继续独立维护。

## 安装与启动

Apple Silicon Mac 从 Release 下载 `easyagent-native-darwin-arm64.dmg`，正式安装入口为 `/Applications/EasyAgent.app`。v0.17.1 及此前的公开包使用 ad hoc 签名；从 v0.17.2 起正式构建固定证书身份以保留系统授权，尚未进行 Developer ID 签名与 Apple 公证。由于自签证书没有团队 ID，主程序通过 `desktop-native/resources/easyagent.entitlements` 关闭 hardened runtime 库验证，否则较新的 macOS 会拒绝加载随包的终端渲染等嵌入库。原生客户端已接入签名更新：设置中可检查更新，并选择自动检查或后台下载。存在活动任务、待审批操作或未保存文件时会阻止退出与更新重启。更新频道需要正式 Release 发布签名清单及归档后才可用；发布构建要求见 RELEASING.md。

本机构建：

```sh
make desktop
open desktop-native/build.noindex/darwin-arm64/EasyAgent.app
```

构建脚本会验证实际 bundle 的核心查找、启动、`/health` 响应和退出回收。产物保存在 `.noindex` 目录，不自动替换现有应用或运行服务。原生 GUI 验收结束后先退出应用，再执行：

```sh
python3 scripts/unregister-desktop-apps.py desktop-native/build.noindex/darwin-arm64/EasyAgent.app
```

## 配置和数据

“运行主机”支持此 Mac 与远程 Go 服务。此 Mac 启动独立 loopback 核心，使用应用自己的 `core-data` 和 `core-home`；模型配置可沿用 CLI 默认值，也可在“模型连接”中单独设置。远程主机配置需要服务地址和 API 令牌。密钥由 macOS 钥匙串保存，普通 JSON 仅存元数据。

远程连接保存成功前会回读钥匙串验证令牌。原地址留空保留已存令牌；修改地址需重新填写令牌，新连接留空表示无令牌。相同地址与令牌重复保存保留连接 ID，实际更换地址或凭据会另存连接并保留原连接与草稿。移除连接会清空当前凭据并清理可访问的旧条目；受旧系统授权保护的条目可能保留，但不会再被自动使用。不删除远程会话或文件。

遇到 HTTP 401，点击错误提示中的“重新填写访问令牌”，会打开对应主机并聚焦令牌框；若其他设置表单未保存，会先确认是否放弃修改。401 后停止自动重试，保存正确凭据后重新连接；普通网络断线继续自动恢复。声明有令牌但凭据缺失的连接会直接提示修复，不发送无认证请求。

macOS 的默认钥匙串与全局搜索列表可能不一致。凭据的读写均显式定位同一个默认钥匙串。v0.23.1 起由固定签名的专用凭据程序访问钥匙串，应用更新复用经过签名校验的程序；凭据程序自身更新时，通过原程序自动迁移到新程序的独立条目。父进程与子进程互相验证签名，密钥仅通过匿名管道传递，不写入普通配置或命令行参数。

更换签名证书（例如从自签证书升级到 Developer ID）后，旧签名的缓存凭据程序会保留但不再执行，也不会阻止当前程序保存新凭据。旧签名拥有的令牌或 API 密钥需要重新填写一次；客户端不会跨签名读取或回退到过期值。当前随包程序及其缓存仍须通过完整身份与权限校验。

可读取的旧 `com.hwj.easyagent.native-preview` 凭据会自动迁移。旧授权已失效时，客户端直接提供重新填写主机访问令牌或模型 API 密钥的入口，不反复弹出系统密码框。这里填写的是该服务的令牌或 API 密钥；旧弹窗要求的 “login” 钥匙串密码可能与当前 Mac 登录密码不同，客户端不会重置或更改它。

发布验证在独立的现代格式钥匙串中检查两个不同应用构建、凭据程序升级、拒绝其他签名/应用、清除后的重启、锁定失败和真实 REST / WebSocket 认证；使用虚构凭据，结束后删除测试钥匙串。

连接现有服务才能查看该服务上的会话。切换主机不会取消远程任务；退出应用会关闭它托管的本地核心。存在运行任务时，修改本地模型配置会拒绝重启，须先停止任务。

会话、工具审批、队列、文件工作台、终端、技能市场、MCP、飞书及音频能力见 [原生客户端说明](../desktop-native/README.md)。电脑控制需要 macOS 14+，辅助功能和屏幕录制授权仍由用户完成，见 [配置说明](CONFIG.md#实验性电脑控制)。

## 旧客户端归档

Electron / React 的 `desktop/`（含共用前端的 Capacitor Android 外壳）已退役，不再构建、测试或发布。从 v0.16.0 起 Release 不再要求 Electron DMG；已发布版本与资产不变。

历史源码与文档可从 [archive/workflow](https://github.com/nightgopher/easyagent/tree/archive/workflow/desktop) 查看，退役前的精确快照为 [e4b4088](https://github.com/nightgopher/easyagent/tree/e4b4088/desktop)。Electron 的自动更新、固定证书重签名及 safeStorage 数据不适用于原生客户端；本次退役不会删除旧用户数据、签名钥匙串或覆盖已安装应用。
