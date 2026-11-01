# 远程访问中继设计（扫码即连）

状态：设计定稿待实现（2026-11-01）。本文是「手机扫码即跨网连接桌面 EasyAgent」的主设计，实现拆分见里程碑。关联文档：[IOS_CLIENT_DESIGN.md](IOS_CLIENT_DESIGN.md)（其 M1 配对令牌是本设计的地基）、[SECURITY_MODEL.md](SECURITY_MODEL.md)。

## 背景与动机

iOS 设计文档 v1 的跨网方案是 Tailscale / 反向代理，自用可行，C 端不可行。2026-11-01 实测自家环境（tailnet `hwj`，Headscale 部署首尔）：

- `MappingVariesByDestIP: true`（对称 NAT），`tailscale ping` 报 `direct connection not established`
- MacBook ↔ mini 全部流量绕行 DERP(hkg)，RTT 125-170ms
- 结论：国内家宽 / 办公网环境下 UDP 打洞失败是**常态**，要求 C 端用户装 VPN 或配反代不现实

因此 C 端需要「主机主动连出 + 运营方中继」模式。参考实现：[yetone/alma-relay](https://github.com/yetone/alma-relay)（MIT）——桌面出站 WebSocket 到中继、手机经中继转发、端到端加密、扫码配对，与本设计同构，可整体参考其 Durable Objects 会话注册、WebSocket hibernation、ECDH+AES-GCM 方案。

## 目标与非目标

目标：

- 手机装 App → 扫桌面端二维码 → 跨网连接，全程零网络配置（无 VPN、无公网 IP、任意 NAT 后可用）
- 中继零知识：中继只见密文，运营方无法读取会话内容
- 与既有 `serve` HTTP API、Owner 门禁、API Key 机制完全共存，`serve` 核心零改动

非目标（首版）：

- 不做账号体系与计费（v2 订阅另行设计，参考 IOS_CLIENT_DESIGN v2 规划）
- 不做 P2P/NAT 打洞；同局域网直连优化列为后续（二维码预留局域网地址字段）
- 不做 APNs 推送（后续里程碑，依赖中继在线状态，见开放问题）
- 不做一台主机多手机共享、自托管中继分发

## 总体架构

```
桌面 EasyAgent                       中继（我们运营，HK）              手机 App
┌─────────────────┐    出站 WSS     ┌──────────────┐    WSS     ┌──────────────┐
│ serve(HTTP,不动) │◄─隧道(穿NAT)──  │ 会话注册表     │◄─隧道──────│ 既有 Backend  │
│ relay-client(新) │───────────────►│ 密文帧转发     │───────────►│ (协议不变)    │
│  └ 本地TLS壳(新)  │               │ 在线状态      │            │              │
└─────────────────┘                └──────────────┘            └──────────────┘
        └──────────── E2E：TLS with 证书钉扎，中继只见密文 ────────────┘
```

三个新组件，各角色边界：

| 组件 | 位置 | 职责 | 明确不做 |
|---|---|---|---|
| relay-client | 桌面（`internal/relay`） | 出站 WSS、重连保活、把隧道桥接为本地 TCP | 不改 serve 协议 |
| 中继 | 独立 `cmd/easyagent-relay` 单二进制 | 注册、路由、帧转发、在线状态 | 不解析不落盘任何载荷 |
| 手机端配对 | iOS / Android 既有主机管理 | 扫码、钉扎证书、按 transport 连接 | 不新增业务协议 |

关键决策 1：**隧道内是透明 TCP 字节流**（每条手机连接 = 一条隧道内虚拟连接，转发到 `127.0.0.1:<serve端口>`）。手机到主机的全部既有 HTTPS API/WebSocket 语义原样工作，`serve` 与手机业务层无感知。

关键决策 2：**E2E 用标准 TLS + 证书钉扎，不自研应用层加密**。配对时桌面生成自签名证书（长期密钥），二维码携带证书公钥指纹；relay-client 在本地起 TLS 壳（`crypto/tls` 监听 → 转发 127.0.0.1:serve，约几十行），手机按钉扎指纹直连。中继只见 TLS 密文。不用 alma-relay 的应用层 ECDH+AES-GCM 方案——我们的流量本来就是标准 HTTPS，让 stdlib 做加密，避免在两端各加一层自研密码学（理由：实现与审计成本；若后续需要中继侧可见的元数据路由再评估）。

## 配对协议（扫码即连）

二维码内容（JSON，一次性令牌 5 分钟有效、单次使用）：

```json
{
  "v": 1,
  "relay_url": "wss://relay.example.com",
  "route_id": "r_...",
  "host_pubkey_fp": "sha256:...",
  "pair_token": "pt_...",
  "lan_addr": "192.168.x.x:8080"
}
```

时序：

1. 桌面开启「远程访问」→ relay-client 出站连中继 `GET /host`（携带 route_id 证明）→ 长连接保活
2. 桌面调 `POST /pair/qr`（IOS_CLIENT_DESIGN M1 已规划）生成 pair_token，渲染二维码
3. 手机扫码 → WSS 连中继 → 凭 pair_token + route_id 完成绑定（令牌随即作废）
4. 手机经隧道对本地 TLS 壳做 TLS 握手，校验证书指纹 = `host_pubkey_fp`（TOFU：首次扫码即信任锚，此后指纹不符即拒绝，防中间人）
5. 手机保存主机条目（relay_url + route_id + 钉扎指纹），后续免扫码直连

`lan_addr` 为后续局域网直连预留：同网段时手机优先直连，失败回落中继（首版不实现选择逻辑）。

## 中继服务

形态：Go 单二进制 `cmd/easyagent-relay`（与仓库技术栈一致，便于复用团队运维；接口极小，后续如需弹性可移植 Workers）。部署 HK（实测该位置单程 37ms，为国内可达的延迟底线），随仓库附 `deploy/` systemd 模板与部署文档。

端点（全部 WSS/HTTPS，无业务语义）：

| 端点 | 方向 | 作用 |
|---|---|---|
| `POST /register` | 桌面 | 携带注册密钥（可选）换取 route_id |
| `GET /host?route_id=` | 桌面 | 升级 WSS，主机侧隧道入口，30s ping/pong 保活 |
| `GET /client?route_id=&pair_token=` | 手机 | 配对绑定 / 升级 WSS，客户端侧隧道入口 |
| `GET /status?route_id=` | 手机 | 在线状态（后续 APNs 与 UI 用） |

转发模型：帧 `{type: data | open | close | ping}`，`data` 为不透明二进制（TLS 密文），一一映射到「手机虚拟连接 ↔ 桌面 127.0.0.1 TCP 连接」。会话注册表在内存，重启即失效（主机有重连逻辑，可接受）。

防滥用（C 端开放前的最小集）：注册密钥灰度发放、单 route_id 并发连接上限、单连接带宽限速占位。注册密钥机制参考 alma-relay 的 `RELAY_REGISTRATION_KEY`。

## 手机端改动

- 主机模型增加 `transport: direct | relay`；扫码后自动创建 relay 主机，无需用户理解地址概念
- 连接层：relay 主机 = 连中继 WSS + 钉扎 TLS；既有流式对话 / 工具审批 / 会话同步协议不变（见 IOS_CLIENT_DESIGN「与服务端的契约」）
- Android 与 iOS 对等改造（两端 Backend 结构对称，改动位置相同）

## 安全模型

在 [SECURITY_MODEL.md](SECURITY_MODEL.md) 基础上新增以下性质：

- pair_token 一次性 + 5 分钟窗口，泄漏影响极小
- 主机私钥仅存桌面（文件权限 0600 / Keychain），公钥指纹经二维码带外传递，TOFU 绑定
- 中继零知识：只见 TLS 密文与路由元数据（route_id、连接时长、流量体积）
- Owner 门禁与 API Key 沿用既有机制，中继路径不提供任何绕过手段

## 里程碑（实现拆分）

| 里程碑 | 内容 | 验收 |
|---|---|---|
| M1 | `POST /pair/qr` 一次性令牌 + 桌面「远程访问」页渲染二维码 | 扫码得到合法 JSON，令牌复用被拒 |
| M2 | `cmd/easyagent-relay` 中继二进制 + `deploy/` 模板 | 两台 NAT 后设备经中继互通 TCP |
| M3 | 桌面 relay-client（出站 WSS + 重连 + TLS 壳桥接） | 手机经中继 HTTPS 访问 serve API 全通 |
| M4 | 手机端扫码配对闭环（iOS 并入其 M1 里程碑） | 全新设备扫码后 30 秒内可流式对话 |
| M5（后续） | APNs、局域网直连、已配对设备管理、多区域中继 | 另行设计 |

依赖顺序：M1 → M2 → M3 → M4，M2/M3 可并行开发（先以本地回环联调）。

## 开放问题

1. 中继域名与 TLS 证书自动化（Caddy / certbot 选型），多区域路由策略
2. 带宽成本核算与 v2 订阅定价（中继是唯一持续成本，也是付费点）
3. 是否支持用户自托管中继并填入自定义 relay_url（与官方中继并存，影响配对二维码设计与防滥用模型）
4. APNs：推送证书申请流程与长任务完成通知的触发点（依赖 M2 在线状态接口）
