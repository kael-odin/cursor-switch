# 开发指南(贡献者)

> 面向想在本地构建、调试、贡献代码的人。终端用户安装见 README。发版与签名见 [RELEASE_SIGNING.md](./RELEASE_SIGNING.md)。

---

## 0. 依赖

| 工具 | 版本 | 用途 |
|---|---|---|
| Go | ≥1.25(见 `go.mod`) | 后端 |
| Node.js | 24 | 前端 |
| yarn | 任意稳定版 | 前端依赖 |
| wails3 CLI | `v3.0.0-alpha.74` | 桌面壳 + bindings 生成 |
| protoc + protoc-gen-go | 见 `release.yml` | proto 代码生成 |

装 wails3:
```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha.74
```

---

## 1. 日常开发循环

### 后端(Go)

```bash
go build ./...          # 编译
go vet ./...            # 静态检查
go test ./internal/... ./scripts/...   # 跑测试
```

CI(`check.yml`)在 PR/push 时跑这三条,本地先过再提交。

### 前端(Vue3 + Vite)

```bash
cd frontend
yarn install            # 装依赖
yarn dev                # 开发服务器(热更新)
yarn build              # 生产构建
```

> **bindings 依赖**:`frontend/bindings/` 是 wails3 自动生成的、被 gitignore 的桥接代码。本地首次开发前必须先生成(见 §2),否则前端 `import "@bindings/..."` 会报错。

### 桌面应用(完整构建)

```bash
# 生成 proto(首次或 proto 变更后)
wails3 task common:generate:proto

# 生成前端 bindings(首次或后端 Wails service 变更后)
wails3 task common:generate:bindings

# 构建 Windows amd64
wails3 task build:windows:amd64
# 产物:bin/windows-64.zip
```

---

## 2. 何时要重新生成代码

### proto(`gen/agentv1` `gen/aiserverv1`)

**当**:`proto/*.proto` 变了,或你想同步 Cursor 最新扩展协议。

```bash
wails3 task common:sync:proto   # 从 Cursor 扩展快照提取并重新生成
# 或只重新生成已有 proto:
wails3 task common:generate:proto
```

`gen/` 是**提交进仓库**的(gitignored 的是 bindings,不是 gen)。proto 变更后 `gen/` 要一起提交。

### 前端 bindings(`frontend/bindings/`)

**当**:你新增/修改/删除了后端的 Wails service 方法(如 `bridge.ProxyService`、`bridge.MetricsService` 上的方法),或改了 service 暴露的 struct 字段。

```bash
wails3 task common:generate:bindings
```

bindings 是**本地生成、不入库**(gitignore)。前端通过 `@bindings/cursor/internal/bridge/*.js` 调用。

> **重要**:bindings 文件里每个方法有一个由 wails3 从方法名算出的 ID(如 `Call.ByID(1942167145)`)。手改 bindings 文件不可靠——ID 算错会导致运行时调用失败。**永远走 `generate:bindings` 重生成**。若需在未装 wails3 的环境调用新方法,用 `Call.ByName("cursor/internal/bridge.MetricsService.GetTokenPricing")`(按名解析,无需 ID),见 `frontend/src/services/clientApi.js` 的 `getTokenPricing`。

---

## 3. 测试

| 包 | 测什么 |
|---|---|
| `internal/updater` | 版本比较、manifest 签名 roundtrip |
| `internal/certs` | 每机器独立 CA 生成 |
| `internal/cursor` | settings.json JSONC 解析、代理 URL 归一化、auth state |
| `internal/backend/forwarder` | history entry 构造器、写路径围栏 |
| `internal/backend/agent/model` | SSE think-tag parser、thinking disable、请求覆盖 |

跑全部:
```bash
go test ./internal/... ./scripts/...
```

新增纯逻辑(解析、归一化、状态转换)请补表驱动测试,参照 `internal/cursor/cursor_pure_test.go` 或 `internal/backend/agent/model/sse_parser_test.go`。涉及 `*Service` 的集成逻辑暂无测试网(god 文件未拆完),改动需人工跑应用验证。

---

## 4. 版本号

**单一事实源**:`build/config.yml` 的 `info.version`。

三处版本(`config.yml` / `windows/info.json` / `windows/wails.exe.manifest`)必须一致,CI `check.yml` 的 `versions` job 跑 `scripts/release verify-versions` 校验,不一致即红。改版本时**只需改 `config.yml`**,再同步另两处(尚未自动化,见 §6 留债)。

---

## 5. 发版流程

见 [RELEASE_SIGNING.md](./RELEASE_SIGNING.md)。要点:

- 发版后 `update.json` 是**未签名**的(CI 不持有私钥),**发版后必须本地 `scripts/release sign` 再重传**,否则新版客户端拒绝更新。
- 私钥 `~/.cursor-byok-release.key`(0600,不入库)。丢了就得重新生成公钥并发强制更新版。

---

## 6. 已知留债(欢迎接手)

- **god 文件**:`service.go`(2038)、`openai.go`(1708)、`compaction.go`(1730)、`appState.js`(1387)仍偏大。已做文件组织级拆分(见 git log `refactor:`),真正的 god 对象解耦(多结构+接口)需补更多 *Service 方法测试夹具。
- **conversation_action 非 cancel 路径测试**:`decodeInboundIntent` 已覆盖 run/exec/heartbeat 等路径,但 conversation_action 的 resume/plan 等路径需 `*StreamBroker` 夹具,留待后续。
- **版本三处合一已自动化**:改 `build/config.yml` 后跑 `go run ./scripts/release sync-versions` 即同步 `info.json` + `wails.exe.manifest`,`verify-versions` 作 CI 兜底。
