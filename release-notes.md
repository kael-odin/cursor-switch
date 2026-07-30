# 2.0.8

## 文生图 + 图生图端到端打通

2.0.8 把 Cursor 的 GenerateImage 工具从空壳 stub 接到真实上游 OpenAI 兼容 images API，文生图与图生图均端到端实测通过（`gpt-image-2`：文生图约 2-3min、图生图约 1.5min 出图）。配置见 [docs/文生图与图生图配置指南.md](./docs/文生图与图生图配置指南.md)。

### GenerateImage 异步化（治本）

- 原实现：`handleGenerateImageToolInvocation` 在 stream actor goroutine 上**同步**调上游 images API（~93s）→ starve mailbox → Cursor 并发 BidiAppend 触发 mitm 60s ResponseHeaderTimeout → 499 → provider loop interrupted。图实际生成但流已死。
- 改造：`PendingImages` map + `image_result` intent kind。actor 上只登记 pending + 派 goroutine + return nil；`pendingBridgeCount` 计入 PendingImages → provider 流进 TurnPhaseWaitingExternal 暂停；goroutine 跑完通过 `dispatchInboundIntent({Kind:"image_result"})` 回投 mailbox，`handleImageResult`（actor 串行）走尾 + terminal。
- **生图超时**：部分文生图正常就要 120s+。移除 `http.Client.Timeout`（整请求硬上限会砍慢请求），改 Transport 级 `DialContext`(30s) / `ResponseHeaderTimeout`(10min) / `IdleConnTimeout`(90s)，整体看门狗交 ctx（`runImageGeneration` 给 15min）。

### 图生图：直取 inline 参考图（守 F-30，不落盘）

- 根因：用户上传图走 `SelectedImage` inline data，但 F-30 清空 Path、只留 inline data，图无工作区路径；而图生图工具参数 `reference_image_paths` 要工作区路径——模型填不出，退化成文生图。
- 治本（方案 A，经两轮协商用户否决落盘）：`ActiveStream.CurrentTurnSelectedImages` 在 `handleRunIntent` 入站快照本轮上传图 inline data（实测到达形态 `blob_id_with_data`，data 在 `BlobIdWithData.Data` 字段）；`handleGenerateImageToolInvocation` 在 `reference_image_paths` 为空时直取 inline 走 `/v1/images/edits`。不落盘、不写工作区、不依赖模型填路径。

### 图生图 multipart MIME 修复

- Go stdlib `multipart.Writer.CreateFormFile` **写死** `Content-Type: application/octet-stream`，上游 `/v1/images/edits` 校验 data URL 的 image MIME 直接 400 拒绝。
- 改 `CreatePart` + 显式 `Content-Type`：新 helper `imagePartContentType`（声明 mime 优先、否则 `http.DetectContentType` 嗅探、**绝不返回非 image/\***、兜底 `image/png`）；`imageReference` 加 `mimeType` 字段从 `SelectedImage.MimeType` 透传。测试 `TestImagePartContentType` 锁死契约。

### 上游 SSE 残缺尾行容错

- 根因：上游 `gpt-5.6-terra` 流式中途 TCP reset，留下半截 `data:` JSON 行，`bufio.Scanner` 当最后一行交上来，`json.Unmarshal` 报 `unexpected end of JSON input` → 整轮死（含尚未调用的图生图工具）。
- 治本：openai.go chat/responses 两处 SSE 解析遇 `json.Unmarshal` 失败时，**已发过有效事件**则跳过残缺行 `break` 走 flush + 正常收尾（复用 P1-2 graceful-drain 路径），零事件仍 fail（保留 F-20 截断判定 + failover）。新增日志 `openai ... stream: skipping malformed tail payload after emitted content`。

### adapter Role 字段（独立生图 adapter）

- `ModelAdapterConfig.Role`（chat/image/both）：`image` 类型 adapter 可独立配置（不依赖 chat adapter 的 ModelID 命中）；`Role==image && ModelID=="" && ImageModelID!=""` 时 ModelID 兜底成 ImageModelID 绕过必填；`Role==image` 时 ImageModelID 必填。
- 透传 config→runtime→resolver；`ChannelResolver` 接口加 `SelectChannelForImage(ctx)`；`resolveImageChannel` 两段兜底（先按 chat 模型命中复用，否则取全局 Role=image/both adapter）。前端两编辑器加 Role Select。
- **前端校验 bug 修复**：`validateModelAdapters` 原死拦「ModelID 必填」，致纯 image adapter 存不下来（与后端兜底矛盾）。改 Role 感知校验：chat→ModelID 必填；image→ModelID 可空但 Image 模型必填；both→两者各自必填。

### 其他

- 生成端点 prompt 提示微调：图生图时 description 是改图指令，上传图自动可用无需填 `reference_image_paths`（agent/multitask 两处 tools.json）。
- 测试：`adapter_role_test.go`、`image_inline_refs_test.go`、`image_async_test.go`、`image_edit_test.go`、`image_generation_test.go`、`appState.test.js`（role-aware 校验 3 例）等。go build/vet/test + 前端 vitest + vite build 全绿。

---

# 2.0.7

## Web 工具增强（免费、零部署、惠及所有用户）

2.0.7 收口独立全量审计（[docs/AUDIT_2026-07-29_独立全量审查.md](./docs/AUDIT_2026-07-29_独立全量审查.md)）中「行为偏离-3」的两项外网工具强化——全部免费、零部署，无需任何配置即开箱可用。

### WebSearch 多 provider SDK + DuckDuckGo JSON lite 端点

- **多 provider BYOK**：WebSearch 不再硬编码 DuckDuckGo HTML 抓取。在「Web 工具」配置卡选 Bing / Serper / Tavily，填对应 API key 即走各家官方 HTTPS JSON（自带 key = BYOK，质量与时效性远优于 HTML 抓取）。`dispatchWebSearch` 按 `WebToolsConfig.WebSearchProvider` 分派；空/非认可值回退 DuckDuckGo 免 key 降级。
- **缺 key 显式告警**：选了需 key 的 provider 但未填 key → 返回 `errWebSearchAPIKeyMissing`，工具结果显式告警「需配置 API key」，不静默返回空（与后端缺 key 错误对齐）。前端 `Config.vue` 加 provider Select + API key Input + 缺 key 告警条。
- **DuckDuckGo JSON lite 端点**：不配 provider 时首选 DuckDuckGo **Instant Answer JSON 端点**（`api.duckduckgo.com/?format=json&no_redirect=1&no_html=1`，官方、结构化、不依赖易变 HTML class，**更稳**）；空结果/失败回退原 HTML 抓取路径（`executeDuckDuckGoHTMLSearch`），保证覆盖率不降。`parseDuckDuckGoInstantAnswer` 纯函数解析 Abstract 摘要 + 递归 RelatedTopics，按 URL 去重。
- **关键判断**：经核实 DuckDuckGo 无公开完整搜索结果 JSON API（官方 `api.duckduckgo.com/?format=json` 仅返回 Instant Answer 摘要，非完整网页结果列表，多数查询返回空）。选 Instant Answer 作首选、空则回退 HTML，是「更稳 + 不丢结果」的稳妥折中。

### WebFetch 正文 LRU 缓存 + 内网 host 白名单

- **正文 LRU 缓存**：`executeWebFetch` 入口查进程内 LRU 缓存（`globalWebFetchCache`，4MiB 上限 + 10min TTL，`container/list` + map，O(1) 命中/淘汰）。命中即原样返回最终 payload（已转 markdown + 截断），跳过 HTTP + readability 全流程，降延迟与被封概率。URL 规范化为键（去 fragment / 默认端口 http:80/https:443 / host 小写 / query 按字典序排序），不同写法命中同一缓存项。失败不缓存（让下游重试）；空 payload 不缓存；单条超总容量不缓存（不独占）。
- **内网 host 白名单**：`safehttp` 加包级 allowlist 表（`SetHostAllowlist`/`IsHostAllowlisted`），`ResolveAndValidateHost` 对白名单 host 放行私网解析（即便解析到内网 IP 也返回首 IP）；`executeWebFetch` 入口 `syncWebFetchAllowlist` 同步配置；`validateWebFetchURL` 查同一全局表跳过字面拒绝。前端 `Config.vue` 加白名单 textarea（逗号/换行/空格分隔），空表 = 保持 SSRF 硬拒绝基线（最安全）。白名单是「用户显式放行」叠加在硬编码安全基线之上，不削弱默认防护。

### 按路由面官方透传开关（@codebase / @docs / Repository Index）

@codebase / @docs / Repository Index 的真实向量检索依赖 Cursor 云索引，纯本地 BYOK 无法实现。改为在「按路由面覆盖」里给 `file_sync`（Codebase/Repository）与 `network_service`（@docs）面加官方透传开关：切「直连 Cursor」即用本人 Cursor 账号的真索引走上游透传（`repositoryServiceProcedure`/`uploadServiceProcedure` 的 `CredentialOriginalCursor` 分支，N-33）；留「本地」=桩化登记（**安全特性，阻断代码上传到第三方 provider，非缺陷，不「修」成真实上传**）。默认仍全本地。本地嵌入模型暂不实现（画蛇添足）。

### 审计 P0/P1 闭合

2.0.7 一并闭合独立审计的 P0/P1 项（逐条 ✅ 标注 + 修复历史见审计文档第九节）：

- **P0-1/P0-2**：AwaitShell 正则 goroutine 泄漏 + 后台 shell 无界 buffer → OOM。RE2 无回溯故放弃嵌套量词检测，改 32KiB 尾截断输入；256KiB 上限 + `appendShellStreamBuffer`/`clampShellBuffer` 有界 helper。
- **P0-3**：half-open permit 漏放致渠道永久卡死。新增 `ReleaseHalfOpenPermit`（仅释放名额、不污染统计），非 provider 失败路径（client cancel / sink 写失败）显式调用。
- **P0-4**：state.vscdb 与运行中 Cursor 进程并发写竞争。`RepairLegacyInjectedIdentity` 入口加 `isCursorProcessRunning()` 检测（unix pgrep / windows tasklist），Cursor 运行时跳过修复。
- **P1-2**：F-20 截断判定过激（无 `[DONE]`/`message_stop` 的正常完成被判截断，已发文本丢失）。OpenAI/Anthropic 适配器加 `emittedAny` 闸门，已发有效事件的 EOF 不再判截断。
- **P1-3**：`applyChannelToRequest` 原地修改共享 `RequestKnobs` map 跨候选污染。入口对 map 做深拷贝。
- **P1-4**：熔断错误率不滑动致健康渠道反复熔断。加滑动窗口（50）`recentErrorRateLocked`，`transitionToClosedLocked` 重置窗口。
- **P1-5**：EventIndex prune 淘汰旧 provider_call 致 turn 计费漏算。`pruneUsageEventIndex` 改为只淘汰已有 `turn_finalized` 落盘的 provider_call；`turn_finalized` 免 prune。
- **P1-7**：Config.vue 运行模式 Select 不持久化。改 `@update:model-value` 立即持久化（乐观更新 + 失败回滚）。
- **P1-8**：乐观更新回滚与磁盘真值脱节。失败时先 `reloadUserConfig` 对齐磁盘再回滚。
- **行为偏离-1**：prefix cache 跨天失效（日期串用 `time.Now()`）。日期串移至 latest-only 段尾，稳定前缀跨天不变。
- **P2-1**：`estimateDailyCost` 用 input 价平方做权重。改 token 占比加权。
- **P2-7**：EChart 无 ResizeObserver。加 ResizeObserver 观察容器。
- **P2-8**：MainLayout 后台轮询不暂停。改监听 `document.visibilitychange`，隐藏时暂停。
- **P3-1**：死代码清理。删 `local_relay_bridge.go`/`directUpstreamProcedure`/`ParseAndValidateRawURL`/`InjectCursorUserInfo` 及辅助函数。

### 测试

`go build ./...` ✓ · `go vet ./internal/backend/agent/bridge/interaction/` ✓ 零 warning · `go test ./...` ✓ 全绿。新增测试：`websearch_provider_test.go`（多 provider 分派 + Bing 解析 + Instant Answer 解析 + host 白名单）、`webfetch_cache_test.go`（LRU 命中/过期/淘汰/键规范化/超限跳过）、`circuit_breaker_sliding_test.go`、`router_halfopen_permit_sink_test.go`。

### 用户约束边界

- **「不需要画蛇添足」**：Tab 补全不实现本地补全模型，仅标注 + 告警；@codebase/@docs/Repository 桩化不「修」成真实上传（桩化是安全特性）。
- **拒绝 ToS 绕过**：不设计多账号轮换/账号共享绕过。官方透传仅限「用本人 Cursor 账号额度」。

---

# 2.0.6

## 第三轮全量审计 P0–P3 全部闭合（N-01 ~ N-40）

2.0.6 收口 2026-07-29 第三轮全量静态审计（详见 [docs/AUDIT_2026-07-29_全量.md](./docs/AUDIT_2026-07-29_全量.md)）的全部可修项——P0/P1/P2/P3 逐条修复，每项独立 commit、全量 `go test ./...` 绿、审计文档内逐条 ✅ 标注 commit 号。按修复面归纳：

- **正确性 / 计费**：half-open 探测名额泄漏致渠道永久卡死（N-01）、turn_finalized 事件携带 ModelID/Provider 修复 dashboard 误标 CalibrationAnomaly（N-12）、anthropic 无参工具 `input_schema` 归一化（N-03/N-23）、AwaitShell 实现真正阻塞等待而非一次性快照（N-04）、@docs 上传抓取页面正文入库（N-21，复用 safehttp SSRF 防护）、EventID 分隔符 `::`→`\x1f` 消除 requestID 含 `::` 时的跨 ID 计费串账（N-38）。
- **路由 / 排障**：放宽 401/403/404 failover 让 BYOK per-channel key 失效可切备用（N-02/F-07）、全候选熔断时保留并透传真实失败原因而非只剩 "circuit open"（N-39）、`IsAvailable` 改纯只读消除排序期提前触发 Open→HalfOpen 的副作用与并发双探测隐患（N-40）。
- **崩溃一致性**：A6 Cursor settings.json 接管/还原/崩溃恢复一致性加固（N-10/N-11/N-25/N-26/N-57）。
- **性能热路径**：工具 args 字段搜索增量偏移消除每 delta 全扫 O(n²)（N-07）、文本/思考 delta 累加改 `strings.Builder`（N-27）、artifacts 热路径 RWMutex 读锁 + debug 关闭短路（N-09）、消除 appendConversationEntries 双重克隆（N-05）、publishCheckpoint 合并重复 ProjectPromptReplay 投影（N-06）、usage_store 合并 Lookup+Upsert 为单次读改写（N-28）、GetThoughtAnnotation miss 回退按 recency 扫描并命中即短路（N-29）。
- **云端一体化透传（默认关）**：codebase/docs 面新增 `CredentialOriginalCursor` 透传选项（N-33/N-35），默认仍全本地，仅当用户在「按路由面覆盖」显式设为直连才生效。
- **可观测性**：OpenAI Responses 加密 thinking 占位文案诚实化——本地无法解密的加密推理显式标注而非伪造明文（N-34）；UsageDashboard 路由切走清理 setInterval 泄漏（N-37）。

> 未修项（架构决策 / 非纯代码）在审计文档中标注为待办，不阻断本次发版。

## 小米 MiMo 上下文窗口数据修复（消除 25K/128K 分母错误）

- **问题**：用户反馈小米 MiMo（如 `mimo-v2.5-pro`）在 Cursor 右下角显示 "25K/128K"，但小米官方上下文窗口实际是 1M。根因：内置 `contextWindowByModelID` 表把小米 v2.5 系列全标 `128_000`——小米 `/v1/models` 端点不返回 context window 字段（大多数 OpenAI 兼容 provider 的通病），在线回退源 models.dev 又没收录小米，错误值无法被纠正，fetch-models 把错的 128K 回填给 Cursor。
- **实测确认**：直连 `api.xiaomimimo.com` 问模型本身，`mimo-v2.5` / `mimo-v2.5-pro` / `mimo-v2.5-pro-ultraspeed` 均自报 `1,000,000`。`mimo-v2-pro` / `mimo-v2-flash` 已下线（"Unsupported model"），但保留并改 1M 与 v2.5 系列同口径，老配置渠道命中也合理。
- **修复**：`internal/client/model_context_window.go` 小米块 4 条 `128_000` → `1_000_000`，新增 `mimo-v2.5-pro-ultraspeed`。用户重新「获取模型列表」或重启后，分母显示为 1M（"25K/1M"），比例恢复正常。
- **关于 25K 分子**：一句话对话就 25K 不是 bug——这是 Cursor 客户端**本地估算**的当前会话上下文占用（含 Cursor 自己注入的系统 prompt + 工具定义，本身就 10-20K token），与 cursor-switch 无关。分母 128K 才是 bug，已修。
- **测试**：`model_context_window_test.go` 3 场景——MiMo 系列返回 1M / 带后缀变体候选匹配仍命中 1M / 硬断言不得回退 128K 错误值。

## 额外参数 / 自定义请求头教学文档

- **问题**：模型编辑器的「额外参数 JSON」「自定义请求头 JSON」字段大部分用户不会配也不理解，配错要么被静默丢弃（F-19 denylist）要么导致请求失败，此前没有任何教学。
- **修复**：新增 [docs/额外参数与自定义请求头配置.md](./docs/额外参数与自定义请求头配置.md)，README + README_EN 顶部文档条 + 主要功能「模型配置」段链接过去。内容含：字段语义、F-19 黑名单（`stream`/`model`/`messages`/`input`/`tools`/`tool_choice`/`system`/`instructions`/`metadata` 不可覆盖；`Authorization`/`x-api-key`/`Host`/`Cookie` 不可设）、验证过的常用配置（OpenAI `service_tier` / 采样参数 / 中转自定义识别头 / Cloudflare 头 / API 版本头）、值必须是字符串的坑、与「获取模型列表」的关系、常见误区。所有示例均对齐实际校验规则（`validateHeadersJSON` 值必须字符串、`blockedCustomHeaders`/`blockedExtraParamKeys` 大小写不敏感）。

# 2.0.5

## F-01 多 Provider Failover 在正常 UI 选模链可达

- **问题**：B2 已修好多 provider 候选链 + failover 核心（router/resolver/circuit breaker 全就绪且有单测），但用户从正常 Cursor UI 选模型时，`mocks.go` 暴露给 UI 的模型标识是**渠道 ID**（`adapter.ID`）而非逻辑 modelID。UI 回传渠道 ID 后，`ResolveAdapterIndexes` 第 1 层精确 ID 匹配命中唯一 adapter，第 3 层 providerModelID fallback 永不触发——同 modelID 多 provider 的候选链恒为 1，主渠道失败不切备用。**用户配了候选链却走不进去。**
- **修复**：`mocks.go` 三处暴露标识从渠道 ID 改为逻辑 modelID：
  - `collectModelAdapterRefs`（`defaultModel`/`fallbackModels`）返回 modelID，按 Priority 升序、按 modelID 去重——同 modelID 多 provider 在列表里只出现一次（选中任一都激活该 modelID 全部候选链）。
  - `buildAvailableModelEntries` 的 `name`/`serverModelName` 改 modelID。
  - `buildThinkingEffortVariants` 的 `variantStringRepresentation` 改 `<modelID>:<effort>`——UI 按 thinking intensity variant 选中后 `splitRuntimeThinkingEffortVariantString` 拆出 modelID。
  - UI 回传 modelID → `ResolveAdapterIndexes` 第 1 层精确 ID（渠道 ID）不命中、第 3 层 providerModelID fallback 命中所有同 modelID 的 enabled adapter → 候选链 >1 → B2 failover 在正常 UI 选模链可达。
  - 默认模型按 Priority 选（新增 `orderAdaptersByPriority`，与 `config/resolver.go` 候选链排序同口径），UI 默认选中项即候选链主候选。
- **同 modelID 多 adapter 的 UI 呈现**：采"每 adapter 一条 entry + displayName 区分"方案（不合并）——下拉可能出现同名项（name=modelID），但选任一都回传 modelID 激活整条候选链，这是 failover 冗余配置的预期态；用户给主备 adapter 配不同 displayName 即可在下拉区分。
- **测试**：`mocks_f01_failover_test.go` 3 端到端（暴露 modelID 契约 / 默认模型按 Priority / failover 经 modelID 可达）+ `mocks_disabled_filter_test.go` 更新断言（name=modelID）+ 加 dedupe/Priority 测试。resolver 层"传 modelID 返回多候选"已由 `config.TestResolveModelAdapterChannelsReturnsAllCandidates` / `modelchannel.TestResolveAdapterIndexesReturnsAll` 覆盖。
- **未做真机验证**：Cursor 客户端是否对 `name` 字段格式有假设（之前是渠道 ID 字符串，现是 modelID）需真机点选验证；纯后端逻辑链已闭合。

## L7 stale exec control 区分"已处理"与"从未存在"（可观测性）

- **问题**：`shouldIgnoreMissingExecControl`（stream 存在但 pending exec 找不到时）经 `shouldIgnoreStaleExecControl` 对 Heartbeat/StreamClose 一律静默吞。重连客户端迟到的控制消息确实是传输级噪声（合理忽略），但若 pending exec 被错误清除也表现为 missing，无条件吞会掩盖真实协议错误。
- **修复**：拆出 `isStaleTransportExecControl` 复用于两处。`shouldIgnoreMissingExecControl` 对传输级控制消息先查 `recentlyCompletedExecExists`——已处理则忽略（合理）；**从未存在则仍忽略但记 WARN**（`... never existed; ignored, may indicate protocol drift`），让真实协议异常可被诊断。
- **关键取舍**："never existed" 必须**返回 true 不杀流**——若返回 error 会经 `actor.go` 的 `failStream` 把整个流标失败，重连客户端迟到的 Heartbeat 会误杀整个对话，比静默吞更糟。故选"忽略 + WARN"而非"surface error"。`shouldIgnoreStaleExecControl`（stream 已不 active 场景）转调 `isStaleTransportExecControl` 保持原静默吞语义。13 回归场景。

## L3 前端 normalizer 回归测试

- **问题**：`appState.js` 的 normalizer（`normalizeModelAdapter`/`normalizeModelAdapters`/`normalizeConfig`）是前端 config 层契约边界——F-02（payload merge 透传）、L5（旧品牌 key 迁移）、v2.0.3（per-namespace 路由）、A6（cursor 配置）等改动都经手这些函数，但前端此前零自动化测试，回归只能靠人工点 UI。
- **修复**：引入 `vitest` + `happy-dom` 最小测试环境。`frontend/vitest.config.js` 故意不复用 `vite.config.js`（后者挂载 wails 插件，纯 Node 测试环境会拉起 wails 绑定导致加载失败），只设最小 `@`/`@bindings` 别名 + happy-dom（提供 localStorage/window，appState.js 模块加载时 `migrateLegacyStorageKeys`/`loadCachedState` 依赖之）。`appState.test.js` 12 场景锁住契约：baseURL 归一（协议/host 小写、去尾斜杠、非 http(s) 置空）、字段别名契约（`baseURL||url`、`apiKey||key`、数值字段接受 snake_case）、未知 type 置空、非法 reasoningEffort 回落 medium、enabled 默认 true、costMultiplier F-02 透传、openai 专属字段仅在 openai 类型生效、非数组归一为空数组、空 config 全默认、perNamespace 清洗非法值丢 auto、mode 非法值回落 local、round-trip 幂等。为测 `normalizeConfig` 导出之（纯函数，导出无副作用，生产代码仅加一个 export 关键字，行为零变化）。`package.json` 加 `test`/`test:watch` 脚本。
- **未做**：ESLint（维持 2.0.2 既定暂缓决策——前端无现成 lint 基础设施，投入产出比低）、i18n 快照测试（`static-i18n-plugin.js` 是构建期转换，快照价值低于 normalizer，留后续）。normalizer 单测是 L3 高价值核心，先行落地。

## A6 Cursor 配置接管安全网（备份/还原/崩溃恢复）

- **问题**：cursor-switch 接管时 `WriteUserProxySettings` 改写用户 Cursor `settings.json` 注入 5 个代理键（`http.proxy` 等）。原 `ClearUserProxySettings` 退出时直接 delete 这些键——**若用户接管前有自己的 `http.proxy`，退出后用户原始代理配置丢失**（被覆盖再被抹掉，而非还原）。若崩溃没来得及 Clear，下次启动在已污染 settings 上再注入，备份污染值还会覆盖真实原始值。
- **修复**（对齐 cc-switch `proxy_live_backup` + `live_takeover_active`）：
  - **接管前备份**：`WriteUserProxySettings` 注入前把被覆盖键的原始值（含"接管前是否存在"标记）快照到 `~/.cursor-local-assistant-v2/data/cursor-settings-backup.json`（0700 目录 + 0600 文件）。仅当无已有备份时写——防"接管→崩溃→重启→又备份当前注入值"覆盖真实原始值。
  - **退出还原**：`ClearUserProxySettings` 不再简单 delete，从备份还原——接管前存在的键写回原始值（用户原始代理回来），接管前不存在的键 delete。无备份退回旧 delete 语义。还原后清备份。
  - **崩溃恢复**：`ApplyCursorSettings` 注入前先 `RestoreCursorSettingsFromCrash`——检测 settings 残留注入键 + 有备份 → 上次非正常退出 → 据备份还原原始配置；残留但无备份 → best-effort 清注入键退回"无代理"。
  - **B3 切换锁**：已由 F-35 的 `lifecycleMu` 串行化 `StartProxy`/`StopProxy`/`SaveUserConfig` 覆盖，`ApplyCursorSettings`/`ClearCursorSettings` 都在锁内调用不会并发半切换，无需额外锁。
- **测试**：`settings_backup_test.go` 8 场景——备份+还原核心契约（接管前有代理→退出还原原始值而非抹掉）/ 无原始代理退出删键 / 崩溃恢复从备份还原 / 崩溃恢复无备份清残留 / 无残留 no-op / 备份不覆盖防污染 / 逐键还原逻辑 / 无备份退回 delete。

## A3 Thinking Signature 整流器（自愈式重试）

- **问题**：走 Anthropic 兼容路由的第三方中转（DeepSeek/Kimi/Qwen/GLM/… 回签 Anthropic 风格响应的）实现 thinking signature 参差，常回签无效签名。cursor-switch 把会话历史里的 assistant 推理内容与签名原样回带上行，provider 校验签名失败直接返回 HTTP 400——而 400 属不可重试错误，Router 不会 failover，签名错误原样透传给用户，对话中断。
- **修复**：adapter 层加 thinking signature 整流器（`internal/backend/agent/model/thinking_rectifier.go`，对齐 cc-switch `thinking_rectifier.rs`）。首试若命中签名类错误（且流尚未首字节、本请求未整流过），自动剥离会话历史里 assistant 消息携带的推理内容与签名（`ReasoningContent`/`ReasoningSignature`/`ReasoningSignatureSource` 与 OpenAI Responses 推理字段），让 provider 视作"无 thinking 历史的新请求"重试一次，绕开签名校验。
- **为何在 adapter 内而非 Router failover**：签名错误是 HTTP 400，`isRetryableChannelError` 视为不可重试，Router 直接透传；根因是会话历史里的无效签名，换候选治标不治本——同一份历史发到下一个候选大概率还是 400。必须整流消息本身。整流重试发生在 sink 首字节之前（provider 在流开始前就拒绝请求），与 Router 的 `sinkStarted` failover 闸门正交不冲突。
- **安全闸门**：① `shouldRectifyThinkingSignature` 只匹配 cc-switch 对齐的 7 个签名错误场景（thinking block 签名无效 / Thought signature not valid / must start with a thinking block / expected thinking found tool_use / signature field required / signature extra inputs not permitted / thinking blocks cannot be modified / 非法请求兜底），普通 400 不触发；② 本地 `sinkStarted` 闸门——首字节已发绝不重试（避免双发）；③ `rectifiedOnce` 闸门（`RequestKnobs["thinking_rectified"]`）——只整流一次，二次失败透传给上层由 Router 决定，避免与真正坏掉的 provider 死循环；④ 客户端已取消不重试。
- **接入**：`AnthropicAdapter.Stream` / `OpenAIAdapter.Stream` 经 `streamWithThinkingRectifier` 包装，原流逻辑下沉为 `streamOnce`（方法体一字未改）。OpenAI 原生协议不含 thinking signature 概念，但走 Anthropic 兼容路由的中转会把签名错误透传进来；判定只在错误形态匹配时触发，对纯 OpenAI 路径零副作用。
- **测试**：`thinking_rectifier_test.go`——`shouldRectifyThinkingSignature` 14 场景（7 触发 + 反例）、`rectifyMessagesForThinkingSignature`（assistant 推理清空 / user·tool 不动 / 深拷贝不改入参 / 无改动返回 nil,false）、`streamWithThinkingRectifier` 7 端到端（签名错误重试成功 / 非签名不重试 / sinkStarted 不重试 / 已整流不重试 / 无可剥离不重试 / 二次失败透传 / 取消不重试）。

# 2.0.4

## Token 口径异常修复（消除 487 条误报 + 成本低估）

- **根因**：usage.json 落盘的 `input` token 在两类 adapter 里都已折算成 **fresh_input**——Anthropic adapter 原样存 API 返回的 `input_tokens`（本就只算非缓存部分），OpenAI adapter 存 `prompt_tokens - cached_tokens`。所有 OpenAI 兼容 provider（DeepSeek/Gemini/Grok/Kimi/Qwen/GLM/MiniMax/MiMo/Doubao/…）都经 openai.go 同一条路，落盘 input 一律 fresh。但内置 seed 价目表此前把 OpenAI 标 `TOTAL`（假设 input 含 cache_read+cache_write）、Anthropic 标 `legacy`（假设 input 含 cache_read），与落盘口径**矛盾**——`billableInputTokens` 走 TOTAL/legacy 分支重复扣 cache（clamp 到 0）导致成本低估，`isCalibrationAnomaly` 走 TOTAL/legacy 分支几乎对每条带缓存的请求误判异常（input 恒小于 cacheRead+cacheWrite），仪表盘持续红字「⚠ 检测到 487 条请求存在成本口径异常」。
- **修复**：内置 seed 全部改标 `FRESH`（与 adapter 落盘口径一致）。`billableInputTokens` FRESH 分支原样返回 input → 成本 = fresh_input×input 价 + cacheRead×cacheRead 价 + cacheWrite×cacheWrite 价，缓存部分各计一次；`isCalibrationAnomaly` FRESH 分支恒 false → 误报清零。等价 cc-switch 在 SQL 数据层把所有 provider 归一 fresh_input 的做法，区别是 cursor-switch 在 adapter 落盘时归一、计算层按 FRESH 直读。
- **老用户迁移**：`normalizePricingConfig` 给命中 seed 的内置记录把残留 TOTAL/legacy 语义迁到 seed 当前值（FRESH），仅迁语义标签不动用户编辑过的价格/倍率；自定义模型（非 seed）的语义原样保留。确保老用户磁盘配置里的 `gpt-5.6-sol: TOTAL`、`claude-opus-5: legacy` 升级后自动生效，487 条误报立即消失。逻辑删除（Disabled）的内置记录同样迁移，恢复默认价后语义正确。
- **测试**：`TestSeedPricingAllFresh`（seed 全 FRESH 回归保险）、`TestNormalizeMigratesBuiltinSemanticsToFresh`（TOTAL→FRESH 迁移 + 价格保留 + 自定义模型不迁）、`TestDisabledBuiltinAlsoMigratedToFresh`（禁用记录也迁）。原 `TestMergeUserPatchPreservesPricing` 改用非 seed 模型 `my-custom-model`，避免被迁移干扰真正测出「用户 pricing 编辑被保留」契约。
- **未动**：adapter 落盘逻辑、usage.json 历史数据、`billableInputTokens`/`realTotalTokensForModel`/`isCalibrationAnomaly` 计算分支全不动——只改 seed 标注 + 加迁移。TOTAL/legacy 语义保留在计算层供自定义模型按其 provider 原始口径标注（罕见，adapter 已统一归一）。

## 定价管理编辑 UX 优化

- **编辑器自动滚入视口**：此前点「编辑」后编辑 UI 出现在表格最下方，长列表滚动条不自动到底，用户误以为点击无效。现 `openEdit`/`openAdd` 在 `nextTick` 后把编辑器 `scrollIntoView({block:'center'})` 并聚焦首个可编辑输入框，编辑器加蓝色 ring 高亮，打开即明确可见可输入。
- **表格价格行内直接编辑**：输入/输出/缓存读/缓存写 四列价格单元格可点击就地变 input，回车保存 / Esc 取消 / 失焦保存。规避「点编辑滚到底下找表单」的来回，常用微调（改输入价/缓存价）就地完成。modelId/displayName 不开放行内编辑（是键/标识，仍走下方完整编辑器）。重入保护防 Enter+blur 双发请求。

# 2.0.3

## 按路由面覆盖（per-namespace 路由）

- **第二部分「优先级 2」能力损失优化**：此前 `Routing.Mode` 是全局开关——要么全本地 byok，要么全直连 Cursor。现细化为**按面配置**：`Routing.PerNamespace` 按 route name 覆盖全局 Mode，单条路由可独立选 `local`（byok 本地）/ `upstream`（透传本人 Cursor 账号），未列出则跟随全局。
- **实现**：`RoutingConfig` 加 `PerNamespace map[string]string`；`Manager.RouteModeFor(hasUpstreamURL, routeName)` 优先查覆盖表回退全局；`PolicyMiddleware` 改按 `ctx.RouteName` 查表（`ctx.RouteName` 本就每请求填充，无需新管道）。native 请求（无 UpstreamURL）恒 local，覆盖也无效——本地直连没有上游可切。`normalizePerNamespace` 清洗非法值并丢弃"auto"（等价不覆盖），全空返回 nil 向后兼容旧配置。
- **前端**：Config 页加「按路由面覆盖」面板——Tab 补全 / Codebase 索引 / @docs 文档 三条高价值「云端一体化服务」各一 Select（跟随全局 / 本地 byok / 直连 Cursor），单条即时保存。推理面（RunSSE / BidiAppend）刻意不暴露——强制本地是项目目标。把 codebase/docs 设为直连即对应审计「优先级 1」：透传到本人 Cursor 云端语义索引（代码/文档会经 Cursor 云，用户知情）。
- **测试**：7 config 层（normalize 清洗/RouteModeFor 覆盖胜全局/native 恒 local/覆盖双向/NormalizeConfig 往返/空保持 nil）+ 4 PolicyMiddleware 层（按路由分流/native 恒 local/全局 upstream 下覆盖 local/空 RouteName 回退）。

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


