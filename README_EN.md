# Cursor Assistant · cursor-byok

> Connect your own LLM API key (OpenAI-compatible / Anthropic-compatible) to the Cursor IDE, bypassing the official model and subscription lock-in.
>
> 中文：[README.md](./README.md)

<img width="820" alt="screenshot" src="https://github.com/user-attachments/assets/2e1710b0-cdbd-4576-bd24-1614df016219" />
<img width="820" alt="screenshot" src="https://github.com/user-attachments/assets/00885453-6a91-4052-aadf-f686daeec881" />
<img width="820" alt="screenshot" src="https://github.com/user-attachments/assets/a607be84-a738-4e33-9750-13352e74001c" />

---

## What it is

A Windows desktop app (Wails v3 + Go backend + Vue 3 frontend) that runs a Cursor-compatible agent service locally and forwards Cursor client chat / agent requests to **your own configured model provider**. It is not a Cursor replacement; it is a local man-in-the-middle proxy plus a local agent execution kernel.

Use cases:
- Drive Cursor's chat and agent with a third-party OpenAI- / Anthropic-compatible API
- Self-host the full agent stack without platform lock-in
- Fine-grained control over model selection, billing, and context handling

## How it works

1. **Local service**: starts an HTTP/Connect-RPC service exposing Cursor-compatible endpoints
2. **Traffic interception**: injects proxy settings into Cursor + installs a local CA certificate to route Cursor traffic to localhost
3. **Request forwarding**: the local backend compiles prompts, projects history, handles tool calls, then calls your configured model provider
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

- **Model adapter management**: GUI CRUD, single / batch concurrent testing (concurrency 10)
- **Two run modes**: local service mode (default, requests forwarded through local backend to your model) / direct Cursor mode (passthrough to official, off by default)
- **Usage metrics**: input / output / cache tokens, cache hit rate, cost estimate at $5 / $25 / $0.5 / $6.25 per million tokens
- **Prompt cache**: Anthropic cache breakpoints, OpenAI prompt_cache_key
- **Thinking / reasoning**: deep thinking, reasoning effort control, provider-specific disable-field injection
- **Session persistence**: config / history / logs under `~/.cursor-local-assistant-v2/`
- **Auto-update**: pulls `update.json` manifest from this repo's releases
- **Multilingual GUI**: Simplified Chinese, English, Japanese

## Install & use

1. Download the Windows amd64 zip from [Releases](https://github.com/kael-odin/cursor-byok/releases)
2. Extract and run `windows-64.exe`
3. Add your model adapter in "Model Config" (fill baseURL / apiKey / modelID)
4. Start the local service (first run requires UAC elevation to install the CA certificate)
5. Open Cursor — chat / agent is now driven by your configured model

## Build

Dependencies: Go ≥1.25, Node.js, yarn, wails3 CLI (`v3.0.0-alpha.74`), protoc toolchain.

```bash
# Generate proto code (first time, or after proto changes)
wails3 task common:generate:proto

# Build the Windows amd64 release package (output: bin/windows-64.zip)
wails3 task build:windows:amd64
```

## Publishing

Multi-platform releases are built by GitHub Actions (`.github/workflows/release.yml`) on 4 platform runners — producing Windows / macOS Intel / macOS Apple Silicon / Linux assets plus `update.json`, then published to Releases. No local cross-platform build required.

**Before a new release**:

1. Update `info.version` in `build/config.yml` and `file_version` / `ProductVersion` in `build/windows/info.json` (keep both in sync)
2. Update `release-notes.md` with the changelog
3. Commit and push to `main`

**Trigger the release** (either way):

```bash
# Option 1: manual dispatch (version without leading v)
gh workflow run release.yml -f version=0.0.40

# Option 2: push a tag
git tag v0.0.40
git push origin v0.0.40
```

Versions containing `beta` / `rc` are automatically marked as prerelease. Assets follow the `cursor-byok-<version>-<platform>.<ext>` naming, matching upstream.

## Project layout

```
internal/
  backend/agent/model/   model adapters (openai.go / anthropic.go / router.go)
  backend/forwarder/     agent execution kernel (service.go / actor.go / compaction.go)
  backend/server/        HTTP routing, middleware, policy
  bridge/                Wails service bridge
  cursor/                Cursor client injection (certs, settings, state.vscdb)
  netproxy/              system-level network proxy
  buildinfo/             version & release target
frontend/                Vue 3 + vue-router + Tailwind + i18n
proto/                   Cursor-compatible proto definitions
cursor-tab-server/       Cursor Tab completion reverse proxy (standalone)
```

## License

MIT. Derived from [leookun/cursor-byok](https://github.com/leookun/cursor-byok) — thanks to the original author.
