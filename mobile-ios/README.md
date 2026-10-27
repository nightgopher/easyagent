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
swift test                 # 后端层单测（macOS 上直接跑）
```

App target（Xcode 工程）在 M1 后续提交加入；SPM 库先行，让网络层可以独立测试。

## 约定

- 凭据只进 Keychain（App 侧），测试与文档里不出现真实 token。
- 与服务端契约以 `mobile-native/backend/Wire.kt` 为权威参考；两端字段命名对齐。
- 后端层不 import SwiftUI，保持可测。
