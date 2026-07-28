# 2.0.2

## F-09 官方回源 NoRedirect 策略修复

- **`CredentialOriginalCursor` 无条件 NoRedirect**：`buildUpstreamRequest` 此前仅在未注入 HTTP client 时才为官方回源链创建不跟随重定向的 client，但 Host 在 `rebuildLocked` 无条件注入了 `netproxy.NewHTTPClient`（会跟随 3xx），导致 NoRedirect 分支恒不可达——恢复的真实 Cursor 凭证（`x-cursor-checksum` 等非标准头）可能随服务端重定向带到未校验目标。现改为凭证策略优先于依赖注入：`CredentialOriginalCursor` 无条件走 `netproxy.NewHTTPClientNoRedirect`，与注入与否解耦；timeout 对齐 Host 注入值 30s 避免无超时挂起。4 回归测试（注入跟随 client + OriginalCursor → 断言 302 原样返回、第二跳 0 命中），修复前可复现失败。

## F-17 内置 Pricing 删除持久化 + 恢复默认价

- **内置定价删除改逻辑删除（tombstone）**：此前删除内置标准价目记录后，`Save` 内的 `normalizePricingConfig` 把刚删的 modelId 当"缺失"按 seed 补回，删除在同一 Save 调用里就被抹掉——UI 承诺"删除后成本按 0 计算"与实际不符。现引入 `IsBuiltin`/`Disabled` 字段：内置记录"删除"实为置 `Disabled=true`，normalize 跳过已禁用的内置 modelId 不补回，成本计算按零价；自定义记录仍物理删除。
- **恢复默认价**：新增 `RestoreDefaultPricing` 接口，把内置记录价格重置为 seed 原值并清 `Disabled`。前端 PricingPanel：内置行显示"禁用/恢复默认价"按钮 + "内置/已禁用"标签 + 灰显，自定义行保留"删除"。8 回归测试（删除路径此前 0 测试），覆盖删除持久化、reload 不补回、disabled 零价、恢复默认、旧配置回填 IsBuiltin。

## P0 CI release gate（低成本部分）

- **go test -race**：check.yml 的 go job 加 `-race` 测试 step（CI ubuntu 有 gcc，本地无 gcc 跑不了）。给 S14/F-28 broker 并发、S20/F-35 hijacked conn + 生命周期状态机、F-02 配置锁、F-13 会话崩溃一致性等大批并发相关安全修复上动态验收保险，是"证明这批大规模安全修改没引入回归"最直接手段。
- **frontend build**：新建 frontend job 跑 `yarn build` production 构建。此前 CI 只 stub `frontend/dist` 占位编译，从不验证前端能真正构建；S17（生产 console 脱敏）等改动改了 clientApi.js，需保证 production build 不破。
- **暂不加 ESLint/Vitest/E2E/smoke**：前端无现成 lint/test 基础设施，cursor-tab-server 无任何测试，smoke/E2E 需从零写——投入产出比低，留后续决策。

# 2.0.1

## CI 发版链修复 · 彻底切 CI 自动签名

2.0.0 的发版链有两个问题，2.0.1 修复并完成 CI 自动签名切换：

- **check.yml `version sync` job cgo 依赖**：`scripts/release` 经 `cursor/internal/updater` 传递依赖 wails v3 cgo 包，`verify-versions` 这种纯字符串校验也触发 pkg-config 探测 gtk/webkit，但该 job 没装 GUI deps 而红。给 `versions` job 加装 `libgtk-3-dev libwebkit2gtk-4.1-dev`，与 `go` job 对齐。
- **Windows protoc 改直连官方 release**：不再用 choco（`community.chocolatey.org` 偶发 503 会让整个 release 挂），改为从 `protocolbuffers/protobuf` GitHub release 下载 `protoc-35.0-win64.zip`。
- **彻底切 CI 自动签名**：release.yml 删除"本地补签 fallback artifact"分支（本地补签已废），保留 `Sign or discard` 门控防 secret 缺失时误发未签名 manifest。签名私钥作为 GitHub Actions secret `CURSOR_SIGNING_KEY` 注入 CI，打 tag 即发版即签名，零本地操作。
- **F-11 姿态演化说明**：F-11 实质目标（消除未签名 manifest 公开窗口）已达成，但实现从"维护者本地补签"演化为 CI 自动签名——泄露面从"仅本地"扩大到"本地 + GitHub secret"，单人 fork、branch protection 开启时判断可接受。详见 [docs/RELEASE_SIGNING.md](./docs/RELEASE_SIGNING.md) 第三节与审计 md 第五部分 F-11。

# 2.0.0

## 安全审计完整收尾 · 生产级路由 · 成本精确化

2.0.0 是 1.1.0 之后的收尾大版本：完成 gpt-5.6 全量静态安全审计（37 finding，35 项闭合）、并入生产级路由能力（多 provider 候选链 + 熔断器）、并收口一批计费正确性与性能修复。完整审计证据与每项修复摘要见 [docs/AUDIT_2026-07-26.md](./docs/AUDIT_2026-07-26.md)。

### 安全审计（gpt-5.6 全量 37 finding，35 项闭合）

S1-S20 安全批次 + 后续闭合项（按修复面归纳）：

- **信任边界 / SSRF**：WebFetch DNS 解析固定 IP 防 rebinding（F-24）、provider redirect 禁跟随防 `x-api-key` 泄漏（F-22）、conversation ID 目录逃逸防护（F-04）。
- **工作区围栏 / 只读 capability**：realpath 双 `EvalSymlinks` 堵 symlink 逃逸 + Delete/downloadPath 覆盖（F-32）、Ask/Plan 子代理后端强制只读能力集（F-31）、禁止服务端读 `SelectedImage.path`（F-30）。
- **流资源预算**：三 provider 流字节/事件预算（F-21）、broker backlog 硬上限 + upstream 32MiB LimitReader + mitm http.Server 超时（F-28）、artifact session 回收堵 map 永久增长（F-36）、流成功终止状态机 + 缺终止事件可重试（F-20）。
- **配置事务 / 数值校验**：配置字段级 patch + 锁内 Load-Modify-Save 事务（F-02/F-03）、Pricing 更新保留 `InputTokenSemantics`（F-16）、倍率 NaN/Inf/零/负值校验（F-34）、extra params denylist 防覆盖协议字段（F-19）。
- **CA / 权限**：CA 加载校验 IsCA/KeyUsage/有效期/公私钥匹配 + 损坏重生（F-12）、文件权限集中 0600/0700 助手 + 启动迁移收紧（F-18）、MITM leaf 证书 ECDSA P-256 + TTL（M7）。
- **发布链加固**：ed25519 验签前置（F-27）、manifest/下载字节数限流（F-25）、触发版本与 config.yml 对齐硬失败（F-26）、未签名 manifest 不进 release 资产（F-11）。
- **生命周期 / 持久化**：hijacked CONNECT 隧道强制关闭 + Start/Stop/Save 串行化 + 显式状态机 + 阶段失败定向回滚（F-14/F-35）、会话崩溃一致性 context/state 版本校验 + atomic write Sync（F-13）、旧目录迁移两阶段 + 备份保留绝不删（F-29）、legacy usage 索引一次性回填（F-05）、endpoint net/url 拼接保留 query/fragment（F-23）。
- **部署 / 前端**：cursor-tab-server 默认 loopback + 入站 token 校验（F-33）、生产前端 console 脱敏（F-37）、上游 HTTP 超时单位 typo（F-10）、客户端取消/sink 错误不计 provider 故障（F-06）、disabled adapter UI 过滤（F-08）。

未闭合（非纯代码或架构决策）：**F-15**（本地 MITM 客户端认证，需架构决策，暂缓）、**F-07**（401/403/404 failover 是否放宽，留后续策略）、**F-01**（B2 已修 failover 核心，UI 暴露逻辑模型 ID 残留）、**F-09 / F-17**（低优先）。

### 生产级路由（B2 候选链 + A1 熔断）

单 channel 架构升级为多 provider 候选链 + 故障转移：同 modelID 配多个适配器时，按优先级组成主→备候选链，主候选在输出内容前失败（连接错误 / 5xx / 429 / 流超时）自动切到下一个，已开始输出的请求绝不切换避免双发。熔断器（A1）接入：单个 provider 连续失败或错误率过高自动熔断，熔断中的候选排到候选链末尾兜底。

- `ModelAdapterConfig` 新增 `Priority`（数字小的优先）、`Enabled`（关闭则保留配置但不参与路由）、`Weight`（轮转策略预留）字段。
- 模型编辑器新增「故障转移候选链」分组（优先级 / 权重 / 启用开关）。
- 模型列表对「同 modelID 多启用适配器」显示 `候选 P{优先级}` 徽章，停用的显示 `已停用`。
- 本期只实现 Failover 策略（主→备按序）；ConversationRoundRobin / RequestRoundRobin / WeightedRoundRobin 三种轮转策略留后续。

### 成本精确化 / 性能

- `usage.json` `EventIndex` 独立持久化，不再被 `RecentEvents` 500 条截断影响 turn 计费（H5）；legacy 文件首次加载一次性回填完整索引（F-05）。
- `GetThoughtAnnotation` 建 requestID→entry 反向索引，消除 O(会话数 × entry 数) 全量扫描（H4）。
- `realTotalTokens` 按 `InputTokenSemantics` 修正 TOTAL 语义重复计 cache（M1）；成本口径异常检测提示（M9）。
- `model_pricing_candidates` 7 段剥离补全 + 内置定价表同步 v3.18.0（A7），候选匹配去命名空间/版本/日期/推理努力后缀让 `openai.gpt-5` / `claude-opus-4-6-20251114` 命中价目。

> 阶段三（云端一体化服务透传 / 细粒度路由）、阶段四（会话管理 + 项目 Profile）保持待办，详见审计 md 第四部分。

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


