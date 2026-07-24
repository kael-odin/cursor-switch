<div align="center">

# Cursor Assistant · cursor-byok

**Connect your own LLM API key to Cursor IDE while keeping your real Cursor account's marketplace / customize fully functional**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/kael-odin/cursor-byok?include_prereleases)](https://github.com/kael-odin/cursor-byok/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/kael-odin/cursor-byok/check.yml?branch=main&label=CI)](https://github.com/kael-odin/cursor-byok/actions)
[![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20macOS%20%7C%20Linux-lightgrey.svg)](#install--use)

📚 English · [中文](./README.md) · [Interface & Architecture](./docs/接口与架构速查.md) · [Refactor Notes](./docs/架构重构记录.md)

</div>

---

<img width="820" alt="screenshot" src="https://github.com/user-attachments/assets/2e1710b0-cdbd-4576-bd24-1614df016219" />
<img width="820" alt="screenshot" src="https://github.com/user-attachments/assets/00885453-6a91-4052-aadf-f686daeec881" />
<img width="820" alt="screenshot" src="https://github.com/user-attachments/assets/a607be84-a738-4e33-9750-13352e74001c" />

---

## ✨ What's new in 1.0.0

1.0.0 is an architecture-level refactor. The headline change: **your real Cursor account and byok custom models now coexist**.

### Problems in old versions (≤0.0.41)

- Forged a fake Ultra account written into Cursor's real local DB, **overwriting your real login** — you couldn't log in/out of your own Cursor account while byok was running
- marketplace / customize was mocked fake data — **visible but not installable** (plugin cards showed but install 404'd)
- Fake Ultra identity mixed with real account caused inconsistent UI state

### The 1.0.0 fix: three control planes, separated

| Plane | Handling | Result |
|---|---|---|
| **Identity / marketplace / auth** | Official passthrough (real Cursor account) | Login/logout/marketplace browse/install/uninstall/customize all work |
| **Subscription / plan / usage** | Local mock (unlimited Pro) | Model picker **not locked to auto** |
| **Model inference / data** | byok local (your provider key) | Custom model routing + cost tracking |

**Effect**: with byok running, you can use your own key/models **and** your real Cursor account's full marketplace (plugins / MCP / skills / subagents / rules / commands / hooks), and log in/out normally.

### Security enhancements

- **Per-machine CA**: no shared CA private key bundled; each machine gets its own on first launch
- **Mandatory update signing**: `update.json` uses ed25519 signature verification; a leaked release token can't forge an accepted update
- **Loopback trust separation**: internal trust uses a dedicated private header `X-Cursor-BYOK-Relay-Proof`, never `Authorization`; real Cursor credentials only go back to the original `*.cursor.sh`, never to third-party providers
- **Write-path fencing**: LLM file writes are confined to workspace/terminal dirs

---

## What it is

A desktop app (Wails v3 + Go backend + Vue 3 frontend) that runs a Cursor-compatible agent service locally and forwards Cursor client chat / agent requests to **your own configured model provider**. It is not a Cursor replacement; it is a local man-in-the-middle proxy plus a local agent execution kernel.

Use cases:
- Drive Cursor's chat and agent with a third-party OpenAI- / Anthropic-compatible API
- Use your real Cursor account's marketplace/customize while running models on your own key
- Self-host the full agent stack without platform lock-in

## How it works

1. **Local service**: starts an HTTP/Connect-RPC service exposing Cursor-compatible endpoints
2. **Traffic interception**: injects proxy settings into Cursor + installs a local CA certificate to route Cursor traffic to localhost
3. **Request routing**:
   - **Model/data-plane requests** → local backend compiles prompts, projects history, handles tool calls, then calls your configured provider
   - **Identity/marketplace/login requests** → passed through to Cursor's official backend (carrying your real login)
   - **Subscription/plan requests** → locally mocked as unlimited to avoid real-plan model locking
4. **Agent kernel**: rebuilds a Cursor-like agent loop locally (tool calls, shell, file edits, codebase indexing, context compaction, usage stats, session replay)

## Supported model providers

| Type | Protocol | Examples |
|---|---|---|
| OpenAI-compatible | `/v1/responses`, `/v1/chat/completions`, custom path | OpenAI official, third-party OpenAI-compatible gateways |
| Anthropic-compatible | Anthropic Messages API | Claude official, Bedrock/Vertex passthrough |

Each model config includes: `baseURL`, `apiKey`, `modelID`, provider type, endpoint, reasoning effort, thinking budget, custom headers, extra params, context window, max tokens.

## Supported IDE

- **Cursor** (current version; Cursor's `state.vscdb` paths, extension protos, and settings keys are hardcoded)

## Features

- **Real account coexistence**: since 1.0.0, log in/out of your real Cursor account while byok runs; marketplace/customize fully functional
- **Model adapter management**: GUI CRUD, single / batch concurrent testing (concurrency 10)
- **Two run modes**: local service mode (default) / direct Cursor mode (passthrough, off by default)
- **Usage metrics**: input / output / cache tokens, cache hit rate, cost estimate
- **Prompt cache**: Anthropic cache breakpoints, OpenAI prompt_cache_key
- **Thinking / reasoning**: deep thinking, reasoning effort control, provider-specific disable-field injection
- **Session persistence**: config / history / logs under `~/.cursor-local-assistant-v2/`
- **Auto-update**: pulls signed `update.json` manifest from this repo's releases
- **Multilingual GUI**: Simplified Chinese, English, Japanese

---

## Install & use

### Quick start

1. Download the archive for your platform from [Releases](https://github.com/kael-odin/cursor-byok/releases)
2. Extract to any folder and launch the app
3. Add your model adapter in "Model Config" (fill baseURL / apiKey / modelID)
4. Start the local service (first run requires UAC elevation to install the CA certificate)
5. **Log in to your Cursor account** (1.0.0 new capability: works while byok is running)
6. **Then launch Cursor** — order matters: start the plugin, install the CA, configure the model, log in, and only then open Cursor

### Correct startup order

> Wrong order is the most common cause of "it doesn't work".

```
1. Launch cursor-byok
2. First launch requests UAC to install the local CA → approve
3. Add your provider in "Model Config" (baseURL/apiKey/modelID) → test connectivity
4. Start the local service (toggle to "start")
5. Log in to your Cursor account (no longer conflicts as of 1.0.0)
6. Launch Cursor → open chat, select your byok model → open marketplace to verify the full UI
```

### Model picker locked to auto?

If the chat UI is locked to auto and you can't select a byok model, **check you're on 1.0.0+**. Old versions locked auto due to fake-account/real-account conflict; 1.0.0 fixes this by uniformly mocking the plan/usage as unlimited Pro.

### Updating Cursor IDE itself

Cursor's own updates **cannot run while the plugin is active**. Correct flow:

1. Quit cursor-byok (stop the local service)
2. Open Cursor, check for and install updates
3. Restart the plugin after the update, then resume use

### Upgrading from ≤0.0.41

1.0.0's first launch **automatically cleans up the legacy fake-account injection**: when the old fake Ultra fingerprint is detected, it safely deletes fields still equal to the fake values (never touches real values), after which you **log in to Cursor once**.

---

## Build

Dependencies: Go ≥1.25, Node.js, yarn, wails3 CLI (`v3.0.0-alpha.74`), protoc toolchain.

```bash
# Generate proto code (first time, or after proto changes)
wails3 task common:generate:proto

# Build the Windows amd64 release package (output: bin/windows-64.zip)
wails3 task build:windows:amd64
```

Quick Go-only build (for dev self-test):

```bash
GOOS=windows CGO_ENABLED=0 GOARCH=amd64 go build -tags production -trimpath \
  -ldflags="-w -s -H windowsgui -X cursor/internal/buildinfo.Version=1.0.0" \
  -o "bin/CursorAssistant.exe" .
```

## Publishing

Multi-platform releases are built by GitHub Actions. **See [docs/RELEASE_SIGNING.md](./docs/RELEASE_SIGNING.md) for the full pre-release and signing flow.**

Summary:

1. Sync version in three places: `build/config.yml`(`info.version`), `build/windows/info.json`(`file_version`/`ProductVersion`), `build/windows/wails.exe.manifest`(`version`)
2. Update `release-notes.md` with the changelog
3. Commit and push to `main`
4. Tag to trigger: `git tag v1.0.0 && git push origin v1.0.0`
5. **After release, locally sign `update.json`** (signing key is maintainer-local, CI doesn't have it):

```bash
gh release download v1.0.0 --pattern update.json --dir /tmp/
go run ./scripts/release sign --manifest /tmp/update.json
gh release upload v1.0.0 /tmp/update.json --clobber
```

Versions containing `beta` / `rc` are automatically marked as prerelease.

---

## Project layout

```
internal/
  relayauth/             process-level relay proof (MITM→backend trust header)
  mitm/                  local MITM proxy (credential capture, proof injection)
  backend/
    server/              HTTP routing, middleware, credential capture, policy
    server/upstream/     outbound credential policy (CredentialOriginalCursor, etc.)
    server/config/       loopback enforcement, routing mode
    forwarder/           agent execution kernel (actor/compaction/tool)
    agent/model/         model adapters (openai.go / anthropic.go / router.go)
    host.go              route classification (passthrough vs local mock)
  cursor/                Cursor client injection (certs, settings, state.vscdb repair)
  netproxy/              system-level network proxy (incl. no-redirect client)
  updater/               auto-update + ed25519 signature verification
  certs/                 per-machine CA generation
  buildinfo/             version & release target
frontend/                Vue 3 + vue-router + Tailwind + i18n
proto/                   Cursor-compatible proto definitions
cursor-tab-server/       Cursor Tab completion reverse proxy (standalone)
docs/                    docs (architecture / refactor / release / dev)
```

## Documentation

- [Interface & Architecture](./docs/接口与架构速查.md) — full route classification, credential flow, debugging
- [Refactor Notes](./docs/架构重构记录.md) — 1.0.0 three-plane separation decisions and pitfalls
- [Release & Signing](./docs/RELEASE_SIGNING.md) — pre-release, ed25519 signing, legacy CA
- [Development Guide](./docs/DEVELOPMENT.md) — dev loop, proto/bindings regen, testing

## Contributing

Before a PR, run `go vet ./...` + `go test ./...` and ensure the three version locations are in sync (CI `check.yml` enforces this).

## License

MIT. Derived from [leookun/cursor-byok](https://github.com/leookun/cursor-byok) — thanks to the original author.
