#!/usr/bin/env bash
#
# EasyAgent 服务端 .deb 打包脚本（easyagent serve + 飞书桥接）
#
# 用法:
#   scripts/build-server-deb.sh <version> <arch> [output-dir]
#
#   version   deb 版本号（不带 v 前缀，如 0.21.0；自动追加 debian 修订号 -1）
#   arch      目标架构: amd64 | arm64
#   output    输出目录（默认 dist）
#
# 环境变量:
#   CORE_BIN   使用预编译的 easyagent 核心二进制（默认从 dist/easyagent-linux-<arch> 取）
#   BRIDGE_BIN 使用预编译的 easyagent-bridge 二进制（默认从 dist/easyagent-bridge-linux-<arch> 取）
#
# 产物布局（系统级安装）:
#   /usr/bin/easyagent                            核心二进制（serve 模式 + CLI）
#   /usr/bin/easyagent-bridge                     飞书桥接二进制
#   /etc/easyagent/easyagent.env                  环境变量（postinst 首次安装生成随机 EA_SERVER_API_KEY）
#   /var/lib/easyagent                            运行数据（会话、配置、工作目录）
#   /lib/systemd/system/easyagent.service         核心服务（自动启用）
#   /lib/systemd/system/easyagent-bridge.service  桥接服务（配置飞书后手动启用）
#
set -euo pipefail

usage() { sed -n '2,22p' "$0" >&2; exit 1; }

[ $# -ge 2 ] || usage
VERSION="$1"
ARCH="$2"
OUT_DIR="${3:-dist}"

case "$ARCH" in
    amd64|arm64) ;;
    *) echo "不支持的架构: $ARCH（仅支持 amd64/arm64）" >&2; exit 1 ;;
esac

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CORE_BIN="${CORE_BIN:-${REPO_ROOT}/dist/easyagent-linux-${ARCH}}"
BRIDGE_BIN="${BRIDGE_BIN:-${REPO_ROOT}/dist/easyagent-bridge-linux-${ARCH}}"
[ -f "$CORE_BIN" ] || { echo "缺少核心二进制: $CORE_BIN（先运行 scripts/build-release.sh 或设置 CORE_BIN）" >&2; exit 1; }
[ -f "$BRIDGE_BIN" ] || { echo "缺少桥接二进制: $BRIDGE_BIN（先运行 scripts/build-release.sh 或设置 BRIDGE_BIN）" >&2; exit 1; }

STAGING="$(mktemp -d /tmp/easyagent-server-deb.XXXXXX)"
trap 'rm -rf "$STAGING"' EXIT

DEB_VERSION="${VERSION}-1"
PKG="easyagent-server_${DEB_VERSION}_${ARCH}"

# ── 1. 二进制 ──
mkdir -p "${STAGING}/usr/bin"
install -m 0755 "$CORE_BIN" "${STAGING}/usr/bin/easyagent"
install -m 0755 "$BRIDGE_BIN" "${STAGING}/usr/bin/easyagent-bridge"

# ── 2. systemd 服务 ──
mkdir -p "${STAGING}/lib/systemd/system"
install -m 0644 "${REPO_ROOT}/deploy/systemd/easyagent.service" "${STAGING}/lib/systemd/system/easyagent.service"
install -m 0644 "${REPO_ROOT}/deploy/systemd/easyagent-bridge.service" "${STAGING}/lib/systemd/system/easyagent-bridge.service"

# ── 3. 数据目录（属主由 postinst 调整） ──
mkdir -p "${STAGING}/var/lib/easyagent"

# ── 4. 控制信息与维护脚本 ──
mkdir -p "${STAGING}/DEBIAN"
cat > "${STAGING}/DEBIAN/control" <<EOF
Package: easyagent-server
Version: ${DEB_VERSION}
Section: net
Priority: optional
Architecture: ${ARCH}
Depends: adduser
Maintainer: WJ Huang <130386673+hwj123hwj@users.noreply.github.com>
Homepage: https://github.com/hwj123hwj/easyagent
Description: EasyAgent core server and Feishu bridge
 Personal AI agent server: 'easyagent serve' exposes the HTTP/WebSocket
 API consumed by the EasyAgent desktop, web and mobile clients, with
 coding, music and knowledge-base agents on top of the EasyAgent SDK.
 Includes the optional Feishu (Lark) bridge binary and systemd units;
 the core service runs as the dedicated "easyagent" user.
EOF

cat > "${STAGING}/DEBIAN/postinst" <<'EOF'
#!/bin/sh
# 首次安装：创建系统用户、数据目录和带随机 EA_SERVER_API_KEY 的环境文件；
# 随后 reload/enable/start（升级路径为 try-restart）。桥接服务默认不启用。
set -e

PKG_USER=easyagent
PKG_GROUP=easyagent
DATA_DIR=/var/lib/easyagent
ENV_FILE=/etc/easyagent/easyagent.env
SERVICE=easyagent.service

case "$1" in
    configure)
        if ! getent group "$PKG_GROUP" >/dev/null 2>&1; then
            addgroup --system --quiet "$PKG_GROUP"
        fi
        if ! getent passwd "$PKG_USER" >/dev/null 2>&1; then
            adduser --system --ingroup "$PKG_GROUP" --home "$DATA_DIR" \
                --no-create-home --shell /usr/sbin/nologin --quiet "$PKG_USER"
        fi
        install -d -o "$PKG_USER" -g "$PKG_GROUP" -m 0750 "$DATA_DIR"

        if [ ! -f "$ENV_FILE" ]; then
            API_KEY="$(head -c 24 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 32)"
            umask 027
            install -d -m 0755 /etc/easyagent
            cat > "$ENV_FILE" <<EOT
# EasyAgent 服务端环境变量（首次安装自动生成）
# 管理 API 认证（桌面端/网页连接远程主机时使用）
EA_SERVER_API_KEY=${API_KEY}

# 监听地址与端口；远程访问改为 0.0.0.0 并确保 EA_SERVER_API_KEY 已设置
EA_HOST=127.0.0.1
EA_PORT=8080

# 模型接入：openai 指向 OpenAI 兼容端点（默认本机 EasyGateway :4001），
# 或改为 anthropic；API Key 按需填写。
EA_PROVIDER=openai
# OPENAI_API_KEY=
# OPENAI_BASE_URL=http://localhost:4001

# 运行数据与主目录（会话、运行配置）
EA_HOME=/var/lib/easyagent
EA_DATA_DIR=/var/lib/easyagent/data

# 飞书桥接（使用 easyagent-bridge.service 时配置；详见 docs/MINI_DEPLOY.md）
# EA_FEISHU_ENV_FILE=/etc/easyagent/feishu.env
# FEISHU_OWNER_STATE_FILE=/var/lib/easyagent/feishu-owner.json
EOT
            chown root:"$PKG_GROUP" "$ENV_FILE"
            chmod 0640 "$ENV_FILE"
            echo "easyagent-server: 已生成配置 $ENV_FILE（含随机 EA_SERVER_API_KEY）"
        fi

        if [ -d /run/systemd/system ]; then
            systemctl --system daemon-reload >/dev/null || true
            deb-systemd-helper enable "$SERVICE" >/dev/null 2>&1 || true
            if [ -n "$2" ]; then
                deb-systemd-invoke try-restart "$SERVICE" >/dev/null || true
            else
                deb-systemd-invoke start "$SERVICE" >/dev/null || true
            fi
        fi
        ;;
esac
EOF

cat > "${STAGING}/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e

if [ -d /run/systemd/system ] && [ "$1" = remove ]; then
    deb-systemd-invoke stop easyagent-bridge.service >/dev/null 2>&1 || true
    deb-systemd-invoke stop easyagent.service >/dev/null || true
fi
EOF

cat > "${STAGING}/DEBIAN/postrm" <<'EOF'
#!/bin/sh
set -e

if [ "$1" = purge ]; then
    deb-systemd-helper purge easyagent.service >/dev/null 2>&1 || true
    deb-systemd-helper unmask easyagent.service >/dev/null 2>&1 || true
    deb-systemd-helper purge easyagent-bridge.service >/dev/null 2>&1 || true
    deb-systemd-helper unmask easyagent-bridge.service >/dev/null 2>&1 || true
    rm -f /etc/easyagent/easyagent.env
    rmdir --ignore-fail-on-non-empty /etc/easyagent 2>/dev/null || true
    rm -rf /var/lib/easyagent
    if getent passwd easyagent >/dev/null 2>&1; then
        userdel easyagent >/dev/null 2>&1 || true
    fi
    if getent group easyagent >/dev/null 2>&1; then
        groupdel easyagent >/dev/null 2>&1 || true
    fi
fi
EOF

chmod 0755 "${STAGING}/DEBIAN/postinst" "${STAGING}/DEBIAN/prerm" "${STAGING}/DEBIAN/postrm"

# ── 5. 文档 ──
DOC_DIR="${STAGING}/usr/share/doc/easyagent-server"
mkdir -p "${DOC_DIR}"
install -m 0644 "${REPO_ROOT}/README.md" "${DOC_DIR}/README.md"
gzip -9n -c "${REPO_ROOT}/docs/MINI_DEPLOY.md" > "${DOC_DIR}/mini-deploy.md.gz"
cat > "${DOC_DIR}/copyright" <<EOF
Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: easyagent
Source: https://github.com/hwj123hwj/easyagent

Files: *
Copyright: 2026 WJ Huang <130386673+hwj123hwj@users.noreply.github.com>
License: MIT
EOF
gzip -9n > "${DOC_DIR}/changelog.Debian.gz" <<EOF
easyagent-server (${DEB_VERSION}) unstable; urgency=medium

  * Release ${VERSION}.

 -- WJ Huang <130386673+hwj123hwj@users.noreply.github.com>  $(date -R)
EOF

# ── 6. 规范化权限（避免宿主 umask 影响包内权限） ──
find "${STAGING}" -type d -exec chmod 0755 {} +
find "${DOC_DIR}" -type f -exec chmod 0644 {} +

# ── 7. 构建 .deb ──
mkdir -p "${OUT_DIR}"
DEB_PATH="${OUT_DIR}/${PKG}.deb"
rm -f "${DEB_PATH}"
dpkg-deb --root-owner-group --build "${STAGING}" "${DEB_PATH}"
echo "构建完成: ${DEB_PATH}"
