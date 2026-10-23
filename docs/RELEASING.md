# 版本与发布

## 版本规则

采用 [Semantic Versioning](https://semver.org/)，仓库 tag 加 `v` 前缀。正式版使用 `vMAJOR.MINOR.PATCH`，预发布仅使用 `vMAJOR.MINOR.PATCH-alpha.N`、`-beta.N` 或 `-rc.N`（N 从 1 开始）。不使用日期标签、`latest` 浮动标签或版本号中的前导零。

- PATCH：兼容的错误修复。
- MINOR：新增能力；0.x 阶段的不兼容变更也提升 MINOR，并在更新记录说明迁移要求。
- MAJOR：1.0 之后不兼容的公开接口变更。当前仍处于 0.x，不为发布流程整改直接跳到 1.0。
- 预发布用于验收，不标记为 GitHub Latest；正式发布只推进版本，不倒退。

公开接口包括 CLI 参数、配置名、HTTP 协议及 `sdk/`。客户端版本由 `desktop-native/mygo.json` 定义，Android 开发构建也从此读取。从 v0.16.0 起发布 Go 核心、桥接与 MyGo 原生桌面 DMG；Electron 已退役。从 v0.19.0 起同时分发固定证书签名的 Android APK，v0.19.2 起增加包含同一 APK 的 ZIP 备用下载包。从 v0.21.0 起分发 Linux 桌面 deb / 自更新 tar.gz 与服务端 easyagent-server deb（双架构）。客户端版本必须与 tag 去掉 `v` 后一致，随包核心使用同一 tag 版本。历史版本资产要求保留在 `scripts/release.py`，已发布资产不变。

2026-10-01 按维护者明确要求，在备份旧 Pi Agent 的 Release 元数据、全部资产和 Git 标签对象后，清理 `v0.10.0` 至 `v0.11.0` 的公开 Release 与 tag，以 EasyAgent `v0.1.0` 重新开始。这是更名时的一次性迁移；提交历史不清理。后续**一个 tag 永远对应一个提交；不能删除重打、强推移动或覆盖已发布资产。** GitHub 的 Release immutability 对启用之后的新发布锁定资产；tag ruleset 保护 `v*` 标签免于移动、删除。[GitHub 不可变发布说明](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases)

## 两条分发路径

| 路径 | 标识与用途 |
|---|---|
| GitHub Release | 经验证的 tag；对外可下载版本，带明确变更说明与校验和 |
| 迷你主机持续部署 | 跟踪 main 的完整 SHA；日常开发更新，构建验证后重启与回滚 |

合并 PR 不等于发布新版本，也不需要为每个修复立刻打 tag。迷你主机保持当前 main 更新模式；不把它的 SHA 版本伪装成已发布 tag。服务暂停更新、回滚与自启动见 [部署运维](MINI_DEPLOY.md)。本地 `make build` 的 `git describe` 信息用于开发定位，带提交距离或 dirty 后缀的构建不是正式 Release。

## 准备版本

1. 选定版本，更新 [CHANGELOG.md](../CHANGELOG.md)：将本轮变化整理到精确的 `## [vX.Y.Z] - YYYY-MM-DD` 节，写明新功能、修复与迁移要求。保留下一轮 `Unreleased`，同步 `desktop-native/mygo.json` 的版本。
2. 通过 PR 合并代码和版本说明，等 CI 完成。在干净的主工作副本或专用临时 checkout 操作，不提交凭据、运行数据或本地审阅文件。
3. 同步 main 和 tags，创建附注 tag，显式推送该 tag：

```bash
git switch main
git fetch --prune --tags origin
git pull --ff-only origin main
# 示例版本；先按本次实际变化选择并提交对应 CHANGELOG 节。
python3 scripts/release.py tag v0.1.1-rc.1
git push origin refs/tags/v0.1.1-rc.1
```

脚本拒绝脏工作区、非 main、未同步远端、重复/倒退版本和缺失说明。支持手工 `git tag -s` 签名；CI 至少强制附注标签。不要执行 `git push --tags`，以免顺带发布本地实验标签。

`Release` 工作流也可从 Actions 手动触发：必须选 main，`tag` 填**已经存在**的附注 tag。手动运行会检出该 tag 的提交，不会拿 main 当前代码冒充指定版本。用 `GITHUB_TOKEN` 创建标签通常不会递归触发 push 工作流，此时应显式 dispatch，不能假设发布已经开始。[触发规则](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow)

## 自动发布的强制检查

1. 严格版本格式；附注 tag；HEAD 与 tag 一致；提交已进入 main；版本高于现有版本；对应 CHANGELOG 节非空。
2. 复用完整 Verify：Go 测试/vet、Bubble Tea、Web、更新器、发布脚本和 Android 单测 / Lint / Debug 构建；Android 发布构建还等待同一 SHA 的 API 31/34/35 UI 测试矩阵通过，并按 [Android 验收清单](QA_ANDROID.md) 记录候选版本和人工检查。Release build check 另在 Apple Silicon runner 构建原生客户端并执行实际 bundle 健康检查，在 Linux runner 使用一次性密钥演练签名 Release APK。
3. 同一 SHA 构建 Linux/macOS、amd64/arm64 的核心与桥接共 8 个程序，注入相同版本。原生 DMG 在 Apple Silicon runner 构建，核验包版本与 tag 一致、Bundle ID 为 `com.hwj.easyagent`，并启动实际 bundle 核心检查健康响应和子进程回收。
4. v0.21.0 起汇集 8 个核心及桥接程序、1 个原生 DMG、2 项桌面签名更新资产、1 个签名 Android APK 和 1 个 ZIP 备用包、2 个 Linux 桌面 deb、2 个 Linux 用户级自更新 tar.gz 及配套 install 脚本与签名清单、2 个服务端 deb，加上 `release.json` 与 `checksums.txt`，共 25 项资产；v0.19.2 至 v0.20.0 保留历史的 15 项，v0.19.0 至 v0.19.1 为 14 项，v0.16.0 至 v0.18.1 为 13 项。`core_only` 清单不能发布；缺少必需文件、空文件、额外旧 Electron 文件或校验失败均阻止发布。Linux 桌面 deb 由核心 job 之后的 Linux runner 构建（amd64 直接执行包内核心校验版本，arm64 依赖同一 prepare 流水线与 deb 元数据校验），签名更新清单使用与 macOS 相同的 `MYGO_UPDATER_PRIVATE_KEY` 并在打包目录内复核签名与 URL。服务端 deb 复用核心 job 已构建并校验过 vcs 元数据的两个 Linux 二进制。Android 构建另校验应用标识、版本、最低系统版本、非 Debug 属性与固定证书指纹；ZIP 须仅包含与独立 APK 字节完全一致的 `easyagent-android.apk`。
5. 先建草稿，上传全部资产，核对 GitHub 返回的各文件 digest，再一次性公开；已公开版本拒绝重传。失败只保留草稿供检查，不公开半套文件。

发布相关脚本变更还会运行 `Release build check`：在临时仓库创建仅供测试的 tag，实际构建所有核心目标与原生桌面目标，不向 GitHub 推送测试 tag，也不创建 Release。

发布失败后先看 Actions 日志。纯网络上传失败可以重跑同一 tag，继续未公开的草稿；若需要改代码或脚本，提交修复后使用新版本，不移动旧 tag。草稿中出现不匹配的提交或额外资产会拒绝发布，应先核查来源。

## 安装与核验

macOS 桌面从 Release 下载 `easyagent-native-darwin-arm64.dmg`（Apple Silicon）。原生客户端支持签名自动更新，也可通过 DMG 手动更新；从 v0.17.2 起安装包使用固定证书签名以保留系统授权；v0.17.1 及此前公开包使用 ad hoc 测试签名。尚未进行 Developer ID 签名或 Apple 公证，固定签名与 GitHub SHA-256 校验均不代替 Apple 的开发者认证和公证。

Linux 桌面自 v0.21.0 起随正式 Release 发布：Debian/Ubuntu 服务器或桌面机直接安装 `easyagent-native-linux-<架构>.deb`（系统级安装到 /opt/easyagent，应用菜单与 `easyagent-desktop` 命令入口；运行需 libgtk-3 与 libwebkit2gtk-4.1，包依赖已声明）；偏好用户级安装或需要应用内自动更新时，下载 `easyagent-<版本>-linux-<架构>.tar.gz` 配套 `easyagent-native-install-linux-<架构>.sh`，客户端通过签名清单 `update-linux-<架构>.json` 自更新。包内 UI 可执行为 `easyagent`、核心为 `easyagent-core`（同目录），`EASYAGENT_NATIVE_CORE` 仍可显式指定核心。历史上的 native-preview 预览 Release 保留原样但不再更新。

服务端自 v0.21.0 起提供 `easyagent-server_<版本>-1_<架构>.deb`：安装 easyagent 核心与飞书桥接并注册 systemd（`easyagent.service` 默认启用并监听 127.0.0.1:8080，`easyagent-bridge.service` 在配置飞书后手动启用）。首次安装生成 `/etc/easyagent/easyagent.env`（含随机 `EA_SERVER_API_KEY`，远程访问把 `EA_HOST` 改为 0.0.0.0 并设置模型凭据）；升级保留该文件，`dpkg --purge easyagent-server` 移除全部数据。迷你主机持续部署（跟踪 main 的更新器）与 deb 稳定版是两条独立通道，同一主机只保留一条。

安装脚本默认选择最新正式 Release，也可显式指定已有版本：

```bash
EA_VERSION=v0.1.0 bash scripts/install.sh
```

脚本校验 SHA-256 和程序报告的版本后才替换文件，新版同时安装核心与桥接。下载或校验失败保留旧安装；不再偷偷回退到 main 编译。安装器仍识别旧 `pi-agent-*` 资产，但本仓库旧公开 Release 已在更名迁移中清理。旧程序的 `--version` 存在启动副作用，因此安装器不会执行它来探测版本。

安装器适合 CLI 安装，不负责生产服务的并发切换；正在运行的迷你主机继续使用其带锁和健康回滚的更新器。

SHA-256 用于检测下载损坏，信任根仍是 GitHub HTTPS 和发布权限。新版 Release 同时受 GitHub 不可变发布保护；下载完整资产后可运行 `sha256sum --check checksums.txt` 核验，macOS 可用 `shasum -a 256 -c checksums.txt`。

## Android 签名与发布

APK 名称固定为 `easyagent-android.apk`，应用标识为 `com.hwj.easyagent.mobile`，要求 Android 8.0 / API 26 及以上。从 v0.19.0（含其预发布）起 APK 是完整 Release 的必需资产；从 v0.19.2（含其预发布）起还须提供 `easyagent-android.zip`，供直接下载 APK 反复中断的手机浏览器改用 ZIP。解压后安装其中的 `easyagent-android.apk`；构建、生成清单及上传前均检查 ZIP 中只有这一文件，且与独立 APK 完全一致。两种格式与其他文件一并上传到草稿、验证 digest 后公开，不补传到已经锁定的旧 Release。

正式构建需要 Actions Secret `ANDROID_KEYSTORE`（PKCS#12 文件的 Base64）和 `ANDROID_KEYSTORE_PASSWORD`；私钥别名为 `easyagent`，密钥与 keystore 使用相同密码。私钥不进入仓库，公开证书 SHA-256 固定于 [signing-cert.sha256](../mobile-native/signing-cert.sha256)。临时 keystore 权限为 0600，构建结束自动删除。发布脚本调用 Android 官方 [apksigner](https://developer.android.com/tools/apksigner) 验证签名，并拒绝不匹配的证书。

维护者的原始 keystore 与密码保存在本机 `~/Library/Application Support/EasyAgent Signing/android/`，目录仅当前用户可读，应通过自己的私有备份保管；丢失签名私钥会影响覆盖更新。更换证书需先设计迁移方案，不直接替换指纹或改用 Debug 密钥。

`scripts/release.py` 将 SemVer 转换为 `major * 10000000 + minor * 100000 + patch * 1000 + phase * 100 + N`；alpha / beta / rc / 正式的 phase 分别为 0 / 1 / 2 / 3，正式 N 为 0。minor、patch、预发布 N 均不超过 99，最终 versionCode 不超过 Android 的 2100000000 限制；越界拒绝构建，避免冲突或降级。versionName 保留完整版本字符串（不含开头的 `v`）。

本地演练使用 `python3 scripts/build-android.py build <不存在的临时目录> --test-signing`，不要求正式私钥。正式构建只从已验证 tag 运行 `python3 scripts/build-android.py build dist --version "$TAG"`。需要 JDK 17、Android SDK 35、Build Tools 35.0.1。下载的正式 APK 可再次运行 `python3 scripts/build-android.py verify v0.19.0 /path/to/easyagent-android.apk` 校验身份与版本；完整文件校验仍以同一 Release 的 `checksums.txt` 为准。

## 旧 Electron 分发链路

Electron 的本机固定签名、自动下载重签名、自动安装和回滚链路随 `desktop/` 退役。原有应用、用户数据与钥匙串不会自动修改。需要恢复旧代码或查阅旧流程时，使用 `archive/workflow` 或退役前提交 `e4b4088`；不要把旧 Electron 安装包用于原生客户端更新。

## 仓库保护配置

配置保存在 [main 规则](../.github/rulesets/main.json) 与 [tag 规则](../.github/rulesets/release-tags.json)。main 要求 PR、最新基线的 GitHub Actions `verify` 成功且讨论已解决，禁止强推和删除。个人维护不强制另一个账号审批；发布仍受上述检查约束。

维护者修改规则时先更新仓库配置，再通过 GitHub Rulesets API 更新对应现有 ruleset（按名称查 ID，避免重复创建）。Release immutability 使用仓库设置开启；它只约束之后的新发布，不会重写历史资产。

### 原生自动更新

从 v0.16.0 开始，Release 还必须包含 `update-darwin-arm64.json` 和 `easyagent-<version>-darwin-arm64.tar.gz`。MyGo 使用 `desktop-native/mygo.json` 中的公开 Ed25519 密钥校验下载；私钥只从 CI Secret `MYGO_UPDATER_PRIVATE_KEY` 读取，禁止提交私钥。`build-native.sh` 在发布构建缺少密钥时失败，并在上传前验证版本、URL、大小和签名。更新清单随完整 Release 一起发布，不修改旧版本资产。

本机构建无需私钥，仍可检查已发布更新；签名发布演练使用 `scripts/check-native-release-build.sh` 的一次性测试密钥，不使用正式密钥或发布测试版本。首次上线需要发布包含新清单的正式版本；旧 Electron 安装不会通过此通道自动迁移。

### macOS 固定代码身份

v0.17.1 及此前的公开原生包使用 ad hoc 代码签名；其身份绑定具体构建，可能使更新后的屏幕录制、辅助功能授权失效。从 v0.17.2 起正式构建使用 `desktop-native/mygo.json` 中固定的 `macos.signingIdentity` 证书指纹，由 MyGo 在生成 DMG 和签名更新归档之前签署主应用和嵌套 Helper。构建后分别校验签名及指定要求，拒绝 ad hoc 或不同证书。

`Release` 的原生构建需要 `MACOS_CERTIFICATE`（包含私钥的 PKCS#12 文件的 Base64）和 `MACOS_CERTIFICATE_PASSWORD` 两项 Actions Secret。证书必须匹配配置中的公开指纹；私钥不进入源码、日志或发布资产。`MYGO_UPDATER_PRIVATE_KEY` 仍用于 Ed25519 更新包校验，与 macOS 代码签名证书相互独立。CI 在临时钥匙串中导入证书，构建结束后恢复搜索列表并删除临时钥匙串；不修改系统证书信任。

配置 Secret 后，可在 main 手动运行 `Release build check`，只验证真实签名证书和原生打包，不创建 tag 或 Release。PR 构建只使用一次性测试证书，不向 PR 提供正式签名私钥。

本机正式构建也可复用已有 `~/Library/Application Support/EasyAgent Signing/identity.json` 指定的私有钥匙串；证书必须匹配同一指纹，配置与密钥文件须仅当前用户可读。缺少证书时正式构建失败；不带 tag 的开发构建仍使用临时 ad hoc 签名。

从旧 ad hoc 身份迁移时，已有系统授权记录可能仍需用户确认一次；之后相同证书与 Bundle ID 的更新应满足之前的指定要求。此规则并不自动解决钥匙串的分区校验：本机证书签名的程序仍可能以每次构建的 `cdhash` 标识钥匙串客户端。

v0.23.1 起，原生包另含 `easyagent-credentials`，标识为 `com.hwj.easyagent.credentials`，使用同一固定证书。其构建不注入应用版本或 VCS 元数据，应用数据目录按代码哈希缓存已验证的程序；常规更新复用原钥匙串客户端。程序自身变化时，先由旧程序读取原条目，再由新程序写入独立服务并回读校验，保留原条目。调用双方验证运行进程的代码签名，凭据只通过匿名管道传递。缓存前驱是凭据迁移所需文件，不作为普通临时构建清理。

`scripts/check-native-keychain.py` 使用一次性证书和 `~/Library/Keychains` 下的独立测试钥匙串，实际验证不同应用构建、不同凭据程序、旧条目修复、清除、锁定、缓存篡改、调用方拒绝及 REST / WebSocket 认证。不能把测试钥匙串放在任意 `/tmp` 路径代替：那种旧格式会跳过分区校验，造成假通过。PR 原生构建和正式 Release 都执行该检查。构建演练还连续打包两个版本，确认主应用及电脑控制 Helper 的代码哈希已改变、指定要求保持稳定、凭据程序的代码哈希未改变。首次电脑控制授权与实际截图、控制能力仍需真实系统验收。固定本机证书不等同于 Apple Developer ID 或公证。
