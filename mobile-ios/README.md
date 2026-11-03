# EasyAgent iOS / iPadOS 客户端

设计见 [docs/IOS_CLIENT_DESIGN.md](../docs/IOS_CLIENT_DESIGN.md)。Swift + SwiftUI，最低 iOS 17，iPhone/iPad 一套代码自适应；与 Android 客户端（`mobile-native/`）按同一服务端契约实现，核心不上机。

## 目录

```
Sources/EasyAgentBackend/   # 网络与数据层（与 Android backend/ 对称）：主机、API、WS 流式
Tests/EasyAgentBackendTests/
```

## 开发

```bash
cd mobile-ios
swift test                 # 完整 Xcode 的 Swift Testing；还需根 go.mod 对应的 Go
```

App target 已由 `project.yml` 提供，运行 `xcodegen generate` 后打开生成的工程；真机签名按自己的开发者账号配置。

添加主机可扫码或粘贴电脑设置 → 手机远程访问生成的一次性配对码。内层 TLS 钉扎电脑公钥，中继仅转发密文，设备凭据保存 Keychain；原有 Tailscale / 局域网连接保留。详见[配对与部署](../docs/RELAY_ACCESS_DESIGN.md)。当前界面支持基础会话列表和聊天，审批、APNs、附件界面等尚未补齐，不将传输连通等同于完整 Android 功能。

## 约定

- 凭据只进 Keychain（App 侧），测试与文档里不出现真实 token。
- 与服务端契约以 `mobile-native/backend/Wire.kt` 为权威参考；两端字段命名对齐。
- 后端层不 import SwiftUI，保持可测。
