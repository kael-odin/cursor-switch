# `cursor-switch` 全量静态审计报告

## 1. 审计结论

本轮保留 **37 项问题**：

| 严重度 | 数量 |
|---|---:|
| 严重 | 0 |
| 高 | 11 |
| 中 | 23 |
| 低 | 3 |

未发现可直接定性为远程代码执行、更新签名绕过或公网开放代理的问题。当前主要风险集中在：

1. **本地代理和独立 Tab Server 的信任边界不足**
2. **只读模式、工作区围栏和本地文件读取缺少后端强制**
3. **配置采用整包覆盖且缺少事务更新**
4. **failover 的模型身份与错误分类不完整**
5. **流式协议、请求体、日志、缓存和内存缺少全局资源预算**
6. **迁移和会话持久化存在不可恢复的数据一致性窗口**

---

## 2. 审计基线与限制

- 仓库：`D:\Github_Open\cursor-byok`
- 分支：`main`
- 最后直接读取到：
  - `HEAD`：`refs/heads/main`
  - `main`：`d927cc612bcf36c83e79b8214078beae95d9542f`
  - `origin/main`：`d927cc612bcf36c83e79b8214078beae95d9542f`
- Origin：`https://github.com/kael-odin/cursor-switch.git`
- 构建配置版本：`1.1.0`

审计过程中检测到仓库由其他流程从 `d48cef82…` 更新到 `d927cc61…`。根据用户决定，没有对新提交重新执行完整核对。因此：

- 下述结论来自提交切换前后读取到的工作区快照。
- 行号是审计读取时的工作区位置，不是固定提交的永久链接。
- **不能证明 37 项都已在 `d927cc61…` 上重新复现。**
- 审计代理未修改或回退被审计代码；本文件是用户明确要求生成的审计产物。

### 动态验证状态

终端持续返回：

- `Missing terminal shell stream event`
- shell transport closed
- 无可信 stdout、退出码或后台任务标识

因此不能声明以下命令通过：

- `git status --short`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build ./...`
- 前端生产构建

`frontend/package.json` 只有 `dev`、`build:dev`、`build`、`preview`，没有现成的前端 `lint` 或 `test` 脚本；同时未发现已安装的 `frontend/node_modules`。

---

# 3. 高严重度问题

## F-01 高：正常 UI 选模链无法进入多 Provider Failover

**证据**

- `internal/backend/server/upstream/mocks.go:432-470` 把第一个渠道 ID 设为默认模型。
- `internal/backend/server/upstream/mocks.go:636-678` 同时把渠道 ID 写入 `name` 和 `serverModelName`。
- `internal/modelchannel/resolve.go:74-124` 遇到唯一渠道 ID 时只命中该 adapter；只有直接提交原始 provider model ID 才可能收集同模型的多个 adapter。
- `auto/default/fast` 最终也回退到第一项 adapter ID。

**影响与触发条件**

配置两个相同 `modelID` 的 provider 后，从正常 Cursor UI 选择模型，Router 通常只得到一个候选。Stage 2 failover 对正常桌面使用链基本不可达；主渠道失败时不会切备用渠道。

**复现思路**

配置两个 enabled adapter，使用相同 `modelID`、不同 ID 和 Priority，让第一个返回 500。分别提交：

- UI 返回的渠道 ID：预期只尝试第一个。
- 原始 provider model ID：可能得到两个候选。

**修复与测试**

模型选择器应暴露逻辑模型 ID 或独立的 route-group ID，渠道 ID 只标识具体 provider。默认模型应从 enabled adapter 按 Priority 选择。增加 `AvailableModels → RequestedModel → ResolveAdapterIndexes → Router` 端到端测试。

---

## F-04 高：Conversation ID 允许 `"."`、`".."` 导致目录逃逸

**证据**

- `internal/backend/forwarder/file_store.go:791-799` 只拒绝空值和路径分隔符。
- `internal/backend/forwarder/file_store.go:650-662` 直接把 ID 作为目录段拼接。
- `"."`、`".."` 不包含分隔符，但会改变最终目录。

**影响与触发条件**

可让 `state.json`、`context.json`、锁文件、debug/provider JSONL 等写入预期 conversation 子目录之外。结合 F-15 的本地代理未认证问题，其他本机进程可能触发该路径。

**复现思路**

分别以 `"."` 和 `".."` 创建或更新 conversation，观察最终路径是否落到 history 根目录或其父目录。

**修复与测试**

拒绝：

- `"."`、`".."`
- 绝对路径
- 任意平台分隔符
- `filepath.Clean` 后变化的值

拼接后再用 `filepath.Rel` 验证最终路径仍在 conversation 根目录内。测试需覆盖 debug artifact 路径。

---

## F-13 高：`state.json` 与 `context.json` 缺少崩溃一致性

**证据**

- `internal/backend/forwarder/file_store.go:444-477` 分别读取 state 和 context，但只返回 context 的 `Items`。
- `internal/backend/forwarder/file_store.go:480-488` 先写 context，再写 state。
- `internal/backend/forwarder/file_store.go:491-524` 两个文件分别原子替换。
- 加载时没有比较 `state.ContextVersion` 与 context 的 `Version/ConversationID`。
- `internal/backend/forwarder/file_store.go:802-837` rename 前关闭文件但未执行 `file.Sync()`。

**影响与触发条件**

进程在两次 rename 之间退出时，会留下“新 context + 旧 state”。由于加载逻辑不校验版本，这个组合会被当作有效对话继续运行，可能造成状态回退、对话恢复错误或重复追加。

**复现思路**

在 `writeContextLocked` 成功后、`writeConversationMetaLocked` 前强制退出，再重启并加载该 conversation。

**修复与测试**

优先使用单文件事务或代际 manifest/journal。最低限度应：

- 比较 conversation ID 和 context version
- 检测不一致后回滚或重建 metadata
- temp 文件 rename 前 `Sync`
- rename 后同步目录
- 测试每一个崩溃注入点

---

## F-15 高：本地 MITM 无客户端认证，可被借用为 Backend Relay

**证据**

- `internal/mitm/service.go:184-193` 的 username/password 参数被直接忽略。
- `internal/client/service.go:135` 和 `internal/app/runner.go:73` 均传入空认证信息。
- `internal/mitm/service.go:505-511` 对匹配的 Cursor 请求自动补进程级 relay proof。
- `internal/backend/server/middleware.go:61-87` backend 只校验这个 proof。
- `internal/mitm/service.go:732-761` 预检响应反射任意 Origin、方法和请求头，并允许 credentials。

**影响与触发条件**

任意本机进程可直接连接 loopback 代理，并借助代理自动获得 backend relay 能力。使用 Cursor 用户级代理的 WebView/扩展网页也可能触发写操作。可触达推理、会话、usage、规则及 KnowledgeBase/UserRule CRUD 等接口。

这不应泛化为所有系统浏览器：当前代理只写入 Cursor 用户级设置，并只监听 loopback。

**复现思路**

从第二个本机进程把请求显式发送到 `127.0.0.1:18080`，目标设为被 MITM 的 `*.cursor.sh` 路由，验证请求是否在无需代理认证的情况下到达 backend。

**修复与测试**

- 为代理入口增加不可伪造的进程间认证，不能只靠可复制的静态 Basic 密码。
- 严格限制 Origin，不反射任意来源。
- 对 backend 路由按能力最小化。
- 测试其他进程、Cursor WebView、错误 Origin 和无凭证客户端。

---

## F-22 高：Provider Redirect 可泄露 `x-api-key` 和自定义认证头

**证据**

- `internal/netproxy/netproxy.go:40-51` 的默认客户端自动跟随重定向。
- OpenAI/Anthropic 推理 adapter 使用默认客户端。
- `internal/backend/agent/model/anthropic.go:144-179` 同时发送 `Authorization` 与 `x-api-key`。
- `internal/client/model_fetch.go:84` 模型列表抓取同样使用自动重定向客户端。
- `ApplyCustomHeaders` 允许除少量 blocked header 以外的任意自定义头。

**影响与触发条件**

恶意或被接管的 provider endpoint 返回跨域 3xx 时，Go 会保护部分标准敏感头，但不会等价保护 `x-api-key` 或任意自定义 secret header。这些头可能发送到重定向目标。

**复现思路**

让配置的 provider 返回 302 到第二个测试域名，在第二个服务记录 `x-api-key` 和自定义头。

**修复与测试**

默认禁止 provider、模型列表和测速请求跟随重定向；或在每跳重新校验 scheme、host，并显式清除所有认证及用户自定义头。加入同域、子域、跨域和降级到 HTTP 的测试。

---

## F-24 高：WebFetch 域名校验可绕过并形成 SSRF

**证据**

- `internal/backend/agent/bridge/interaction/bridge.go:762-814` 只对字面 IP 调用 `net.ParseIP`；普通域名不解析 A/AAAA。
- `internal/backend/agent/bridge/interaction/bridge.go:915-931` redirect 仅重新执行同一字符串 host 检查。
- 没有把验证后的安全 IP 固定到实际 Dial。
- 用户批准发生在 URL 字符串层，而不是最终连接 IP 层。

**影响与触发条件**

攻击者控制的域名可解析到：

- loopback
- LAN 地址
- link-local
- 云元数据地址

也可通过 DNS rebinding 在批准后改变解析结果。响应内容随后返回模型。

**复现思路**

使用一个解析到 `127.0.0.1` 或 `169.254.169.254` 的测试域名执行 WebFetch；再测试首跳公网、redirect 到私网的情况。

**修复与测试**

解析全部 A/AAAA，逐个拒绝非公网地址，并通过自定义 `DialContext` 固定已验证 IP。每次 redirect 重新解析和校验，同时处理 IPv4-mapped IPv6、DNS rebinding 和多地址结果。

---

## F-29 高：旧目录迁移失败后仍删除唯一旧副本

**证据**

- `internal/appdata/migrate.go:31-36` 复制配置和规则后无条件 `os.RemoveAll(legacyRoot)`。
- `internal/appdata/migrate.go:38-59` Walk 和复制错误基本都被忽略。
- `internal/appdata/migrate.go:61-84` 目标文件以 `O_TRUNC` 覆盖，`io.Copy` 结果被忽略。
- 迁移发生在多个服务初始化前，可由正常启动触发。

**影响与触发条件**

磁盘满、权限失败、目标冲突、部分复制或进程崩溃时，可同时覆盖新目录内容并删除旧目录中的唯一 `config.yaml` 或规则文件。

**复现思路**

构造新旧目录同时存在，再注入目标不可写、磁盘满或中途复制失败，观察旧目录是否仍被删除。

**修复与测试**

- 不覆盖已存在目标。
- 每个文件使用临时文件、校验大小/摘要并 fsync。
- 全部复制成功后写迁移完成标记。
- 至少保留一份可恢复备份，不应直接删除源目录。

---

## F-30 高：`SelectedImage.path` 可读取任意本地文件并发送给 Provider

**证据**

- `proto/agent_v1.proto:3960-3982` 的 `SelectedImage.path` 是普通字符串。
- `internal/backend/agent/prompt/replay.go:143-165` 把 path 直接转成 image content part。
- `internal/backend/agent/model/content_parts.go:175-188` 在 data 为空时直接 `os.ReadFile(path)`。
- `internal/backend/forwarder/prompt_guard.go:108-119` 没有对 SelectedImages 的路径、数量或总大小实施限制。

**影响与触发条件**

调用方可提交应用权限范围内的任意绝对路径，文件内容会被 Base64 编码并发送到所配置的图像 provider。结合 F-15，其他本机进程可能通过代理触发。超大文件还会放大 F-28。

**复现思路**

提交一个指向 workspace 外文本文件或 symlink 的 `SelectedImage.path`，且不携带内联 data，观察 provider 请求中的 Base64 内容。

**修复与测试**

服务端禁止读取客户端提供的 path，只接受有硬上限的内联图像数据。若必须保留 path：

- 限制到可信 workspace
- 解析 realpath 和 symlink
- 校验 MIME、魔数和尺寸
- 设置单图、总量和数量上限

---

## F-31 高：Ask/Plan/Readonly Task 的只读能力只由 Prompt 约束

**证据**

- `internal/backend/forwarder/tool_catalog.go:51-160`：
  - Ask 仍包含 `Write`、`PatchEdit`、`Delete`、`Shell`、`Task` 等。
  - Plan 仍包含 `Shell`、`CallMcpTool`、`FetchMcpResource`、`Task` 等副作用入口。
- `internal/backend/forwarder/tool_catalog.go:184-205` 只要 `subagentTypeName` 非空，就统一使用 Agent 工具集。
- `internal/backend/forwarder/tool_catalog.go:232-241` child 工具资产也回到 Agent，而不是只读 subagent 工具集。
- `internal/backend/forwarder/state_tools.go:830-835` 只把 `readonly=true` 映射为 `TASK_MODE_PLAN`，没有形成后端 capability。

**影响与触发条件**

模型忽略或被 prompt injection 绕过只读提示时，backend 仍会生成并分发具备副作用的工具调用。readonly child conversation 实际可获得 Agent 能力。

**复现思路**

在 Ask/Plan 和 `Task(readonly=true)` 中显式请求 Write、Shell 或 MCP 副作用，观察工具目录以及 backend 是否拒绝执行。

**修复与测试**

按 mode 构造不可变 capability set，并在：

1. 工具目录生成
2. 工具调用分发
3. child conversation 创建

三个边界复用同一策略。readonly Task 应使用真正的 `ModeSubagent` 只读集合。

---

## F-32 高（条件性、中等置信度）：Workspace 写入围栏存在多条旁路

**证据**

- `internal/backend/forwarder/path_resolution.go:169-189` 只做词法 `Clean/Rel`，不解析 symlink。
- `internal/backend/forwarder/path_resolution.go:233-258` workspace/terminals 缺失时允许任意绝对路径。
- `Write`、`PatchEdit` 使用围栏；`internal/backend/forwarder/events.go:312-321` 的 Delete 原样下发。
- `internal/backend/agent/bridge/exec/bridge.go:1007-1039` 把 `FetchMcpResource.downloadPath` 原样下发，未拒绝绝对路径或 `..`。

**影响与触发条件**

可能通过：

- workspace 内 symlink
- 缺失 workspace 上下文
- Delete
- MCP resource download path

访问或修改 workspace 外部路径。

最终客户端可能还有二次确认或路径校验；静态 backend 代码无法证明所有路径都会成功，因此该项属于条件性高风险。

**复现思路**

分别测试 workspace symlink、绝对 Delete、`../` downloadPath，以及缺失 workspace context 的绝对 Write。

**修复与测试**

统一实现 realpath/symlink-aware 的路径 capability，workspace 缺失时 fail closed，并覆盖全部读写删除及下载入口。客户端确认不能替代 backend 强制。

---

## F-33 高（部署条件性）：`cursor-tab-server` 默认全网卡监听且无入站认证

**证据**

- `cursor-tab-server/main.go:25` 默认地址为 `:8041`。
- `cursor-tab-server/main.go:91-95` 直接启动监听。
- `cursor-tab-server/main.go:101-149` 不校验调用方身份，却自动附加配置中的 Cursor bearer token 和 checksum。
- `cursor-tab-server/config.example.yaml:1` 只有明文上游 token，没有入站 token。
- 固定路由包含补全、commit message 和文件同步接口。

**影响与触发条件**

当端口被 LAN、防火墙规则、容器端口映射或公网暴露时，未认证调用者可把服务作为 Cursor 账号能力中继，消耗额度或调用文件同步相关路由。

**复现思路**

从另一台主机请求监听端口上的固定路由，不携带任何认证，验证服务是否自动补官方凭据并转发。

**修复与测试**

默认绑定 `127.0.0.1:8041`，要求高熵入站 token、mTLS 或受认证反向代理；精简路由，并在部署文档中明确防火墙与容器端口要求。

---

# 4. 中严重度问题

## F-02 中：前端整包保存会删除后端字段

- **证据**：`frontend/src/state/appState.js:560-619` 的归一化和 payload 构建丢弃 `pricing`、`routing.tabServerBaseURL`、adapter `costMultiplier`；`appState.js:640-666` 随后保存完整 payload；`internal/backend/server/config/store.go:99-115` 完整替换配置。
- **影响**：任何普通设置保存都可能删除定价、Tab 上游地址和倍率。`saveTabServerBaseURL` 自身也经过同一丢字段路径。
- **复现**：先保存 Pricing 和 tab URL，再修改日志开关或模型配置并保存，比较前后 YAML。
- **修复**：改为字段级 patch，或后端锁内读取并 merge；前端 round-trip 测试必须保留未知字段。

## F-03 中：配置 Load–Modify–Save 缺少事务或 CAS

- **证据**：`Store.Load` 和 `Store.Save` 分别加锁，但完整读改写不在同一临界区；`manager.go:95-106`、`metrics.go:130-205` 都采用独立 Load/Save。
- **影响**：前端保存、Pricing CRUD 和 `lastAgentModelHash` 更新可能互相覆盖。
- **复现**：并发执行两个修改不同字段的保存，验证后写是否丢失先写字段。
- **修复**：提供 `Update(func(*Config) error)` 锁内事务，或 revision/ETag CAS；并增加并发回归测试。

## F-05 中：Legacy Usage 升级可能隐藏旧事件并重复累计

- **证据**：`usage_store.go:173-187` 只把当前新事件写入 `EventIndex`；`usage_store.go:193-213` 只有索引为空时才扫描 `RecentEvents`；现有 `buildUsageEventIndex` 未接入升级。
- **影响**：首次写入后旧事件从 Lookup 视角消失；upsert 同一旧事件时无法回滚旧 delta，可能重复计费。
- **复现**：构造只有 `recent_events`、没有 `event_index` 的旧文件，再 append 新事件并 upsert 旧 request ID。
- **修复**：加载旧文件时一次性构建完整索引并持久化，测试首次 append/upsert。

## F-06 中：客户端取消和 Sink 错误被计为 Provider 故障

- **证据**：`router.go:87-119` 对 `adapter.Stream` 的任意错误先调用 `RecordFailure`，之后才判断错误类型和是否 failover。
- **影响**：`context.Canceled`、客户端断连、sink 写失败和普通 4xx 会污染熔断统计，健康渠道可能被错误 Open。
- **复现**：连续取消流请求，观察 circuit breaker 是否进入 Open。
- **修复**：只记录结构化网络错误、429、5xx 和 provider idle timeout；取消和下游写失败不计 provider failure。

## F-07 中：401/403/404 一律禁止跨 Provider Failover

- **证据**：`router.go:267-307` 将除 429 外的全部 4xx 视为不可重试，并主要从错误字符串提取 `status=NNN`。
- **影响**：当前 provider 的 key、权限或模型映射失效时，备用 provider 即使可用也不会尝试。
- **复现**：主渠道返回 401、403、404，备用渠道正常，验证没有切换。
- **修复**：引入结构化 HTTP 错误，并把认证、权限、模型不存在按“是否 provider 特有”配置 failover 策略。

## F-08 中：Disabled Adapter 仍出现在 UI 且可成为默认

- **证据**：`mocks.go:432-470`、`mocks.go:636-678` 和 `collectModelAdapterRefs` 不过滤 disabled；运行时 resolver 在 `config/resolver.go:66-77` 才过滤。
- **影响**：UI 可选择一个运行时拒绝的模型；第一项 disabled adapter 仍可能成为默认。
- **复现**：把第一个 adapter 设为 disabled，调用 AvailableModels/GetDefaultModel，再实际推理。
- **修复**：模型目录和默认选择复用 resolver 的 enabled/Priority 逻辑。

## F-09 中：官方上游 NoRedirect 策略被 Host 注入客户端绕过

- **证据**：`upstream/client.go:84-109` 只有未注入 client 时才创建 NoRedirect client；`backend/host.go:277` 注入自动重定向的客户端。
- **影响**：官方 3xx 不再原样返回；`x-cursor-checksum` 等非标准敏感头可能跨域继续发送。不能据此直接声称完整 Cursor 登录凭据会跨域泄露。
- **复现**：给 Host 注入客户端并让官方测试端点返回跨域 302，记录第二跳请求头。
- **修复**：CredentialOriginalCursor 路由无条件使用 NoRedirect，或克隆 client 并覆盖 `CheckRedirect`。

## F-10 中：上游 HTTP Timeout 单位错误

- **证据**：`internal/backend/host.go:277` 使用 `netproxy.NewHTTPClient(30000 * time.Second)`。
- **影响**：实际超时约 8 小时 20 分，而不是通常意图的 30 秒，故障请求会长期占用资源。
- **修复**：改为 `30 * time.Second`，或显式使用 `time.Duration(milliseconds) * time.Millisecond`；加入超时测试。

## F-11 中：发布流程存在公开未签名 Manifest 窗口

- **证据**：`.github/workflows/release.yml:128-155` 明确先发布未签名 `update.json`，再要求维护者手工签名重传。
- **影响**：内置公钥客户端会在窗口期拒绝更新。这是发布原子性和可用性问题，不是签名绕过。
- **复现**：完成 workflow 但不执行人工签名步骤，客户端检查更新。
- **修复**：CI 在发布前使用受保护签名服务生成签名，所有资产和 manifest 一次性发布。

## F-12 中：CA 初始化不验证既有 Pair，也不修复权限和并发

- **证据**：`certs/ca.go:175-226` 的加载只解析证书和私钥类型，不验证匹配、CA 属性或有效期；`ca.go:242-253` 两文件可读就直接返回；写入使用 `WriteFile`，不会收紧既有权限。
- **影响**：不匹配、非 CA、过期或宽权限 pair 不会安全重建；多进程并发可能互相覆盖。
- **修复**：验证公私钥匹配、`IsCA`、KeyUsage、NotAfter；使用文件锁、唯一临时文件、原子 rename，并对私钥主动 chmod 0600。

## F-14 中：停止代理不保证关闭 Hijacked CONNECT

- **证据**：`mitm/service.go:334-352` 在 Shutdown 前先把 `httpServer` 清空；`client/lifecycle.go:130-158` 代理停止失败后直接返回；没有活动 hijacked connection 集合。
- **影响**：`IsRunning()` 可能提前变为 false，而旧 CONNECT/流式连接继续转发；停止失败还会阻断后续清理 Cursor 设置和 backend。
- **修复**：通过 ConnState/连接注册表跟踪并强制关闭 hijacked 连接，状态只在真正停止后变更。

## F-16 中：Pricing 更新丢失 `InputTokenSemantics`

- **证据**：`internal/bridge/metrics.go:130-167` 重建 `ModelPricing` 时没有复制原记录的 `InputTokenSemantics`。
- **影响**：编辑 TOTAL/FRESH 模型后退化到 legacy 成本口径，OpenAI billable input token 和成本会错误。
- **修复**：更新时保留原语义；新增时显式校验。增加 TOTAL/FRESH 编辑回归测试。

## F-18 中：敏感文件权限过宽

- **证据**：
  - `config/store.go:119-133` 以 0644 保存含 API key、自定义认证头的配置。
  - `debug_recorder.go:159-163` JSONL 为 0644。
  - `logger/logger.go:183-207` `app.log` 为 0644。
  - 多个数据目录使用 0755。
- **影响**：在 macOS/Linux 多用户环境，其他本地用户可能读取 API key、用户消息、上下文、工具结果和请求内容。
- **修复**：敏感目录 0700、文件 0600，并在启动时迁移修复既有权限。`docs_index_store`、`codebase_index_store` 已主动 chmod 0600，不在此项内。

## F-19 中：Extra Params 可覆盖协议关键字段

- **证据**：`request_override.go:52-80` 无白名单地执行 `body[name] = value`；OpenAI/Anthropic 在标准 payload 构建后应用它。
- **影响**：可覆盖 `stream`、`model`、`messages/input`、`tools` 等。与 F-20 组合时，`stream=false` 的普通 JSON 200 可能被当作空流成功。
- **修复**：仅允许明确的扩展字段白名单，禁止修改协议和身份字段。

## F-20 中：三种 Provider 流都不要求成功终止事件

- **证据**：
  - Chat Completions：`openai.go:634-790`
  - Responses：`openai.go:1343-1530`
  - Anthropic：`anthropic.go:512-695`
- **影响**：没有 `[DONE]`、`response.completed` 或 `message_stop` 时，正常 EOF 仍可能返回 nil。也未强制 `Content-Type: text/event-stream` 或至少一个有效事件。
- **复现**：返回普通 JSON 200、零事件、缺终止标记或语义截断但 TCP 正常关闭。
- **修复**：记录流状态机，只有收到合法终止事件才成功；否则返回可 failover 的截断错误。

## F-21 中：Provider 流只有单事件限制，缺少整流预算

- **证据**：OpenAI 只有 scanner token 上限；Anthropic 单事件约 1 MiB；`stream_idle.go:12-60` 每次有效 delta 都重置默认约 4 分钟 watchdog；`model_adapter_benchmark.go:75-102` 在内存保存完整原始响应。
- **影响**：恶意 provider 可通过长期慢发、海量事件、超大工具参数或图片持续消耗内存、磁盘和连接。
- **修复**：增加总字节数、事件数、工具参数、图片、总持续时间和 debug 配额。F-36 的 session map 泄漏单独处理。

## F-23 中：Custom Endpoint 使用字符串拼接，Query/Fragment 会破坏 URL

- **证据**：
  - `openai_endpoint.go:9-39`
  - `anthropic.go:148-156`
  - `modelchannel/identity.go:19-39`
- **影响**：含 `?api-version=` 的 BaseURL 会把后续路径拼进查询值；fragment 后追加的路径不会发送。`/custom` 文案称完整 URL，但仍可能追加 `/chat/completions`。
- **修复**：用 `net/url` 分离 Path、RawQuery 和 Fragment，并明确区分 base URL 与完整 endpoint。

## F-25 中：Updater 在验签/摘要失败前可无界解析和下载

- **证据**：`updater/manager.go:186-213` 对 manifest 使用无大小限制的 `json.Decoder`；`manager.go:239-289` 使用无界 `io.Copy`，asset size 主要用于进度。
- **影响**：恶意更新服务可耗尽内存或临时盘。最终摘要失败会阻止安装错误资产，因此不是代码执行。
- **修复**：限制 manifest 大小；检查 Content-Length；按 `min(asset.Size+1, globalMax)` 限制下载并校验实际字节数。

## F-26 中：发布触发版本与构建配置版本未对齐

- **证据**：
  - workflow 的 release tag/name 来自 tag/input。
  - `Taskfile.yml:12-17` 和 `scripts/release/main.go:142-167` 从 `build/config.yml` 读取资产版本。
  - `verify-versions` 只比较仓库内部三处版本。
  - `.github/workflows/release.yml:145` 用 `|| echo` 吞掉 manifest 失败。
- **影响**：可能产生 release `vX`、二进制/manifest `vY` 的错配，甚至缺失 manifest 时继续发布。
- **修复**：在构建前强制比较触发 tag/input 与 config version；manifest 失败必须终止 workflow。

## F-27 中：Updater 在验签前信任 Version 字段

- **证据**：`updater/manager.go:203-216` 先比较版本，再验证 manifest 签名。
- **影响**：伪造同版本或低版本 manifest 可被当作“已是最新”，从而冻结更新或抑制提示。下载安装仍受签名与摘要保护。
- **修复**：解析受限 manifest 后立即验签，之后才能信任任何字段。

## F-28 中：本地代理和 Backend 缺少完整资源预算

**主要证据**

- Connect handler 未配置 `connect.WithReadMaxBytes`。
- `backend/host.go:146-150` 只有 `ReadHeaderTimeout`。
- `mitm/service.go:297-304` 未设置 Read/Write/Idle timeout、连接数等。
- `upstream/action.go:72` 使用无界 `io.ReadAll`。
- `broker.go:302-343` backlog 无硬上限。
- `mitm/service.go:695-722` 证书缓存无容量/TTL。
- `logger.go:183-207` 按行数轮转且整文件读入内存，超长单行可绕过字节预算。

**边界**

Go 默认仍可能提供 header 上限；actor mailbox 有 128 容量；终态 stream 通常会清理。因此不能笼统描述为“所有队列完全无界”。

**修复**

对入站 body、连接、并发流、backlog、SelectedImages、证书缓存、debug 和日志设置分层预算，并添加慢连接与超量测试。

## F-35 中：Proxy 生命周期和配置操作未串行化

- **证据**：`client/service.go:46-47` 声明了 `configMu`，但调用链未使用；`StartProxy`、`StopProxy` 和 `SaveUserConfig` 可并发；`backend/host.go:80-93` 未持 `runMu` 读取 `httpServer`。
- **影响**：可形成 Go data race，以及 backend、MITM、Cursor 设置只完成部分阶段的状态。
- **修复**：用统一 lifecycle mutex/state machine 串行化 Start/Stop/Save；`Host.SaveConfig` 在锁内读取运行状态；阶段失败执行明确回滚。

## F-36 中：`artifactRecorder.sessions` 永久增长并保留请求体

- **证据**：`forwarder/artifacts.go:20-24` 定义无界 map；`artifacts.go:51-100` 保存深拷贝请求/摘要；`artifacts.go:131-155` 的 `ClearActiveArtifacts` 是唯一删除路径，但未发现生产调用。
- **影响**：每次 provider request 都可能永久保留消息、上下文、工具参数和图片，直到 backend 对象释放。
- **修复**：在统一 provider finalize/defer 中清理。若需诊断，使用有容量和 TTL 的脱敏摘要。测试成功、失败、取消和并发请求。

---

# 5. 低严重度问题

## F-17 低：内置 Pricing 删除无法持久化

- **证据**：`metrics.go:168-189` 删除记录后保存；`config/pricing.go:191-214` 的 normalize 会立即从 seed 补回缺失内置模型。
- **影响**：UI 所谓“删除后成本按 0 计算”与实际行为不符。
- **修复**：增加 tombstone/disabled 字段，或把 UI 改为“恢复默认价格”。

## F-34 低：倍率和定价字段允许 NaN、Inf、零及负值

- **证据**：`metrics.go:191-228` 直接使用 `strconv.ParseFloat`；`metrics.go:394-400` 返回 NaN/Inf；`PricingPanel.vue:141` 允许零；计算层又把 `<=0` 回退为 1。
- **影响**：保存语义与显示不一致，非有限值还可能导致 JSON/Wails 序列化失败。
- **修复**：统一要求 `math.IsNaN == false`、`math.IsInf == false` 且数值大于零；前后端复用同一规则。

## F-37 低：生产前端控制台记录完整配置和 API 参数

- **证据**：
  - `frontend/src/services/clientApi.js:19-48` 统一记录 payload 和 result。
  - `clientApi.js:50-183` 包括 `LoadUserConfig`、`SaveUserConfig`、`adapterJSON`、测速和模型列表参数。
  - `frontend/vite.config.js:16-20` 未配置生产环境移除 console。
- **影响**：WebView 开发者工具中可能显示 `apiKey`、`customHeadersJSON` 和完整 adapter 配置。
- **边界**：未发现这些 console 自动进入原生日志或远程外传，主要是本机调试面风险。
- **修复**：生产构建移除 API 日志；或集中脱敏 `apiKey`、Authorization、Cookie、自定义头和原始响应。

---

# 6. 去重与组合关系

这些问题共享调用链，但不应合并：

- **F-15 → 放大 F-04/F-28/F-30**：未认证本地代理提供触发入口；后三项仍需独立修复。
- **F-02/F-03/F-16/F-17/F-34**：分别是字段丢失、并发覆盖、语义丢失、删除语义和数值校验。
- **F-09 与 F-22**：前者是 Cursor 官方回源链；后者是用户配置的 provider、模型列表和测速链。
- **F-13/F-14/F-35**：分别属于持久化事务、连接关闭和生命周期并发。
- **F-21/F-28/F-36**：分别是单条 provider 流、系统级资源预算和 artifact session 生命周期。
- **F-11/F-25/F-26/F-27**：分别是发布原子性、下载资源限制、版本来源一致性和验签顺序。
- **F-31 与 F-32**：一个是能力授权，一个是路径作用域；只修其中一个不足以形成安全写入边界。

---

# 7. 建议修复顺序

## P0：先封闭信任边界

1. F-15 本地 MITM 客户端认证与 Origin 限制
2. F-33 Tab Server 默认 loopback 与入站认证
3. F-30 禁止服务端读取 `SelectedImage.path`
4. F-24 WebFetch DNS/IP 固定校验
5. F-04 Conversation ID 最终路径校验
6. F-31/F-32 后端 capability 和统一 realpath 工作区围栏

## P1：恢复核心正确性和数据完整性

1. F-01 重构“逻辑模型 ID—渠道 ID—候选链”关系
2. F-02/F-03 配置 patch API 与事务更新
3. F-13 会话单事务或代际持久化
4. F-20 成功终止状态机
5. F-06/F-07 结构化 provider 错误与 failover 策略
6. F-21/F-28/F-36 统一资源预算和回收

## P2：发布、生命周期和长期维护

1. F-11/F-25/F-26/F-27 更新发布链
2. F-12/F-18 CA 与文件权限迁移
3. F-14/F-35 生命周期状态机
4. F-16/F-17/F-34 Pricing 语义
5. F-37 生产日志脱敏

---

## 8. 关键测试缺口

- Failover：真实渠道 ID；AvailableModels → RequestedModel → Router；disabled/default/Priority。
- Usage：legacy 文件首次 append/upsert。
- Conversation path：`"."`、`".."`、分隔符、绝对路径和 debug artifact。
- Upstream：Host 注入自动重定向 client。
- Provider：普通 JSON 200、错误 Content-Type、零事件、缺终止标志、截断 EOF、extra params 覆盖关键字段、跨域 redirect 泄露、query/fragment endpoint、整流资源预算。
- Artifact recorder：完成、失败、取消、并发后的 map 回收和容量。
- CA：损坏、不匹配、非 CA、过期、宽权限和并发初始化。
- MITM：停止关闭 hijacked CONNECT、客户端认证、Origin allowlist、跨进程防护。
- Lifecycle：并发 Start/Stop/Save、`Host.SaveConfig()` 与 Serve/Shutdown race、阶段失败回滚。
- 资源：Connect/HTTP body、慢连接、透传 body、BidiAppend 多副本、stream/backlog、随机子域证书缓存、日志/debug 配额。
- WebFetch：域名解析私网、DNS rebinding、redirect 私网。
- Updater/release：manifest/资产超量、验签前版本比较、未签名窗口、触发版本错配。
- Migration：新旧并存、冲突、部分复制失败、磁盘满/权限错误、重试恢复。
- Image：绝对路径、symlink、超大文件、内联总量、非 workspace。
- Capability：Ask/Plan 工具集、readonly Task child、目录生成与运行时拒绝一致性。
- Workspace fence：symlink、缺 workspace、Delete、FetchMcpResource `downloadPath`、客户端二次确认。
- `cursor-tab-server`：默认监听、未认证 LAN 调用、额度/同步路由滥用。
- Pricing：NaN/Inf/零/负倍率、JSON/Wails 返回。
- 前端无现成 JS/TS 单元测试。

---

## 9. 已确认的正面边界

- Router 在已经向客户端发送事件后不会切换备用 provider，不会拼接两家输出。
- Provider response body 均有关闭；非 2xx 错误摘要有大小限制。
- Updater 的最终 manifest 签名和资产 SHA-256 校验边界总体正确。
- 更新安装路径作为独立 argv 传递，未发现 shell 命令注入。
- MITM 和 backend 默认监听字面 loopback，不是 LAN 开放代理。
- 默认转发会清理 hop-by-hop 和若干敏感头。
- 未发现 `InsecureSkipVerify`、明显 SQL 注入或新的字符串命令拼接链。
- 未发现桌面 UI 使用 `innerHTML`、远程脚本或明显 HTML 注入链。