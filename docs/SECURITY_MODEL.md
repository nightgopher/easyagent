# 安全模型与部署加固

EasyAgent 以启动它的系统用户身份运行，工具能做的事不超过这个用户的权限。本页说明哪些检查是安全边界、哪些只是提醒，以及远程部署时应当加上的隔离和传输加密。

## 威胁模型

主要风险来自**提示注入**：网页（`web_fetch`）、MCP 工具结果、仓库里的文件都可能包含诱导模型执行命令的文字。模型一旦被诱导，能动用的就是当前启用的工具。

| 机制 | 是否安全边界 | 说明 |
|---|---|---|
| API 令牌（`EA_SERVER_API_KEY`） | 是 | 非回环访问必须携带；常量时间比较，错误令牌过多会临时封禁来源 |
| 文件工具路径策略 | 是 | `read`/`write`/`edit` 等默认限制在工作区，检查绝对路径、相对路径与符号链接；`EA_ALLOW_OUTSIDE_WORKSPACE` 才放开 |
| 工具审批 | 部分 | serve 模式下写文件、删文件、未信任的 MCP 工具需要客户端批准；`-y` / `EA_AUTO_APPROVE=true` 会全部跳过 |
| Bash 危险命令识别 | **否** | bash 只有命中危险规则时才请求审批，普通命令直接执行。规则只认出常见的破坏性写法（递归删除、覆盖重定向、提权、`curl \| sh`、`eval` 等）；变量拼接、脚本文件、别名等写法都能绕过，**没命中不代表命令安全** |
| Bash 工具本身 | **否** | 启用 `EA_ENABLE_BASH` 就等于把运行用户的 shell 交给模型，路径策略对它不生效 |

EasyAgent 不在工具内部实现进程级沙箱（bwrap、sandbox-exec 等）。这与 [pi](https://github.com/earendil-works/pi) 的取舍一致：隔离交给操作系统和容器做，比在工具里维护一套不完整的规则更可靠。

## 推荐的部署组合

**本机使用（桌面客户端、`easyagent chat`）**：保持工具审批开启，不要长期使用 `-y`；需要让 agent 跑不受信任的仓库时，用下面的容器方式。

**远程主机 / 迷你主机**：

1. 用专门的系统用户运行，不要用自己的登录用户，也不要用 root。
2. 优先使用正式发布的服务端 deb 包。它自带的 systemd 单元已经做了以下限制：
   - 文件系统只读（`ProtectSystem=strict`），只有 `/var/lib/easyagent` 可写；
   - 看不到 `/home`（`ProtectHome=true`），所以 `~/.ssh`、`~/.aws` 这类凭据对 bash 不可见；
   - 不能提权（`NoNewPrivileges`、空的 `CapabilityBoundingSet`、`RestrictSUIDSGID`）；
   - 独立的 `/tmp` 和 `/dev`，不能改内核参数、模块、时钟和主机名。
   需要让 agent 访问其他目录时，用 `systemctl edit easyagent` 追加 `ReadWritePaths=`，不要直接关掉这些选项。
3. 需要更强的隔离（例如让 agent 处理来路不明的仓库），把整个服务放进容器，只挂载工作目录：

   ```bash
   docker run --rm -it \
     --user "$(id -u):$(id -g)" \
     -v "$PWD:/work" -w /work \
     -e EA_PROVIDER -e EA_MODEL -e EA_BASE_URL -e EA_API_KEY \
     -e EA_SERVER_API_KEY -e EA_ENABLE_BASH=true \
     -p 127.0.0.1:8080:8080 \
     <装有 easyagent 的镜像> easyagent serve --listen :8080
   ```

   容器里看不到宿主机的家目录和凭据；端口只绑定到 `127.0.0.1`，再由下面的反向代理对外提供 HTTPS。

## HTTPS

`easyagent serve` 只提供明文 HTTP。监听非回环地址时会在启动日志里给出警告。令牌通过 `Authorization` 头传输，明文链路上任何能抓包的人都能拿到它，所以**跨网络访问必须经过 HTTPS 反向代理**，或者只在可信的内网 / VPN（如 Tailscale）里使用。

做法是让 EasyAgent 只监听回环地址，由反向代理终止 TLS：

```bash
easyagent serve --listen 127.0.0.1:8080
```

**Caddy**（自动申请证书）：

```caddyfile
agent.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

**Nginx**（流式输出和 WebSocket 需要关闭缓冲并转发升级头）：

```nginx
server {
    listen 443 ssl;
    server_name agent.example.com;
    ssl_certificate     /etc/letsencrypt/live/agent.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/agent.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_buffering off;
        proxy_read_timeout 1h;
    }
}
```

反向代理转发过来的请求来源是 `127.0.0.1`，EasyAgent 会把它当作本机请求：没设令牌时本机请求免认证，错误令牌限流也不对回环来源生效（不信任 `X-Forwarded-For`）。所以使用反向代理时**必须设置 `EA_SERVER_API_KEY`**，并在代理上自行限制请求频率。

桌面端和手机端连接远程主机时，地址填 `https://agent.example.com`。
