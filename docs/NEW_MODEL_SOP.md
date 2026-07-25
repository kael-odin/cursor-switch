# 新模型上线更新 SOP

每当有新 Claude / GPT / Gemini / Grok 等模型发布，按本流程更新两张纯数据表 + 跑测试 + 写 release note。全程约 1 分钟，无需改逻辑代码。

## 1. 加定价记录

编辑 `internal/backend/server/config/pricing.go` 的 `pricingModelSeed` 切片，加一条：

```go
// Anthropic Claude（legacy 口径：input 含 cache_read，减 cache_read；不标 InputTokenSemantics）
{ModelID: "claude-opus-4-9", DisplayName: "Claude Opus 4.9", InputPerMillion: "5", OutputPerMillion: "25", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25"},

// OpenAI GPT（TOTAL 口径：input 已含 cache_read+cache_creation，须标 InputSemanticsTotal，否则缓存部分会被重复计费）
{ModelID: "gpt-6", DisplayName: "GPT-6", InputPerMillion: "5", OutputPerMillion: "30", CacheReadPerMillion: "0.50", CacheWritePerMillion: "6.25", InputTokenSemantics: InputSemanticsTotal},
```

**字段说明**：

| 字段 | 含义 | 单位 |
|---|---|---|
| `ModelID` | 归一化后的裸 model id（小写、无命名空间前缀、无日期后缀） | — |
| `DisplayName` | 展示名 | — |
| `InputPerMillion` / `OutputPerMillion` | 每百万 token 单价 | 美元，字符串 |
| `CacheReadPerMillion` | 缓存读单价（Anthropic 约为 input 的 10%） | 美元，字符串 |
| `CacheWritePerMillion` | 缓存写单价（Anthropic 约为 input 的 1.25 倍） | 美元，字符串 |
| `InputTokenSemantics` | **成本回算口径**，空 = legacy | `FRESH` / `TOTAL` / 空(legacy) |

**InputTokenSemantics 怎么选**：

- `legacy`（空，默认）：input 计费时扣除 cache_read。**Anthropic / Google / 其余**厂商用这个——它们的 `input_tokens` 字段含 cache_read，账单上 cache_read 单独计费。
- `TOTAL`：input 计费时扣除 cache_read **和** cache_creation。**OpenAI** 系列用这个——它的 `input_tokens` 已含缓存部分，不减会重复计费。
- `FRESH`：input 原样计费，不减任何缓存。极少用（仅当 provider 的 input 已是纯 fresh input 时）。

不确定就留空（legacy），最坏只是 OpenAI 类厂商的缓存部分被重复计一点点，不会错得离谱。

## 2. 加上下文窗口

编辑 `internal/client/model_context_window.go` 的 `contextWindowByModelID` map，加一条：

```go
"claude-opus-4-9": 1_000_000,
```

只收录窗口 ≠ 200K 默认值的常见模型（避免表过大）。未命中的模型由 resolver 的 200K 默认值兜底，并由 models.dev 在线回退补全（见下）。

**数据源**：[models.dev](https://models.dev/models.json) 的 `limit.context` 字段，或厂商官方文档。

## 3. 跑测试确认候选匹配命中

```bash
go test ./internal/backend/server/config/ -run TestPricingCandidateMatch -v
```

若新模型 id 带日期/版本/命名空间变体（如 `openai.gpt-6-20260101`），可在 `pricing_test.go` 的 `TestPricingCandidateMatch` cases 里加一条验证候选匹配能命中基线：

```go
{"openai.gpt-6-20260101", "gpt-6"},  // 去命名空间+日期后缀，命中基线 gpt-6
```

候选匹配算法（去命名空间 / `-vN` 版本 / 日期后缀 / 推理努力后缀 / 前缀回退，工作集串联）见 `pricing.go` 的 `modelPricingCandidates`。

## 4. release-notes 提一句

在 `release-notes.md` 顶部对应版本段加一行：

```
- 支持 Claude Opus 4.9 / GPT-6 / Gemini 3.x 定价与上下文窗口
```

## 5.（可选）更新内置 models.dev 缓存表

内置静态表缓存于 2026-07，会随时间过期。但 **fetch-models 已支持 models.dev 在线回退**（`internal/client/modelsdev.go`）：静态表 miss 时自动拉取 `https://models.dev/models.json`（内存 + 落盘 7 天 TTL 缓存），命中则回填。所以静态表过期不影响新模型回填——在线回退会兜底。

静态表的意义是离线场景 + 减少首次拉取延迟。若想刷新静态表，可手动从 models.dev 拉取最新数据后更新 `contextWindowByModelID`。

---

## 快速检查清单

- [ ] `pricing.go:pricingModelSeed` 加定价（OpenAI 系列标 `InputSemanticsTotal`）
- [ ] `model_context_window.go:contextWindowByModelID` 加窗口
- [ ] `go test ./internal/backend/server/config/ -run TestPricingCandidateMatch` 绿
- [ ] `release-notes.md` 提一句支持新模型
- [ ] `go vet ./... && go test ./...` 全绿后提交
