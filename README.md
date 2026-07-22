# Cursor助手 · cursor-byok

> 把你自己的 LLM API key（OpenAI 兼容 / Anthropic 兼容）接入 Cursor IDE，绕开官方模型与订阅绑定。
>
> English: [README_EN.md](./README_EN.md)

[截图1]: https://github.com/user-attachments/assets/2e1710b0-cdbd-4576-bd24-1614df016219
[截图2]: https://github.com/user-attachments/assets/00885453-6a91-4052-aadf-f686daeec881
[截图3]: https://github.com/user-attachments/assets/a607be84-a738-4e33-9750-13352e74001c

<img width="820" alt="screenshot" src="https://github.com/user-attachments/assets/2e1710b0-cdbd-4576-bd24-1614df016219" />
<img width="820" alt="screenshot" src="https://github.com/user-attachments/assets/00885453-6a91-4052-aadf-f686daeec881" />
<img width="820" alt="screenshot" src="https://github.com/user-attachments/assets/a607be84-a738-4e33-9750-13352e74001c" />

---

## 这是什么

一个 Windows 桌面应用（Wails v3 + Go 后端 + Vue 3 前端），在本地起一个与 Cursor 兼容的 agent 服务，把 Cursor 客户端的 chat / agent 请求转发到**你自己配置的模型 provider**。它不是 Cursor 的替代品，而是一个本地中间人代理 + 本地 agent 执行内核。

适用场景：
- 想用第三方 OpenAI 兼容 / Anthropic 兼容 API 驱动 Cursor 的 chat 与 agent
- 想自托管整套 agent 服务，不被单一平台锁定
- 想精细控制模型选择、计费方式与上下文处理

## 工作原理

1. **本地服务**：在本地启动 HTTP/Connect-RPC 服务，对外暴露与 Cursor 兼容的接口
2. **流量导入**：通过向 Cursor 注入代理设置 + 安装本地 CA 证书，把 Cursor 流量导向本地
3. **请求转发**：本地 backend 做 prompt 编译、历史投影、tool call 处理后，调用你配置的模型 provider
4. **agent 内核**：在本地重建类 Cursor 的 agent 执行循环（tool 调用、shell、文件编辑、codebase 索引、上下文压缩、usage 统计、会话回放）

## 支持的模型 provider

| 类型 | 协议 | 示例 |
|---|---|---|
| OpenAI 兼容 | `/v1/responses`、`/v1/chat/completions`、自定义路径 | OpenAI 官方、各类第三方 OpenAI 兼容网关 |
| Anthropic 兼容 | Anthropic Messages API | Claude 官方、Bedrock/Vertex 透出等 |

每条模型配置含：`baseURL`、`apiKey`、`modelID`、provider 类型、端点、reasoning effort、thinking budget、自定义请求头、额外请求参数、context window、max tokens。

## 支持的 IDE

- **Cursor**（当前版本，代码内硬编码 Cursor 的 `state.vscdb` 路径、扩展 proto、settings keys）

## 主要功能

- **模型适配器管理**：GUI 增删改查、单测 / 批量并发测试（并发 10）
- **两种运行模式**：本地服务模式（默认，请求经本地 backend 转发到你配置的模型）/ 直连 Cursor 模式（放行到官方，默认关闭）
- **usage 指标**：input / output / cache token、缓存命中率、按内置模型定价估算花费
- **prompt cache**：Anthropic cache breakpoints、OpenAI prompt_cache_key
- **thinking / reasoning**：深度思考、reasoning effort 控制、按 provider 差异化注入 disable 字段
- **会话持久化**：`~/.cursor-local-assistant-v2/` 下 config / history / logs
- **自动更新**：从本仓库 release 拉取 `update.json` manifest
- **多语言 GUI**：简体中文、English、日本語

## 安装与使用

### 快速开始

1. 从 [Releases](https://github.com/kael-odin/cursor-byok/releases) 下载 Windows amd64 压缩包
2. 解压到任意目录，双击 `windows-64.exe` 启动插件
3. 在「模型配置」中添加你的模型适配器（填 baseURL / apiKey / modelID）
4. 启动本地服务（首次需 UAC 提权安装 CA 证书）
5. **再启动 Cursor**——顺序很重要：先开插件、装好 CA、配好模型，最后才开 Cursor，流量才会被正确导入

### Cursor IDE 自身更新

Cursor 自身的版本更新与插件**不能同时进行**：插件开着时，Cursor 的更新检查会失败或被代理拦截。正确流程：

1. 关闭 cursor-byok 插件（停止本地服务）
2. 打开 Cursor，检查并安装更新
3. 更新完成后重新启动插件，再继续使用

### 详细配置

> 详细配置项与故障排查见 GUI 内的「使用教程」入口（指向本仓库 README）。

## 构建

依赖：Go ≥1.25、Node.js、yarn、wails3 CLI（`v3.0.0-alpha.74`）、protoc 工具链。

```bash
# 生成 proto 代码（首次或 proto 变更后）
wails3 task common:generate:proto

# 构建 Windows amd64 发行包（产物：bin/windows-64.zip）
wails3 task build:windows:amd64
```

## 发布

多平台发行通过 GitHub Actions 自动构建（`.github/workflows/release.yml`），在 4 个平台 runner 上产出 Windows / macOS Intel / macOS Apple Silicon / Linux 资产，生成 `update.json`，并发布到 Release。本地无需跨平台构建。

**发新版前**：

1. 改 `build/config.yml` 的 `info.version`，并同步 `build/windows/info.json`(`file_version`/`ProductVersion`)与 `build/windows/wails.exe.manifest`(`version`)——三处必须一致，CI `check.yml` 的 `verify-versions` 会校验
2. 更新 `release-notes.md` 写本次变更
3. 提交并推送到 `main`

**触发发布**（二选一）：

```bash
# 方式一：手动触发（指定版本号，不带前导 v）
gh workflow run release.yml -f version=0.0.40

# 方式二：打 tag 自动触发
git tag v0.0.40
git push origin v0.0.40
```

版本号含 `beta` / `rc` 等字样时自动标记为 prerelease。资产命名遵循 `cursor-byok-<版本>-<平台>.<后缀>`，与上游一致。

## 项目结构

```
internal/
  backend/agent/model/   模型适配器（openai.go / anthropic.go / router.go）
  backend/forwarder/     agent 执行内核（service.go / actor.go / compaction.go）
  backend/server/        HTTP 路由、中间件、policy
  bridge/                Wails service 桥接层
  cursor/                Cursor 客户端注入（证书、settings、state.vscdb）
  netproxy/              系统级网络代理
  buildinfo/             版本与发布目标
frontend/                Vue 3 + vue-router + Tailwind + i18n
proto/                   Cursor 兼容 proto 定义
cursor-tab-server/       Cursor Tab 补全反向代理（独立程序）
```

## 许可证

MIT。本项目基于 [leookun/cursor-byok](https://github.com/leookun/cursor-byok) 衍生，感谢原作者。
