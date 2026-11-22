# EasyAgent

**A Go-powered personal AI coding assistant and agent workbench — native desktop, Android, web, terminal TUI, Feishu, and MCP.**

[中文文档](README.md) · Website: [Easy Suite](https://nightgopher.github.io/easy-suite/) · Docs: [Online Docs](https://nightgopher.github.io/easy-suite/docs/)

EasyAgent reads and edits code, runs tools with explicit approval, and keeps resumable sessions across a native desktop client, an Android app, a browser workspace, a terminal TUI, and Feishu chats. Its agent core (providers, tools, sessions, context compaction, skills, local/SSH execution) is reusable as a public Go SDK.

![EasyAgent Desktop](https://nightgopher.github.io/easy-suite/screenshots/easyagent-desktop.webp)

> Documentation is Chinese-first. See [README.md](README.md) and [docs/](docs/) for the full manuals; this page covers the essentials in English.

## Install

### One-line CLI install (macOS / Linux)

```bash
curl -fsSL https://raw.githubusercontent.com/nightgopher/easyagent/main/scripts/install.sh | bash
```

### Desktop, Mobile & Server

Download installers from [GitHub Releases](https://github.com/nightgopher/easyagent/releases/latest) or the [Easy Suite Download Page](https://nightgopher.github.io/easy-suite/download/):

- **macOS (Apple Silicon)** — `easyagent-native-darwin-arm64.dmg` (includes one-click `easyagent` CLI symlink in Settings).
- **Linux desktop** — `easyagent-native-linux-<arch>.deb`, or `tar.gz` + user-level install script.
- **Servers (Debian/Ubuntu)** — `easyagent-server_<version>-1_<arch>.deb` registers a systemd service on `127.0.0.1:8080` and generates a random `EA_SERVER_API_KEY`.
- **Android (8.0+)** — [easyagent-android.apk](https://github.com/nightgopher/easyagent/releases/latest/download/easyagent-android.apk) or [ZIP mirror](https://github.com/nightgopher/easyagent/releases/latest/download/easyagent-android.zip).

## Quickstart

Minimal configuration for an OpenAI-compatible upstream or [EasyGateway](https://github.com/nightgopher/easygateway) (`EA_*` prefix):

```dotenv
EA_PROVIDER=openai
EA_BASE_URL=http://localhost:4001
EA_MODEL=your-model-id
EA_API_KEY=your-api-key
```

Run the terminal TUI or web workspace:

```bash
easyagent                                 # interactive TUI
easyagent serve --listen 127.0.0.1:8080   # web workspace & HTTP API
```

Or build from source (requires Go — see [go.mod](go.mod)):

```bash
git clone https://github.com/nightgopher/easyagent.git
cd easyagent
make build
cp .env.example .env && ./bin/easyagent
```

## Security

Report vulnerabilities privately via GitHub's **Report a vulnerability** button on the [Security tab](https://github.com/nightgopher/easyagent/security) — see [SECURITY.md](SECURITY.md). Please do not open public issues for security problems.

## Contributing

See [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) (Chinese). Every PR runs the full verify pipeline: Go tests, vet, golangci-lint with a shrink-only legacy baseline, race + coverage gates, govulncheck, and Android unit/lint/build checks for client changes. Keep changes minimal and add regression tests for behavior you touch.
