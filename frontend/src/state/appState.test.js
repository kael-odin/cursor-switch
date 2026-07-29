// 审计 L3：normalizer 回归测试。appState.js 的 normalizer 是 config 层契约边界——
// F-02（payload merge 透传）、L5（旧品牌 key 迁移）、v2.0.3（per-namespace 路由）、
// A6（cursor 配置）等改动都经手这些函数。锁住它们防回归。
//
// 环境约定：appState.js 顶层 import wails runtime 与 clientApi（wails 绑定，Node 下不可用），
// 且模块加载时执行 migrateLegacyStorageKeys() + loadCachedState()——故 mock 掉这两个模块，
// happy-dom 提供 localStorage。生产代码不动，仅测试。
import { describe, expect, it, vi } from "vitest";

vi.mock("@wailsio/runtime", () => ({ Events: { On: () => () => {} } }));
vi.mock("@/services/clientApi", () => ({
  checkForUpdates: vi.fn(),
  getAppVersion: vi.fn(),
  getHomeMetricsSummary: vi.fn(),
  installReadyUpdate: vi.fn(),
  getModelAdapterTestResults: vi.fn(),
  getProxyState: vi.fn(),
  openConfigWindow: vi.fn(),
  loadUserConfig: vi.fn(),
  openLogsDirectory: vi.fn(),
  openModelConfig: vi.fn(),
  openModelEditor: vi.fn(),
  saveUserConfig: vi.fn(),
  startProxyService: vi.fn(),
  stopProxyService: vi.fn(),
  testModelAdapter: vi.fn(),
}));

const {
  normalizeModelAdapter,
  normalizeModelAdapters,
  normalizeConfig,
  ANTHROPIC_THINKING_EFFORT_DEFAULT,
  OPENAI_ENDPOINT_RESPONSES,
} = await import("@/state/appState");

describe("normalizeModelAdapter", () => {
  it("归一 baseURL（协议+host 小写、去尾斜杠）+ 接受 camelCase/别名字段 + snake_case 数值字段", () => {
    // 字段别名契约（normalizeModelAdapter 实际接受）：baseURL||url、apiKey||key、modelID（仅）、
    // context_window_tokens/max_completion_tokens 等数值字段接受 snake_case（后端 Go 序列化为 snake_case）。
    const got = normalizeModelAdapter({
      type: "openai",
      url: "HTTPS://API.Example.com/v1/", // url 别名 → baseURL，归一大小写+去尾斜杠
      key: "sk-1", // key 别名 → apiKey
      modelID: "gpt-5",
      reasoningEffort: "HIGH", // 大写 → 小写
      openAIEndpoint: "/v1/chat/completions",
      context_window_tokens: "200000", // snake_case + 字符串 → 数
      max_completion_tokens: "8192", // snake_case + 字符串 → 数
    });
    expect(got.type).toBe("openai");
    expect(got.baseURL).toBe("https://api.example.com/v1");
    expect(got.apiKey).toBe("sk-1");
    expect(got.modelID).toBe("gpt-5");
    expect(got.reasoningEffort).toBe("high");
    expect(got.openAIEndpoint).toBe("/v1/chat/completions");
    expect(got.contextWindowTokens).toBe(200000);
    expect(got.maxCompletionTokens).toBe(8192);
  });

  it("baseURL 接受 baseURL 与 url 别名，两者同存 baseURL 优先", () => {
    expect(normalizeModelAdapter({ type: "openai", baseURL: "https://a.com/" }).baseURL).toBe("https://a.com");
    expect(normalizeModelAdapter({ type: "openai", url: "https://b.com/" }).baseURL).toBe("https://b.com");
    expect(
      normalizeModelAdapter({ type: "openai", baseURL: "https://preferred.com/", url: "https://ignored.com/" }).baseURL,
    ).toBe("https://preferred.com");
  });

  it("未知 type 置空，非法 reasoningEffort 回落 medium", () => {
    const got = normalizeModelAdapter({ type: "gemini", reasoning_effort: "ultra" });
    expect(got.type).toBe("");
    expect(got.reasoningEffort).toBe("medium");
  });

  it("非对象输入归一到空 adapter，enabled 默认 true", () => {
    const got = normalizeModelAdapter(null);
    expect(got.type).toBe("");
    expect(got.enabled).toBe(true);
    expect(got.contextWindowTokens).toBe(0);
  });

  it("openai 类型才解析 openai 专属字段，anthropic 留空", () => {
    const openai = normalizeModelAdapter({ type: "openai", open_ai_endpoint: "" });
    expect(openai.openAIEndpoint).toBe(OPENAI_ENDPOINT_RESPONSES); // 空端点默认 responses
    expect(openai.openAIExtraParamsEnabled).toBe(false);

    const anthropic = normalizeModelAdapter({ type: "anthropic", openAIEndpoint: "/v1/responses" });
    expect(anthropic.openAIEndpoint).toBe(""); // 非 openai 强制空
    expect(anthropic.anthropicThinkingEffort).toBe(ANTHROPIC_THINKING_EFFORT_DEFAULT); // 默认 xhigh
  });

  it("costMultiplier carry-through（F-02）：不解释仅透传，保 per-adapter 倍率", () => {
    const got = normalizeModelAdapter({ type: "openai", costMultiplier: "1.2" });
    expect(got.costMultiplier).toBe("1.2");
    const empty = normalizeModelAdapter({ type: "openai" });
    expect(empty.costMultiplier).toBe("");
  });

  it("baseURL 非 http(s) 协议返回空串", () => {
    expect(normalizeModelAdapter({ type: "openai", baseURL: "ftp://x" }).baseURL).toBe("");
    expect(normalizeModelAdapter({ type: "openai", baseURL: "not a url" }).baseURL).toBe("not a url");
  });
});

describe("normalizeModelAdapters", () => {
  it("非数组归一为空数组，逐项归一", () => {
    expect(normalizeModelAdapters(null)).toEqual([]);
    expect(normalizeModelAdapters("x")).toEqual([]);
    const got = normalizeModelAdapters([{ type: "openai" }, { type: "anthropic" }]);
    expect(got).toHaveLength(2);
    expect(got[0].type).toBe("openai");
    expect(got[1].type).toBe("anthropic");
  });
});

describe("normalizeConfig", () => {
  it("空输入回落全默认，不抛异常", () => {
    const got = normalizeConfig(null);
    expect(got.modelAdapters).toEqual([]);
    expect(got.routing.mode).toBe("local");
    expect(got.routing.perNamespace).toEqual({});
    expect(got.homeMetrics.includeCacheWriteInHitRate).toBe(false);
  });

  it("routing.perNamespace 清洗非法值丢 auto，全空保持空对象", () => {
    const got = normalizeConfig({
      routing: {
        mode: "upstream",
        perNamespace: {
          cpp_service: "upstream",
          file_sync: "local",
          bad_route: "auto", // auto = 跟随全局，丢弃
          empty_route: "",
          network_service: "invalid", // 非法值丢弃
        },
      },
    });
    expect(got.routing.mode).toBe("upstream");
    expect(got.routing.perNamespace).toEqual({
      cpp_service: "upstream",
      file_sync: "local",
    });
  });

  it("routing.mode 非法值回落 local", () => {
    expect(normalizeConfig({ routing: { mode: "direct" } }).routing.mode).toBe("local");
    expect(normalizeConfig({ routing: { mode: "LOCAL" } }).routing.mode).toBe("local"); // 大小写归一
  });

  it("round-trip：normalize 后再 normalize 不产生二次变化（幂等）", () => {
    const input = {
      modelAdapters: [{ type: "openai", baseURL: "https://a.com/", context_window_tokens: 1000 }],
      routing: { mode: "upstream", perNamespace: { cpp_service: "local" } },
    };
    const once = normalizeConfig(input);
    const twice = normalizeConfig(once);
    // modelAdapters 的 baseURL 已归一（去尾斜杠），二次归一应稳定。
    expect(twice.modelAdapters[0].baseURL).toBe("https://a.com");
    expect(twice.routing).toEqual(once.routing);
  });
});
