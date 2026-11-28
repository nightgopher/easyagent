# Substrate 沙箱部署探索

把 EasyAgent 作为有状态 Actor 部署进 [Substrate](https://github.com/agent-substrate/substrate)(CNCF 沙箱运行时,2026-11 时点 v1.0)的完整探路记录:企业级形态的参照系、经过完整验证的实操路径,以及九个真实踩坑。部署加深前先读[安全模型](./SECURITY_MODEL.md)。

## 为什么研究它

[安全模型](./SECURITY_MODEL.md)里说:EasyAgent 不在工具内部实现进程级沙箱,隔离交给操作系统和容器做。容器是这条路上大众的一步,Substrate 是这条路的极致形态——它把"隔离 + 有状态会话 + 零信任出口"做成了一个调度原语。

企业级多租户部署需要四样东西,Substrate 每一样都给了教科书实现:

| 企业级需求 | Substrate 的机制 | 对 EasyAgent 的意义 |
|---|---|---|
| 多租户隔离 | 每个 Actor 独占 gVisor / microVM 沙箱,默认拒绝网络 | 多个用户的 agent 互相不可见、不可达 |
| 有状态会话 | 空闲 Actor 快照出内存(内存+文件+rootfs 三层),请求到达 <500ms 恢复,可跨 worker 迁移(实测冻结在 A pod、复活在 B pod) | 对话上下文、文件句柄、MCP 连接不因进程死亡而丢失 |
| 零信任出口 | 沙箱内零凭证;出站流量过策略执行点(PEP),按 per-actor 的 EgressPolicy 放行;HTTPS 经 MITM 审查 | 被提示注入的 agent 手里没有密钥,打不通未授权通道 |
| 成本密度 | 空闲即冻结,10~30x 超额复用,只为"思考时刻"付费 | 一千个挂起的会话 ≈ 对象存储里的一堆快照 |

而 EasyGateway 在这套形态里天然就是**模型平面**:沙箱里的 Actor 出网策略指向网关,密钥留在网关侧,Agent 本体无凭据。

## 结论先行

1. **能跑通,且是标准路径。** `easyagent serve` 是普通 HTTP 服务,不内嵌进程沙箱、不依赖宿主特权,天然适合作为 Substrate Actor——不需要任何产品代码改动即可部署(网页端「模型连接」配置是独立的功能增强,见文末)。
2. **Substrate 不适合现在并入 EasyAgent 主线。** 它要求 plain containers 之外的自定义沙箱运行时、自建控制面(PostgreSQL、Envoy、证书体系),是"一个平台"而非"一个库"。个人助手阶段引入它属于过度设计。
3. **它是最值得对标的企业级参照系。** 会话快照、出口策略、digest 锁定的不可变模板、策略随 Actor 生灭——这些机制设计可以直接反哺 EasyAgent 未来"多实例托管"(Fleet)形态的规划。

## 实操:五步跑通

以下路径在 2026-11 于 kind 集群(Kubernetes v1.37,Substrate main,Go 1.27)完整验证。第 0 步的集群搭建与控制面部署用[官方 Quickstart](https://github.com/agent-substrate/substrate#readme),此处不重复。

### 1. 构建 linux 二进制

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-X main.version=$(git describe --tags)" \
  -o bin/easyagent ./cmd/easyagent
```

静态编译即可,容器内无需 glibc 依赖。

### 2. 镜像:注入出口网关的 MITM CA

沙箱出 HTTPS 会被出口网关 MITM 解密审查(用自建 CA 签证书),镜像必须信任这张 CA,否则所有 HTTPS 工具报 `certificate signed by unknown authority`:

```dockerfile
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY bin/easyagent /usr/local/bin/easyagent
# substrate 安装时创建的 MITM CA(ate-system/egress-mitm-ca-pool 的 tls.crt)
COPY mitm-ca.crt /usr/local/share/ca-certificates/substrate-mitm.crt
RUN update-ca-certificates
ENV EA_PROVIDER=openai
ENV EA_BASE_URL=http://gateway.host:4001
ENV EA_SERVER_API_KEY=<网页登录令牌>
EXPOSE 8080
CMD ["/usr/local/bin/easyagent", "--mode", "serve", "--listen", ":8080"]
```

构建后推入 Substrate 随 kind 部署的本地镜像仓库(`localhost:5001`)。

### 3. ActorTemplate:不可变,digest 锁定

```yaml
metadata:
  atespace: easyagent-demo
  name: easyagent-serve
workerSelector:
  matchLabels:
    workload: counter          # 匹配 WorkerPool 的 metadata.labels,不是 pod 标签
containers:
- name: easyagent
  image: localhost:5001/easyagent-serve@sha256:<必填,digest 锁定>
  env:
  - { name: EA_PROVIDER,        value: "openai" }
  - { name: EA_BASE_URL,        value: "http://gateway.host:4001" }
  - { name: EA_API_KEY,         value: "<网关密钥>" }
  - { name: EA_SERVER_API_KEY,  value: "<网页登录令牌>" }
resources:
  limits:
    cpuMillicores: 500         # 不要申请整核/整 GB:会塞不进 worker 剩余容量
    memoryBytes: 402653184     # 384Mi,配合 1Gi 的 worker 可与其它 Actor 混布
snapshotConfig:
  preferredFidelity: SNAPSHOT_FIDELITY_MEMORY
```

要点:

- **镜像必须 digest 锁定**,tag 会因"changing the image invalidates snapshots"被准入拒绝——快照与镜像内容强绑定,这是快照可恢复性的根基。
- **模板不可变**:改镜像/环境都要删除重建模板与 Actor。会话状态如需跨版本保留,用 `kubectl ate tag` 打快照标签后再重建。
- 模板 `env` 里的密钥会进快照与仓库。生产形态应改用 Substrate 的[出口凭证注入](https://github.com/agent-substrate/substrate)(沙箱零凭证,PEP 按需替换),本文为演示简化。

### 4. Actor、EgressPolicy、代理

```bash
export KUBECONFIG=~/.kube/config PATH="$PATH:$HOME/go-workspace/bin"
kubectl ate create atespace easyagent-demo
kubectl ate create actor easyagent-1 -a easyagent-demo --template easyagent-serve

# 策略必须在 Actor 首次启动前挂好:沙箱 DNS relay 只解析策略内主机名,
# 无策略或名字不匹配一律 NXDOMAIN。策略是 Actor 的子资源,删 Actor 策略随之消失。
cat > /tmp/egress.yaml <<'EOF'
metadata: { atespace: easyagent-demo, name: default }
rules:
- http:
    hostnames: ["gateway.host"]     # 出口网关(集群 DNS hosts 映射到宿主机)
    ports: { numbers: [4001] }
- https:
    hostnames: ["*"]                # 外网 HTTPS 全放行,经 PEP MITM 审查
    ports: { all: {} }
- http:
    hostnames: ["*"]
    ports: { numbers: [80] }        # 明文 80(拿 301 跳转用)
EOF
kubectl ate create egress-policy easyagent-1 -f /tmp/egress.yaml -a easyagent-demo

# 本地代理:浏览器/工具流量直达 Actor,自动唤醒
nohup kubectl ate proxy actor easyagent-1 18001:8080 -a easyagent-demo &
```

注意 `http "*"` 与 `https "*"` **同模式同端口(全端口)互斥**,准入会以 tie 拒绝;明文规则必须限定端口(如仅 80),或干脆不要明文规则。

策略更新走 `kubectl ate update egress-policy`,需携带 `get -o yaml` 输出的 `metadata.uid` 与 `version`(乐观锁)。

### 5. 验证

`GET /models` 返回网关模型目录且无 `discovery_error` = 沙箱 → PEP → 网关全链路通。生命周期实验:

```bash
kubectl ate suspend actor easyagent-1 -a easyagent-demo --mode=sync   # STATE→SUSPENDED,worker 释放
kubectl ate get actors -a easyagent-demo                              # WORKER POD 列 <none>
# 此时刷新网页:自动解冻。实测解冻 ~0.15s,且可能被调度到与冻结时不同的 worker(Actor Teleport)
```

## 踩坑清单

每一条都在本次部署中真实命中,排查过程见对应层日志:

| # | 现象 | 根因 | 解法 |
|---|---|---|---|
| 1 | 模板创建被拒:`must be pinned by digest` | 浮动 tag 会使快照失效 | 镜像按 `@sha256:` 锁定 |
| 2 | 首次请求 503:`no worker has room` | Actor 申请的资源 ≈ worker 总容量(worker 还要跑 ateworker 管理进程) | 资源申请留余量,如 500m / 384Mi |
| 3 | 同上仍被拒 | `workerSelector` 匹配的是 **WorkerPool 的 metadata.labels**,不是 worker pod 标签 | 用池标签(如 `workload: counter`) |
| 4 | 外部域名全部 `no such host` | 沙箱 DNS relay 只解析 EgressPolicy 内主机名,其余 NXDOMAIN(169.254.17.1 是沙箱内置 relay,非故障) | 挂策略;这是零信任的设计行为 |
| 5 | 策略内域名仍解析失败:`dns_resolution_failure{cares_failure:DNS_server_returned_general_failure}` | PEP 用 c-ares 按 ndots 展开搜索域,某变体被 forward 到宿主机上游返回 SERVFAIL,**中断整条搜索链**,永远走不到裸名查询 | CoreDNS 加 `template IN ANY localdomain { rcode NXDOMAIN }`,把软错误还给搜索链 |
| 6 | HTTPS 全部 `unknown authority` | 出口网关 MITM 的私有 CA 不在镜像信任链 | Dockerfile 注入 CA 并 `update-ca-certificates` |
| 7 | 明文 HTTP 外站全 403 | 非 PEP 故障:策略未放行的明文请求按契约必须返回 403 | 加 http 规则或接受拒绝 |
| 8 | 删除重建 Actor 后策略失效 | EgressPolicy 是 Actor 子资源,随 Actor 删除而 GC | 每次重建 Actor 后重新创建策略 |
| 9 | 策略更新报缺 `metadata.uid/version` | 替换操作带乐观锁,防并发覆盖 | 先 `get -o yaml` 取 uid+version 再提交 |

另有一条集群层知识:CoreDNS 的 kubernetes 插件**不解析 ExternalName Service**,给宿主机起 DNS 别名要用 hosts 块(`172.20.0.1 gateway.host`)。

## 安全模型对照:EasyAgent 现状 vs Substrate 机制

| 维度 | EasyAgent 现状(个人助手) | Substrate 机制 | 差距含义 |
|---|---|---|---|
| 进程隔离 | 信任运行用户 + systemd 加固 + 可选容器 | 每 Actor 一个 gVisor/microVM 沙箱,内核级 | 多租户的前提 |
| 密钥边界 | `EA_*` 环境变量/.env 进进程与配置文件 | 沙箱零凭证,PEP 按出口策略注入 | 被注入的 agent 无物可窃 |
| 出口管控 | `EA_ENABLE_WEB` 等粗粒度开关 | per-Actor 策略文件,主机名+端口粒度,可热更新(乐观锁) | 策略即代码,可审计 |
| 状态持久化 | 会话存本地文件,进程死即依赖磁盘 | 快照三层保真(内存/文件/rootfs),跨 worker 迁移 | 冻结-复活-迁移成为调度原语 |
| 模型接入 | EasyGateway(地址+密钥在配置里) | 出口策略指向网关,网关持密钥 | **EasyGateway 已天然是这套形态的模型平面** |

## 企业级差距:EasyAgent 还缺什么

这次探路验证了"能跑",也量出了"个人助手 → 企业级"的落差。若规划多实例托管(Fleet)形态,至少要补:

1. **控制面**:租户/Actor 的生命周期管理、策略下发、快照存储——Substrate 证明了这套东西的复杂度(ateapi + PostgreSQL + Envoy + 证书体系),自研前应先评估直接采用。
2. **策略化出口**:把 `EA_ENABLE_WEB` 的布尔开关升级为 per-实例的域名/端口策略文件,并让网关类凭据退出沙箱(凭证注入)。这一步不需要 Substrate,普通容器 + Envoy 也能做,是性价比最高的中间站。
3. **快照会话**:对话与工具状态的可冻结性,依赖"不依赖进程外实时连接"的设计约束——本地执行的 bash 工具天然兼容,长连接类工具(MCP SSE、SSH)需要重连协议。
4. **密钥治理**:`EA_SERVER_API_KEY` 已是服务令牌与上游密钥分离的雏形,继续沿"凭据边界"思路把模型密钥从 Agent 侧剥离到网关侧即可——EasyGateway 补上 per-租户配额与审计后,模型平面即就绪。

近期结论:EasyAgent 保持个人助手定位不变;上述第 2、4 点与 EasyGateway 的演进天然重合,可作为网关路线的一部分推进;完整 Fleet 形态等真实多租户需求出现再立项,届时 Substrate 是首选参照与候选运行时。

## 复现环境

- Substrate main(2026-11),kind 集群 Kubernetes v1.37,/dev/kvm 直通(gVisor + microVM 双沙箱类)
- EasyAgent v0.56 起(`--mode serve --listen :8080`),镜像 `debian:bookworm-slim` + `ca-certificates` + MITM CA
- 资源占用:Actor 500m / 384Mi;worker 1C/1Gi ×3 与 counter demo 混布;宿主 16C/14GB(桌面机)可承载
- 关联文档:[安全模型](./SECURITY_MODEL.md) · [配置](./CONFIG.md) · [EasyGateway](https://nightgopher.github.io/easy-suite/docs/gateway_overview/)
