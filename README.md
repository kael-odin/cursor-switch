<div align="center">

# Cursor助手 · cursor-byok

**把你自己的 LLM API key 接入 Cursor IDE，同时保留真实 Cursor 账号的 marketplace / customize 全功能**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/kael-odin/cursor-byok?include_prereleases)](https://github.com/kael-odin/cursor-byok/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/kael-odin/cursor-byok/check.yml?branch=main&label=CI)](https://github.com/kael-odin/cursor-byok/actions)
[![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20macOS%20%7C%20Linux-lightgrey.svg)](#安装)

📚 [English](./README_EN.md) · 中文 · [接口与架构速查](./docs/接口与架构速查.md) · [架构重构记录](./docs/架构重构记录.md)

</div>

---

<img width="820" alt="screenshot" src="https://github.com/user-attachments/assets/2e1710b0-cdbd-4576-bd24-1614df016219" />
<img width="820" alt="screenshot" src="https://github.com/user-attachments/assets/00885453-6a91-4052-aadf-f686daeec881" />
<img width="820" alt="screenshot" src="https://github.com/user-attachments/assets/a607be84-a738-4e33-9750-13352e74001c" />

---

## ✨ 1.0.0 重大变更

1.0.0 是一次架构级重构，核心变化：**真实 Cursor 账号与 byok 自定义模型共存**。

### 旧版（≤0.0.41）的问题

- 伪造一个 Ultra 假账号写入 Cursor 的真实本地数据库，**覆盖你的真实登录态**——开着 byok 就无法登录/退出自己的 Cursor 账号
- marketplace / customize 是 mock 的假数据，**能看不能装**（插件卡片显示但安装 404）
- 假 Ultra 身份与真实账号混用，UI 状态不一致

### 1.0.0 的解法：三条控制面分离

把 Cursor 的请求分成三条互不干扰的面：

| 控制面 | 处理方式 | 结果 |
|---|---|---|
| **身份 / marketplace / 登录** | 官方透传（真实 Cursor 账号） | 登录/退出/marketplace 浏览/安装/卸载/customize 全功能正常 |
| **订阅 / 套餐 / 用量** | 本地 mock（无限制 Pro） | 模型选择器**不锁 auto**，不被真实套餐限制 |
| **模型推理 / 数据面** | byok 本地（你的 provider key） | 自定义模型路由 + 成本统计 |

**效果**：开着 byok，既能用你自己的 key/模型，又能用真实 Cursor 账号的完整 marketplace（plugins / MCP / skills / subagents / rules / commands / hooks），还能正常登录/退出账号。

> 完整架构与接口分类见 [docs/接口与架构速查.md](./docs/接口与架构速查.md)，重构决策历程见 [docs/架构重构记录.md](./docs/架构重构记录.md)。

### 安全增强

- **每机器独立 CA**：不再随二进制分发共享 CA 私钥，首次启动为每台机器生成独立 CA
- **更新强制签名**：`update.json` 启用 ed25519 签名校验，release token 泄露也无法伪造可被接受的更新
- **loopback 信任分离**：内部信任走独立私有头 `X-Cursor-BYOK-Relay-Proof`，不再占用 `Authorization`；真实 Cursor 凭证只回原始 `*.cursor.sh`，绝不发给第三方 provider
- **写路径围栏**：LLM 写文件仅限工作区与终端目录，拒绝写入 `~/.ssh` 等敏感路径

---

## 📖 这是什么

一个桌面应用（Wails v3 + Go 后端 + Vue 3 前端），在本地起一个与 Cursor 兼容的 agent 服务，把 Cursor 客户端的 chat / agent 请求转发到**你自己配置的模型 provider**。它不是 Cursor 的替代品，而是一个本地中间人代理 + 本地 agent 执行内核。

适用场景：
- 想用第三方 OpenAI 兼容 / Anthropic 兼容 API 驱动 Cursor 的 chat 与 agent
- 想用真实 Cursor 账号的 marketplace / customize，同时用自己的 key 跑模型
- 想自托管整套 agent 服务，不被单一平台锁定

## 🔧 工作原理

1. **本地服务**：启动 HTTP/Connect-RPC 服务，对外暴露与 Cursor 兼容的接口
2. **流量导入**：向 Cursor 注入代理设置 + 安装本地 CA 证书，把 Cursor 流量导向本地
3. **请求分流**：
   - **模型/数据面请求** → 本地 backend 做 prompt 编译、历史投影、tool call 处理后，调用你配置的 provider
   - **身份/marketplace/登录请求** → 原样透传到 Cursor 官方后端（携带你的真实登录态）
   - **订阅/套餐请求** → 本地 mock 成无限制，避免真实套餐锁模型
4. **agent 内核**：本地重建类 Cursor 的 agent 执行循环（tool 调用、shell、文件编辑、codebase 索引、上下文压缩、usage 统计、会话回放）

## 🤖 支持的模型 provider

| 类型 | 协议 | 示例 |
|---|---|---|
| OpenAI 兼容 | `/v1/responses`、`/v1/chat/completions`、自定义路径 | OpenAI 官方、各类第三方 OpenAI 兼容网关 |
| Anthropic 兼容 | Anthropic Messages API | Claude 官方、Bedrock/Vertex 透出等 |

每条模型配置含：`baseURL`、`apiKey`、`modelID`、provider 类型、端点、reasoning effort、thinking budget、自定义请求头、额外请求参数、context window、max tokens。

## 💻 支持的 IDE

- **Cursor**（当前版本，代码内硬编码 Cursor 的 `state.vscdb` 路径、扩展 proto、settings keys）

## ✅ 主要功能

- **真实账号共存**：1.0.0 起，开着 byok 也能登录/退出真实 Cursor 账号，marketplace/customize 全功能可用
- **模型适配器管理**：GUI 增删改查、单测 / 批量并发测试（并发 10）
- **两种运行模式**：本地服务模式（默认，请求经本地 backend 转发到你配置的模型）/ 直连 Cursor 模式（放行到官方，默认关闭）
- **usage 指标**：input / output / cache token、缓存命中率、按内置模型定价估算花费
- **prompt cache**：Anthropic cache breakpoints、OpenAI prompt_cache_key
- **thinking / reasoning**：深度思考、reasoning effort 控制、按 provider 差异化注入 disable 字段
- **会话持久化**：`~/.cursor-local-assistant-v2/` 下 config / history / logs
- **自动更新**：从本仓库 release 拉取带签名的 `update.json` manifest
- **多语言 GUI**：简体中文、English、日本語

---

## 🚀 安装与使用

### 快速开始

1. 从 [Releases](https://github.com/kael-odin/cursor-byok/releases) 下载对应平台压缩包
2. 解压到任意目录，启动应用
3. 在「模型配置」中添加你的模型适配器（填 baseURL / apiKey / modelID）
4. 启动本地服务（首次需 UAC 提权安装 CA 证书）
5. **登录你的 Cursor 账号**（1.0.0 新能力：开着 byok 也能正常登录）
6. **再启动 Cursor**——顺序很重要：先开插件、装好 CA、配好模型、登录账号，最后才开 Cursor

### 正确的启动顺序

> 顺序错误是最常见的"不工作"原因。

```
1. 启动 cursor-byok 插件
2. 首次启动会申请 UAC 安装本地 CA 证书 → 同意
3. 在「模型配置」添加你的 provider（baseURL/apiKey/modelID）→ 测试连通
4. 启动本地服务（开关打到"启动"）
5. 登录你的 Cursor 账号（插件内或 Cursor 内均可，1.0.0 起不再冲突）
6. 启动 Cursor → 打开对话，选择你的 byok 模型 → 打开 marketplace 验证完整界面
```

### 模型选择器锁 auto？

如果对话界面锁在 auto 无法选择 byok 模型，**检查是否装的是 1.0.0+**。旧版会因假账号与真实账号冲突而锁 auto，1.0.0 已通过"套餐/用量统一 mock 成无限制 Pro"修复。

### Cursor IDE 自身更新

Cursor 自身的版本更新与插件**不能同时进行**：插件开着时，Cursor 的更新检查会失败或被代理拦截。正确流程：

1. 关闭 cursor-byok 插件（停止本地服务）
2. 打开 Cursor，检查并安装更新
3. 更新完成后重新启动插件，再继续使用

### 从旧版（≤0.0.41）升级

1.0.0 首次启动会**自动清理历史假账号注入**：检测到旧版写入的假 Ultra 指纹时，安全删除仍等于假值的字段（绝不动真实值），清理后需**重新登录一次** Cursor 账号。

---

## 🛠️ 构建

依赖：Go ≥1.25、Node.js、yarn、wails3 CLI（`v3.0.0-alpha.74`）、protoc 工具链。

```bash
# 生成 proto 代码（首次或 proto 变更后）
wails3 task common:generate:proto

# 构建 Windows amd64 发行包（产物：bin/windows-64.zip）
wails3 task build:windows:amd64
```

纯 Go 后端快速构建（开发自测用）：

```bash
GOOS=windows CGO_ENABLED=0 GOARCH=amd64 go build -tags production -trimpath \
  -ldflags="-w -s -H windowsgui -X cursor/internal/buildinfo.Version=1.0.0" \
  -o "bin/Cursor助手.exe" .
```

## 📦 发布

多平台发行通过 GitHub Actions 自动构建（`.github/workflows/release.yml`）。**发版前置与签名流程见 [docs/RELEASE_SIGNING.md](./docs/RELEASE_SIGNING.md)。**

简述：

1. 同步版本号三处合一：`build/config.yml`(`info.version`)、`build/windows/info.json`(`file_version`/`ProductVersion`)、`build/windows/wails.exe.manifest`(`version`)
2. 更新 `release-notes.md` 写本次变更
3. 提交并推送到 `main`
4. 打 tag 触发：`git tag v1.0.0 && git push origin v1.0.0`
5. **发版后本地补签 `update.json`**（签名私钥在维护者本地，CI 不持有）：

```bash
gh release download v1.0.0 --pattern update.json --dir /tmp/
go run ./scripts/release sign --manifest /tmp/update.json
gh release upload v1.0.0 /tmp/update.json --clobber
```

版本号含 `beta` / `rc` 等字样时自动标记为 prerelease。

---

## 📁 项目结构

```
internal/
  relayauth/             进程级 relay proof（MITM→backend 信任头）
  mitm/                  本地 MITM 代理（凭证捕获、proof 注入）
  backend/
    server/              HTTP 路由、中间件、凭证捕获、policy
    server/upstream/     出站凭证策略（CredentialOriginalCursor 等）
    server/config/       loopback 强制、路由模式
    forwarder/           agent 执行内核（actor/compaction/tool）
    agent/model/         模型适配器（openai.go / anthropic.go / router.go）
    host.go              路由分类总表（透传 vs 本地 mock）
  cursor/                Cursor 客户端注入（证书、settings、state.vscdb 修复）
  netproxy/              系统级网络代理（含 no-redirect 客户端）
  updater/               自动更新 + ed25519 签名校验
  certs/                 每机器独立 CA 生成
  buildinfo/             版本与发布目标
frontend/                Vue 3 + vue-router + Tailwind + i18n
proto/                   Cursor 兼容 proto 定义
cursor-tab-server/       Cursor Tab 补全反向代理（独立程序）
docs/                    文档（架构速查 / 重构记录 / 发版签名 / 开发指南）
```

## 📚 文档

- [接口与架构速查](./docs/接口与架构速查.md) — 全部路由分类、凭证链路、调试方法
- [架构重构记录](./docs/架构重构记录.md) — 1.0.0 三条控制面分离的决策与踩坑历程
- [发版与签名](./docs/RELEASE_SIGNING.md) — 发版前置、ed25519 签名、旧 CA 处理
- [开发指南](./docs/DEVELOPMENT.md) — 开发循环、proto/bindings 再生、测试范式

## 🤝 贡献

PR 前请跑 `go vet ./...` + `go test ./...`，并确保版本号三处合一（CI `check.yml` 会校验）。

## 📄 许可证

MIT。本项目基于 [leookun/cursor-byok](https://github.com/leookun/cursor-byok) 衍生，感谢原作者。
