# 1.1.0

## 成本精确化 · 仪表盘增强 · models.dev 在线回退 · 改名 cursor-switch

1.1.0 在 1.0.0 架构基础上收尾一批成本与可用性优化，并把项目由 `cursor-byok` 改名为 `cursor-switch`。

### 日常成本精确化

- `usage.json` daily rollup 新增 `by_model` 维度，按模型分别累计 token 与成本。
- 仪表盘成本计算优先按 per-model 价格 × 倍率精确计算；旧 usage.json（无 by_model）回退加权均价近似，并在 UI 标注 `近似`。
- 顺带修复 `negateUsageFileDelta` 不带 modelID 的遗留 bug：顶层 by_model 在事件 upsert 回滚时漏减。

### 仪表盘：日期范围 + 请求日志分页

- 使用统计仪表盘新增 today / 7d / 30d / 全部 日期范围选择器。
- 请求日志列表分页（每页 20 条），500 条上限事件可翻页浏览。
- 纯前端实现，未改后端契约。

### fetch-models：models.dev 在线回退

- 内置静态上下文窗口表 miss 时，自动拉取 `https://models.dev/models.json` 在线查询。
- 内存 + 落盘 7 天 TTL 缓存（`~/.cursor-local-assistant-v2/models-dev-cache.json`）。
- 双向候选交集匹配，兼容 `deepseek/deepseek-v4` 等带命名空间/版本号的模型 id。
- best-effort：在线失败回退 0，不阻断获取模型列表。

### 新模型上线 SOP

- 新增 [docs/NEW_MODEL_SOP.md](./docs/NEW_MODEL_SOP.md)：定价记录 / 上下文窗口 / 候选匹配测试 / release-notes 一条龙步骤。

### 改名 cursor-switch

- 产品由 `cursor-byok` 改名为 `cursor-switch`（GitHub repo 名 + 资产前缀硬切）。
- 保留不变：`X-Cursor-BYOK-Relay-Proof` header（契约非品牌）、数据目录 `~/.cursor-local-assistant-v2`、模块路径 `cursor`、机器 CA 命名空间 `cursor-byok-ca`（改了会让现有 CA 失效）。
- 签名公钥不变，私钥默认路径迁移到 `~/.cursor-switch-release.key`（维护者 `cp` 旧私钥即可）。

### README 美化

- 新增 hero.svg（项目标题 + 三控制面分流图）与 architecture.svg（详细三面分离架构）。
- 中英文 README 同步嵌入，纯 SVG，无外部资源。

**完整改动见 [docs/NEW_MODEL_SOP.md](./docs/NEW_MODEL_SOP.md) 与 git log。**

---

# 1.0.0

## 架构级重构：真实账号 + byok 自定义模型共存

1.0.0 是一次架构级重构。核心变化：**真实 Cursor 账号与 byok 自定义模型共存**。

### 旧版问题

- 伪造 Ultra 假账号写入 Cursor 真实 `state.vscdb`，**覆盖用户真实登录态**——开着 byok 无法登录/退出真实账号
- marketplace / customize 是 mock 假数据，**能看不能装**（插件卡片显示但安装 404）
- 假 Ultra 与真实账号混用，UI 状态不一致

### 三条控制面分离

| 控制面 | 处理 | 结果 |
|---|---|---|
| 身份 / marketplace / 登录 | 官方透传（真实 Cursor 账号） | 登录/退出/marketplace 浏览/安装/卸载/customize 全功能 |
| 订阅 / 套餐 / 用量 | 本地 mock（无限制 Pro） | 模型选择器不锁 auto |
| 模型推理 / 数据面 | byok 本地 | 自定义模型路由 + 成本统计 |

### 凭证链路重构

- 新增 `internal/relayauth`：进程级随机 proof 走私有头 `X-Cursor-BYOK-Relay-Proof`，不再占用 `Authorization`
- MITM 保留原始 `Authorization`/`Cookie`/`x-cursor-checksum` → backend 捕获到 `ctx.Credentials` 并从请求头剥离 → 仅 `CredentialOriginalCursor` 策略在目标校验通过后恢复给官方 `*.cursor.sh`
- 真实凭证绝不发给第三方 provider 或 `tab.leokun.cn`；不跟随重定向
- backend/proxy 强制只监听 loopback（`127.0.0.1`/`::1`）

### 路由分类

- **官方透传**：`/oauth/token`、`/auth/*`（登录/退出/poll）、`GetMe`/`GetEmail`/`GetUserProfile`、整个 `DashboardService/*`（含 marketplace 安装/卸载/更新/MCP/Skills/Commands/Hooks）、`BootstrapStatsig`、Plugins/Marketplace/Chat/Health/Metrics/BackgroundComposer 服务
- **本地 mock（无限制）**：`GetUsageLimitStatusAndActiveGrants`（`allowedModelIds:[]`）、`GetPlanInfo`、`GetCurrentPeriodUsage`、`IsOnNewPricing`、stripe 三接口（`full_stripe_profile`/`stripe_profile`/`has_valid_payment_method`）
- **byok 本地**：`AvailableModels`/`GetDefaultModel`/`RunSSE`/`BidiAppend`/Repository/Upload/`GetTokenUsage`

### 历史假账号清理

- `StartProxy` 不再调 `InjectCursorUserInfo`（不再覆盖真实 `state.vscdb`）
- 新增 `RepairLegacyInjectedIdentity`：首启检测旧假指纹，安全删除仍等于假值的字段（绝不创建库/删真实值），清理后需重新登录一次

### 文档

- 新增 `docs/接口与架构速查.md` — 全路由分类与调试方法
- 新增 `docs/架构重构记录.md` — 决策历程与踩坑记录

## 安全增强（继承自 0.0.41）

- **每机器独立 CA**：不再随二进制分发共享 CA 私钥
- **更新强制签名**：`update.json` ed25519 签名校验
- **loopback 信任分离**：内部 proof 独立于 `Authorization`
- **写路径围栏**：LLM 写文件仅限工作区与终端目录
- **自定义请求头黑名单**：禁止覆盖 `Authorization`/`x-api-key`/`Host`/`Cookie`
- **prompt 注入面闭合**：XML 特殊字符转义

## 升级须知

从 ≤0.0.41 升级：首启自动清理历史假账号注入，需重新登录一次 Cursor 账号。之后即可同时使用 byok 自定义模型与真实账号 marketplace。

---

# 0.0.41

- 支持 GPT 5.6
- 纯净化
- 发布目标迁移至 kael-odin/cursor-byok（自动更新、release 资产、README 同步全部指向本 fork）
- 修复 OpenAI 适配器 thinking disable 不识别小米 MiMo（reasoning=disabled 时 MiMo 仍开思考）
- 修复 Anthropic 适配器 thinking 配置在 override 路径丢失，与 openai 行为对齐
- 标题与品牌去除"永久免费"立场表述
- 更新支持最新版 Cursor 3.9.16
- 支持多工作区模式

## 安全更新（P0）

- **每机器独立 CA**：不再随二进制分发共享 CA 私钥，首次启动为每台机器生成独立 CA（存于用户数据目录）
- **写路径工作区围栏**：LLM 写文件仅限工作区与终端目录，拒绝写入 `~/.ssh`、系统目录等敏感路径
- **更新签名校验**：update.json 启用 ed25519 强制签名校验（公钥已内置），release token 泄露也无法伪造可被接受的更新；篡改的 manifest 一律拒绝
- **CA 可卸载**：停止服务时从系统信任存储移除本机 CA（此前卸载会残留信任锚）
- **loopback 鉴权**：本地 backend 仅接受本进程 mitm 转发的请求，拒绝本机其它进程裸调
- **prompt 注入面闭合**：文件正文 / tool_result / 上下文嵌入串转义 XML 特殊字符，防止标签逃逸
- **自定义请求头黑名单**：禁止自定义头覆盖 `Authorization` / `x-api-key` / `Host` / `Cookie`
- **正则 ReDoS 防护**：AwaitShell 模式限长 + 匹配超时
- **Cursor 配置解析失败改备份**：损坏的 settings.json 改为备份而非删除
- **cursor-tab-server 配置隔离**：默认配置改名为 config.example.yaml，真实 token 不再进 git

## 重构与工程化

- **前端 i18n / a11y / 定价后端化**：路由标题走 i18n（en/ja 不再显示中文）；首页 Modal 加焦点陷阱与 `role=dialog`；首页成本估算定价从前端硬编码挪到后端 `MetricsService.GetTokenPricing`
- **版本三处合一**：`config.yml` / `info.json` / `wails.exe.manifest` 版本一致性由 CI `verify-versions` 自动校验；新增 `sync-versions` 子命令一键同步
- **删除 license 死代码**：移除未使用的 license / usage records DTO 与 Wails 方法
- **god 文件拆分**：`service.go`（3573→2038）、`openai.go`（2241→1708）、`compaction.go`（1912→1730）按主题拆出 history entries / subagent overrides / endpoint 解析 / turn lifecycle / tool invocation / exec intent / compaction entries / openai responses / openai messages 等独立文件（零行为变更）
- **测试网扩充**：新增 SSE think-tag 解析、history entry 构造器、compaction entry 构造器、`decodeInboundIntent` 各路径、settings JSONC 解析、代理 URL 归一化、manifest 签名 roundtrip 等纯逻辑测试
- **CI 检查**：新增 `check.yml`，PR / push 到 main 自动跑 go vet/build/test + 版本同步校验
- **lint 修复**：修复 proto 消息值拷贝锁的 vet warning，`go vet ./...` 零 warning
- **贡献者文档**：新增 `docs/DEVELOPMENT.md`（开发循环、proto/bindings 再生、测试范式、留债清单）


