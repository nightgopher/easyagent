# EasyAgent

**A Go-powered personal AI coding assistant and agent workbench — desktop, Android, web, terminal, Feishu and MCP.**

EasyAgent reads and edits code, runs tools with explicit approval, and keeps resumable sessions across a native desktop client, an Android app, a browser workspace, a terminal TUI and Feishu chats. The agent core (providers, tools, sessions, context compaction, skills, local/SSH execution) is reusable as a public Go SDK.

> Documentation is Chinese-first. See [README.md](README.md) and [docs/](docs/) for the full manuals; this page only covers the essentials in English.

## Quickstart from source

Requires Go (see [go.mod](go.mod) for the exact version).

```bash
git clone https://github.com/hwj123hwj/easyagent.git
cd easyagent
make build
cp .env.example .env    # then fill in your provider, model and API key
./bin/easyagent         # terminal chat
```

Minimal configuration for an OpenAI-compatible upstream:

```dotenv
EA_PROVIDER=openai
EA_BASE_URL=http://localhost:4001
EA_MODEL=your-model-id
EA_API_KEY=your-api-key
```

To serve the web workspace instead of the terminal UI:

```bash
./bin/easyagent serve --listen 127.0.0.1:8080
```

All configuration uses the `EA_*` prefix — see the [configuration reference](docs/CONFIG.md) (Chinese) for providers, tool permissions, paths and the HTTP API key.

## Install a release

- **macOS (Apple Silicon)** — download [`easyagent-native-darwin-arm64.dmg`](https://github.com/hwj123hwj/easyagent/releases/latest) from the latest release.
- **Linux desktop** — `easyagent-native-linux-<arch>.deb`, or the self-updating `tar.gz` + install script.
- **Servers (Debian/Ubuntu)** — `easyagent-server_<version>-1_<arch>.deb` registers a systemd service on 127.0.0.1:8080 and generates a random `EA_SERVER_API_KEY` on first install.
- **Android (8.0+)** — [easyagent-android.apk](https://github.com/hwj123hwj/easyagent/releases/latest/download/easyagent-android.apk) or the [ZIP mirror](https://github.com/hwj123hwj/easyagent/releases/latest/download/easyagent-android.zip).

## Security

Report vulnerabilities privately via GitHub's **Report a vulnerability** button on the [Security tab](https://github.com/hwj123hwj/easyagent/security) — see [SECURITY.md](SECURITY.md). Please do not open public issues for security problems.

## Contributing

See [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) (Chinese). Every PR runs the full verify pipeline: Go tests, vet, golangci-lint with a shrink-only legacy baseline, race + coverage gates, govulncheck, and Android unit/lint/build checks for client changes. Keep changes minimal and add regression tests for behavior you touch.
