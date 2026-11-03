# 手机远程访问与加密中继

实现入口：`internal/relay/`、`cmd/easyagent-relay/`、桌面设置「手机远程访问」、Android / iOS 主机配对页。v0.36.1 及以前仅包含设计文档，不支持此功能。

## 使用

1. 电脑打开设置 → **手机远程访问**，首次填写中继管理员发放的接入码并开启。接入码只用于注册，不保留在桌面设置中。
2. 状态变为「已连接」后生成二维码。手机添加主机 → **扫码配对电脑**，也可粘贴配对码。二维码 5 分钟有效、只能使用一次；离开桌面设置会取消未完成配对。
3. 手机直接显示电脑的会话和聊天。设备凭据保存到 Android Keystore / iOS Keychain，不需要复制电脑 Owner API Key，也不需要 VPN。
4. 桌面可撤销设备或关闭远程访问，立即阻断新请求并断开已有连接。已经在电脑上运行的任务仍按原任务生命周期执行，停止任务需单独操作。

当前为接入码灰度开放，默认中继 `https://150.5.143.32`，也支持自托管 HTTPS 地址。更换中继前须撤销已配对设备再重新配对。电脑须保持开机、核心须运行，两端网络须允许出站 HTTPS/WSS。已有 Tailscale / 局域网主机继续使用原连接。

Android 复用现有聊天、审批和附件能力。iOS 仍受其客户端现有功能范围限制，当前可查看会话和聊天；中继不自动补齐尚未实现的审批等界面。iOS 被系统挂起时不保证实时通知，当前没有 APNs。

## 数据路径

```text
手机 OkHttp / URLSession
  │ 内层 HTTPS / WSS，钉扎电脑公钥
  ▼
App 内回环 CONNECT 代理（仅允许该电脑的虚拟域名）
  │ 出站 WSS，载荷已经是内层 TLS 密文
  ▼
公网中继 ── 电脑主动建立的 WSS ── 电脑 TLS 入口
                                    │ 设备凭据校验
                                    ▼
                               原有 HTTP / WS handler
```

中继承载聊天与附件流量，**并非只做地址发现**。它能看到来源 IP、路由标识、连接时长和流量大小，不能解密聊天、配对令牌或设备凭据。手机在自己的 TLS 端点解密，因此能正常查看聊天。首版没有 P2P、打洞或明文回退。

每条应用连接独占一对数据 WebSocket，电脑另有一条控制连接。回环 CONNECT 代理不修改系统代理；外层 WSS 使用系统证书信任，内层使用配对公钥和严格主机名校验。独立数据连接避免大附件阻塞其他流。

电脑将认证后的内层请求交给同进程业务 handler，以不可由网络客户端注入的 context 身份接入原授权门禁，没有绕过认证的 loopback 转发端口。中继不能指定任意转发目标。

## 配对与身份

二维码 JSON：

```json
{
  "v": 1,
  "relay_url": "https://relay.example.com",
  "route_id": "<32 个随机字节的 base64url，无填充>",
  "host_pubkey_fp": "sha256:<SPKI DER 的 SHA-256，标准 Base64>",
  "pair_token": "<32 个随机字节的 base64url，无填充>",
  "host_name": "我的电脑",
  "expires_at": "2026-11-03T12:05:00Z"
}
```

只有已认证的本机 Owner 能生成、取消配对或撤销设备；未配置 Owner Key 或外部网络发来的管理请求会被拒绝。路由标识不是凭据。

手机先完成内层 TLS、校验二维码公钥，然后在内层 HTTPS 调用 `POST /pair/claim` 提交 `pair_token` 和设备 `name`。电脑原子消费令牌，返回独立 `device_token`；后续业务请求使用内层 Bearer。最多 16 台设备，配对码累计 10 次失败后失效，重新生成会使旧码失效。

手机持久化的公开主机配置仅含中继地址、路由和指纹，凭据单独安全存储。电脑数据目录的 `remote-access.json` 原子写入、权限 `0600`，包含主机隧道凭据、TLS 私钥和设备凭据哈希。二维码不包含 Owner Key；配对令牌不进入持久化主机配置。

内层采用 P-256，虚拟域名为 `ea-<SPKI SHA-256 前 16 字节的十六进制>.invalid`。证书 30 天有效，剩余不足 7 天时在新连接前用原密钥续签并保存；钉扎不变。公钥变化必须重新扫码。关闭访问后重新开启保留已配对设备。设备不能经中继访问 `/remote-access*` 或 `/admin/*`。

| 本机管理 API | 功能 |
|---|---|
| `GET /remote-access` | 状态、脱敏设备列表、配对状态 |
| `POST /remote-access` | `enabled`、`relay_url`、首次 `registration_key` |
| `POST /remote-access/pair` | 生成二维码载荷 |
| `DELETE /remote-access/pair` | 取消配对 |
| `DELETE /remote-access/devices/{id}` | 撤销设备 |

## 中继协议与约束

| HTTPS / WSS 路径 | 功能 |
|---|---|
| `GET /health` | 服务、协议、构建版本，无路由或凭据 |
| `POST /register` | 接入码 Bearer 认证，返回路由和主机凭据 |
| `GET /host?route_id=` | 主机凭据 Bearer 认证，控制连接 |
| `GET /client?route_id=` | 手机数据连接，不携带业务凭据 |
| `GET /host/stream?route_id=&connection_id=` | 主机凭据认证，加入对应数据连接 |

控制消息为 `{"type":"open","connection_id":"..."}`，数据只接受不透明二进制帧。注册表持久化路由和主机凭据哈希，重启后仍须认证才能恢复。新主机连接替换旧连接时关闭旧数据流，连接代次隔离。

默认限制：256 个注册路由，每路由 16 条数据连接，每来源 IP 32 条，全局 128 条；单帧 64 KiB，转发块 32 KiB、有界缓冲，每连接每方向 4 MiB/s。注册按来源限速。Nginx 覆盖 `X-Real-IP`，中继仅信任来自回环地址的该请求头。默认绑定 `127.0.0.1:8787`，此明文后端端口不能暴露公网。

20 秒心跳、70 秒失联关闭；主机以 1、2、4…最多 30 秒加抖动重连。手机连接层恢复聊天订阅和断点；非幂等业务请求不由中继盲目重放。中继仍能断连、延迟、观察元数据；端到端加密不承诺匿名或可用性。

## 部署

需要 Go 二进制、systemd 和 HTTPS/WSS 反向代理，不运行模型、Agent、数据库或聊天存储。按服务器架构从已核实源码构建：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git rev-parse HEAD)" \
  -o easyagent-relay ./cmd/easyagent-relay
```

1. 二进制安装至 `/usr/local/bin/easyagent-relay`，安装 [systemd 模板](../deploy/easyagent-relay.service)。服务使用受限动态用户、私有状态目录和自动重启。
2. root 私有 `/etc/easyagent-relay.env` 写入 `EA_RELAY_REGISTRATION_KEY=<至少 32 字符的高熵随机值>`，权限 `0600`。该值独立于模型、Owner 和设备凭据，禁止提交、记录日志或内置客户端。
3. 按 [Nginx 示例](../deploy/easyagent-relay.nginx.conf.example) 配置 HTTPS/WSS、来源 IP、超时及禁用访问日志，保留 ACME 续期路由。域名或 IP 证书必须被手机系统信任，不跳过证书校验。
4. `systemctl enable --now easyagent-relay`，验证公网健康检查、WSS、手机配对。保留并监控证书续期 timer；短期 IP 证书尤其依赖自动续期。
5. 备份 `/var/lib/easyagent-relay/routes.json` 和 root 环境文件。升级保存旧二进制和状态，原子替换并重启，核对构建版本及实际恢复。丢失注册表会使原主机身份无法恢复。

## 验证与后续

Go 测试覆盖配对过期、取消、并发消费、认证、撤销、关闭恢复和证书续签，race 检测覆盖隧道并发。Android 和 Apple 原生网络栈连接本地 Go 服务，实际执行内层 TLS、REST、聊天 WebSocket、大附件、错误指纹拒绝、重启恢复与撤销，不使用真实模型。

Android 集成测试需 PATH 中有根 `go.mod` 对应的 Go；Gradle 将中继源码纳入测试缓存输入。iOS `swift test` 使用完整 Xcode 的 Testing 框架并运行 URLSession 联调。相机扫码及实际移动网络仍需真机验收，本机协议测试不能替代这一部分。

后续范围：APNs、账号订阅、多区域及局域网优化。原有 Tailscale 部署不需要迁移或删除。
