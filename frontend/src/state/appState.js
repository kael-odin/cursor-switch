import { computed, reactive, watchSyncEffect } from "vue";
import { Events } from "@wailsio/runtime";
import dayjs from "dayjs";
import {
  checkForUpdates,
  getAppVersion,
  getHomeMetricsSummary,
  getModelAdapterTestResults,
  installReadyUpdate,
  getProxyState,
  openConfigWindow as openConfig,
  loadUserConfig,
  openLogsDirectory,
  openModelConfig,
  openModelEditor,
  saveUserConfig,
  startProxyService,
  stopProxyService,
  testModelAdapter,
} from "@/services/clientApi";
import { migrateLegacyStorageKeys } from "@/state/migrateStorage";

// 把旧品牌 key（cursor-client:）一次性迁到 cursor-switch: 前缀，再读新 key。
// 必须在 loadCachedState 之前执行，否则老用户升级后运行态（服务监听地址/代理态等）丢失。
migrateLegacyStorageKeys();

const APP_STATE_STORAGE_KEY = "cursor-switch:runtime-state:v2";
const GENERIC_SERVICE_ERROR = "服务错误";
const SUPPORTED_MODEL_ADAPTER_TYPES = new Set(["openai", "anthropic"]);
const SUPPORTED_REASONING_EFFORTS = new Set(["low", "medium", "high", "xhigh", "max"]);
const SUPPORTED_ANTHROPIC_THINKING_EFFORTS = new Set(["low", "medium", "high", "xhigh", "max"]);
export const ANTHROPIC_THINKING_EFFORT_DEFAULT = "xhigh";
export const OPENAI_ENDPOINT_RESPONSES = "/v1/responses";
export const OPENAI_ENDPOINT_CHAT_COMPLETIONS = "/v1/chat/completions";
export const OPENAI_ENDPOINT_CUSTOM = "/custom";
export const OPENAI_EXTRA_PARAMS_DEFAULT_JSON = `{
  "service_tier": "priority"
}`;
export const EXTRA_PARAMS_DEFAULT_JSON = `{
}`;
export const CUSTOM_HEADERS_DEFAULT_JSON = `{
}`;
const SUPPORTED_OPENAI_ENDPOINTS = new Set([OPENAI_ENDPOINT_RESPONSES, OPENAI_ENDPOINT_CHAT_COMPLETIONS, OPENAI_ENDPOINT_CUSTOM]);
const SUPPORTED_ROUTE_MODES = new Set(["local", "upstream"]);
const PROXY_STATE_EVENT = "proxy:state";
const USER_CONFIG_CHANGED_EVENT = "user-config:changed";
const UPDATE_STATE_EVENT = "update:state";
const UPDATE_PROGRESS_EVENT = "update:progress";
const UPDATE_READY_EVENT = "update:ready";
const UPDATE_ERROR_EVENT = "update:error";
const MODEL_ADAPTER_TEST_UPDATED_EVENT = "model-adapter-test:updated";
const SUPPORTED_MODEL_ADAPTER_TEST_STATUSES = new Set(["idle", "running", "success", "error"]);
const HOME_METRICS_MIN_LOADING_MS = 600;

export const ROUTE_MODE_OPTIONS = [
  { label: "本地服务模式", value: "local" },
  { label: "直连 Cursor 模式", value: "upstream" },
];

function asString(value) {
  if (typeof value === "string") {
    return value.trim();
  }
  if (value instanceof String) {
    return value.toString().trim();
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return "";
}

function asBoolean(value, fallback = false) {
  if (typeof value === "boolean") {
    return value;
  }
  if (typeof value === "number") {
    return value !== 0;
  }
  const normalized = asString(value).toLowerCase();
  if (!normalized) {
    return fallback;
  }
  return normalized === "true" || normalized === "1" || normalized === "yes";
}

function asArray(value) {
  return Array.isArray(value) ? value : [];
}

function asPositiveIntegerString(value) {
  const text = asString(value);
  if (!text) {
    return "";
  }
  if (!/^\d+$/.test(text)) {
    return "";
  }
  return Number(text) > 0 ? text : "";
}

function asPositiveInteger(value) {
  const text = asPositiveIntegerString(value);
  if (!text) {
    return 0;
  }
  return Number(text);
}

// asInteger 解析任意整数（含负数），用于 priority 等允许负值的字段。
function asInteger(value) {
  if (typeof value === "number" && Number.isFinite(value) && Number.isInteger(value)) {
    return value;
  }
  const text = asString(value);
  if (!text) {
    return 0;
  }
  if (!/^-?\d+$/.test(text)) {
    return 0;
  }
  const parsed = Number(text);
  return Number.isFinite(parsed) ? parsed : 0;
}

function asNumber(value, fallback = 0) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  const text = asString(value);
  if (!text) {
    return fallback;
  }
  const parsed = Number(text);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function formatReleaseDate(value) {
  const text = asString(value);
  if (!text) {
    return "未知";
  }
  const parsed = dayjs(text);
  if (!parsed.isValid()) {
    return text;
  }
  return parsed.format("YYYY-MM-DD HH:mm");
}

function normalizeRouteMode(value, fallback = "local") {
  const text = asString(value).toLowerCase();
  if (SUPPORTED_ROUTE_MODES.has(text)) {
    return text;
  }
  return fallback;
}

// 审计第二部分「优先级 2」per-namespace 路由覆盖：单个路由面可独立选 local/upstream，
// 不改变全局 Mode。仅对 Local/Upstream 可切的路由有意义（officialProcedure 把两者设同一
// DirectAction，覆盖对其无影响）。值 "auto"/空 = 跟随全局（不写入表，由后端 normalize 丢弃）。
function normalizePerNamespace(raw) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return {};
  }
  const cleaned = {};
  for (const [name, mode] of Object.entries(raw)) {
    const key = asString(name);
    if (!key) continue;
    const value = asString(mode).toLowerCase();
    if (SUPPORTED_ROUTE_MODES.has(value)) {
      cleaned[key] = value;
    }
  }
  return cleaned;
}

// 审计「行为偏离-3」：WebSearch provider 合法值。duckduckgo = 免 key 降级质量；
// bing/serper/tavily = 需对应 APIKey（BYOK）。空/非认可值归 duckduckgo。
const SUPPORTED_WEB_SEARCH_PROVIDERS = new Set([
  "duckduckgo",
  "bing",
  "serper",
  "tavily",
]);

export const WEB_SEARCH_PROVIDER_OPTIONS = [
  { value: "duckduckgo", label: "DuckDuckGo（免 key，降级质量）" },
  { value: "bing", label: "Bing Web Search v7（需 key）" },
  { value: "serper", label: "Serper / Google（需 key）" },
  { value: "tavily", label: "Tavily（需 key）" },
];

function normalizeWebSearchProvider(value) {
  const text = asString(value).toLowerCase();
  if (SUPPORTED_WEB_SEARCH_PROVIDERS.has(text)) {
    return text;
  }
  return "duckduckgo";
}

// WebFetch host 白名单：去空白 + 小写 + 去重，空数组保留为空（后端归一为 nil = 硬拒绝基线）。
function normalizeWebFetchHostAllowlist(raw) {
  if (!Array.isArray(raw)) {
    return [];
  }
  const seen = new Set();
  const cleaned = [];
  for (const entry of raw) {
    const host = asString(entry).trim().toLowerCase();
    if (!host || seen.has(host)) continue;
    seen.add(host);
    cleaned.push(host);
  }
  return cleaned;
}
// 推理面（run_sse/bidi_append/ai_service）默认 byok local，不暴露在此——它们强制本地是
// 项目目标。此处只列「云端一体化服务」类，对应审计优先级 1：可按需透传到用户本人 cursor
// 账号（牺牲隐私换召回质量）。value 为路由名（后端 host.go 的 server.Name）。
export const PER_NAMESPACE_ROUTES = [
  {
    name: "cpp_service",
    label: "Tab 补全 / Git 提交消息",
    hint: "Cursor 专有小模型端点。默认走 Tab server 中转；可设为直连走本人 Cursor 账号。",
  },
  {
    name: "file_sync",
    label: "Codebase 索引 (@codebase)",
    hint: "RepositoryService/* codebase 索引。默认 byok 本地简化版（文件哈希清单，无向量检索）；设为直连走 Cursor 云端语义索引（代码上传 Cursor 云）。",
  },
  {
    name: "network_service",
    label: "NetworkService / @docs 文档",
    hint: "DocumentationQuery/@docs 等。默认 byok 本地文档库；设为直连复用 Cursor 云端预建文档索引。",
  },
];

function normalizeBaseURL(value) {
  const text = asString(value);
  if (!text) {
    return "";
  }
  try {
    const parsed = new URL(text);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return "";
    }
    parsed.protocol = parsed.protocol.toLowerCase();
    parsed.hostname = parsed.hostname.toLowerCase();
    const normalized = parsed.toString().replace(/\/+$/, "");
    return normalized || parsed.toString();
  } catch (_error) {
    return text;
  }
}

function buildModelAdapterIdentityKey(adapter) {
  return [
    normalizeBaseURL(adapter.baseURL),
    asString(adapter.modelID),
    asString(adapter.apiKey),
    asString(adapter.displayName),
    adapter.type === "openai" ? normalizeOpenAIEndpoint(adapter.openAIEndpoint) : "",
  ].join("\n");
}

function hashStringFNV32a(value) {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash.toString(16).padStart(8, "0");
}

export function buildModelAdapterTestRequestHash(source) {
  const adapter = normalizeModelAdapter(source);
  return hashStringFNV32a([
    asString(adapter.type),
    normalizeBaseURL(adapter.baseURL),
    asString(adapter.apiKey),
    asString(adapter.modelID),
    adapter.type === "openai" ? asString(adapter.reasoningEffort || "medium") : "",
    adapter.type === "openai" ? normalizeOpenAIEndpoint(adapter.openAIEndpoint) : "",
    adapter.type === "openai" ? String(Boolean(adapter.openAIExtraParamsEnabled)) : "false",
    adapter.type === "openai" && adapter.openAIExtraParamsEnabled ? asString(adapter.openAIExtraParamsJSON) : "",
    String(Boolean(adapter.customHeadersEnabled)),
    adapter.customHeadersEnabled ? asString(adapter.customHeadersJSON) : "",
    adapter.type === "anthropic" ? String(Boolean(adapter.anthropicExtraParamsEnabled)) : "false",
    adapter.type === "anthropic" && adapter.anthropicExtraParamsEnabled ? asString(adapter.anthropicExtraParamsJSON) : "",
    String(asPositiveInteger(adapter.contextWindowTokens)),
    String(asPositiveInteger(adapter.maxCompletionTokens)),
    String(asPositiveInteger(adapter.anthropicMaxTokens)),
    adapter.type === "anthropic" ? asString(adapter.anthropicThinkingEffort || ANTHROPIC_THINKING_EFFORT_DEFAULT) : "",
  ].join("\n"));
}

export function formatDuration(value) {
  const durationMS = Math.max(0, Math.round(asNumber(value)));
  if (durationMS < 1000) {
    return `${durationMS} ms`;
  }
  return `${(durationMS / 1000).toFixed(1)} s`;
}

function normalizeModelAdapterTestStatus(value) {
  const text = asString(value).toLowerCase();
  return SUPPORTED_MODEL_ADAPTER_TEST_STATUSES.has(text) ? text : "idle";
}

export function formatModelAdapterTestSummary(source) {
  const result = source && typeof source === "object" ? source : {};
  const status = normalizeModelAdapterTestStatus(result.status);
  if (status === "running") {
    return "测试中...";
  }
  if (status === "error") {
    return asString(result.error) || "模型测试失败";
  }
  if (status !== "success") {
    return "";
  }
  const roundedTPS = Math.max(0, Math.round(asNumber(result.tokensPerSecond)));
  return `${roundedTPS} t/s | 首字 ${formatDuration(result.firstTextTokenMS)}`;
}

function normalizeModelAdapterTestResult(source) {
  const raw = source && typeof source === "object" ? source : {};
  const status = normalizeModelAdapterTestStatus(raw.status);
  const normalized = {
    adapterID: asString(raw.adapterID),
    requestHash: asString(raw.requestHash),
    status,
    tokensPerSecond: Math.max(0, asNumber(raw.tokensPerSecond)),
    firstTextTokenMS: Math.max(0, Math.round(asNumber(raw.firstTextTokenMS))),
    totalDurationMS: Math.max(0, Math.round(asNumber(raw.totalDurationMS))),
    outputTokens: Math.max(0, Math.round(asNumber(raw.outputTokens))),
    tokensEstimated: asBoolean(raw.tokensEstimated),
    summaryText: asString(raw.summaryText),
    error: asString(raw.error),
    rawResponse: asString(raw.rawResponse),
    testedAt: asString(raw.testedAt),
  };
  if (!normalized.summaryText) {
    normalized.summaryText = formatModelAdapterTestSummary(normalized);
  }
  if (status === "error" && !normalized.summaryText) {
    normalized.summaryText = normalized.error || "模型测试失败";
  }
  return normalized;
}

function normalizeModelAdapterTestResults(source) {
  const raw = source && typeof source === "object" && !Array.isArray(source)
    ? source.results
    : source;
  return asArray(raw)
    .map((item) => normalizeModelAdapterTestResult(item))
    .filter((item) => item.adapterID);
}

export function createEmptyModelAdapter() {
  return {
    id: "",
    displayName: "",
    type: "openai",
    baseURL: "",
    apiKey: "",
    tooltipData: "备注",
    modelID: "",
    reasoningEffort: "medium",
    openAIEndpoint: OPENAI_ENDPOINT_RESPONSES,
    openAIExtraParamsEnabled: false,
    openAIExtraParamsJSON: OPENAI_EXTRA_PARAMS_DEFAULT_JSON,
    customHeadersEnabled: false,
    customHeadersJSON: CUSTOM_HEADERS_DEFAULT_JSON,
    anthropicExtraParamsEnabled: false,
    anthropicExtraParamsJSON: EXTRA_PARAMS_DEFAULT_JSON,
    contextWindowTokens: 0,
    maxCompletionTokens: 0,
    anthropicMaxTokens: 0,
    anthropicThinkingEffort: ANTHROPIC_THINKING_EFFORT_DEFAULT,
    thinkingBudgetTokens: 0,
    priority: 0,
    enabled: true,
    weight: 0,
  };
}

// normalizeOpenAIEndpoint 归一化 endpoint 路径。
// 支持三个预设值：/v1/responses、/v1/chat/completions、/custom（自定义路径）。
// 选 /custom 时，用户需在接口地址栏填写完整请求 URL。
function normalizeOpenAIEndpoint(value) {
  const text = asString(value).toLowerCase();
  if (!text) {
    return OPENAI_ENDPOINT_RESPONSES;
  }
  return SUPPORTED_OPENAI_ENDPOINTS.has(text) ? text : "";
}

function isValidOpenAIEndpoint(value) {
  return normalizeOpenAIEndpoint(value) !== "";
}

function validateJSONObject(value, label) {
  const text = asString(value);
  if (!text) {
    return `${label}不能为空`;
  }
  try {
    const parsed = JSON.parse(text);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return `${label}必须是 JSON 对象`;
    }
  } catch (_error) {
    return `${label}必须是合法 JSON 对象`;
  }
  return "";
}

function validateHeadersJSON(value) {
  const objectError = validateJSONObject(value, "自定义请求头 JSON");
  if (objectError) {
    return objectError;
  }
  const parsed = JSON.parse(asString(value));
  for (const [key, item] of Object.entries(parsed)) {
    if (!asString(key)) {
      return "自定义请求头名称不能为空";
    }
    if (typeof item !== "string") {
      return `自定义请求头 ${key} 的值必须是字符串`;
    }
  }
  return "";
}

function validateOpenAIExtraParamsJSON(value) {
  return validateJSONObject(value, "额外参数 JSON");
}

function validateAnthropicExtraParamsJSON(value) {
  return validateJSONObject(value, "Anthropic 额外参数 JSON");
}

export function normalizeModelAdapter(source) {
  const raw = source && typeof source === "object" ? source : {};
  const normalizedType = asString(raw.type).toLowerCase();
  const normalizedReasoningEffort = asString(raw.reasoningEffort || raw.reasoning_effort).toLowerCase();
  const normalizedAnthropicThinkingEffort = asString(
    raw.anthropicThinkingEffort
      ?? raw.anthropic_thinking_effort
      ?? raw.outputConfigEffort
      ?? raw.output_config_effort,
  ).toLowerCase();
  const normalizedOpenAIEndpoint = normalizeOpenAIEndpoint(
    raw.openAIEndpoint ?? raw.openaiEndpoint ?? raw.open_ai_endpoint ?? raw.endpoint,
  );
  const openAIExtraParamsEnabled = normalizedType === "openai"
    ? asBoolean(raw.openAIExtraParamsEnabled ?? raw.openaiExtraParamsEnabled ?? raw.open_ai_extra_params_enabled)
    : false;
  const openAIExtraParamsJSON = normalizedType === "openai"
    ? asString(raw.openAIExtraParamsJSON ?? raw.openaiExtraParamsJSON ?? raw.open_ai_extra_params_json) || OPENAI_EXTRA_PARAMS_DEFAULT_JSON
    : "";
  const customHeadersEnabled = asBoolean(raw.customHeadersEnabled ?? raw.custom_headers_enabled);
  const customHeadersJSON = asString(raw.customHeadersJSON ?? raw.custom_headers_json) || CUSTOM_HEADERS_DEFAULT_JSON;
  const anthropicExtraParamsEnabled = normalizedType === "anthropic"
    ? asBoolean(raw.anthropicExtraParamsEnabled ?? raw.anthropic_extra_params_enabled)
    : false;
  const anthropicExtraParamsJSON = normalizedType === "anthropic"
    ? asString(raw.anthropicExtraParamsJSON ?? raw.anthropic_extra_params_json) || EXTRA_PARAMS_DEFAULT_JSON
    : "";
  return {
    id: asString(raw.id),
    displayName: asString(raw.displayName || raw.name),
    type: SUPPORTED_MODEL_ADAPTER_TYPES.has(normalizedType) ? normalizedType : "",
    baseURL: normalizeBaseURL(raw.baseURL || raw.url),
    apiKey: asString(raw.apiKey || raw.key),
    tooltipData: asString(raw.tooltipData),
    modelID: asString(raw.modelID),
    reasoningEffort: SUPPORTED_REASONING_EFFORTS.has(normalizedReasoningEffort)
      ? normalizedReasoningEffort
      : "medium",
    openAIEndpoint: normalizedType === "openai" ? normalizedOpenAIEndpoint : "",
    openAIExtraParamsEnabled,
    openAIExtraParamsJSON,
    customHeadersEnabled,
    customHeadersJSON,
    anthropicExtraParamsEnabled,
    anthropicExtraParamsJSON,
    contextWindowTokens: asPositiveInteger(
      raw.contextWindowTokens ?? raw.context_window_tokens ?? raw.maxInputTokens ?? raw.max_input_tokens,
    ),
    maxCompletionTokens: asPositiveInteger(
      raw.maxCompletionTokens ?? raw.max_completion_tokens ?? raw.max_tokens ?? raw.max_token,
    ),
    anthropicMaxTokens: asPositiveInteger(
      raw.anthropicMaxTokens ?? raw.anthropic_max_tokens ?? raw.max_tokens,
    ),
    anthropicThinkingEffort: normalizedType === "anthropic"
      ? (SUPPORTED_ANTHROPIC_THINKING_EFFORTS.has(normalizedAnthropicThinkingEffort)
        ? normalizedAnthropicThinkingEffort
        : ANTHROPIC_THINKING_EFFORT_DEFAULT)
      : "",
    thinkingBudgetTokens: asPositiveInteger(
      raw.thinkingBudgetTokens ?? raw.thinking_budget_tokens,
    ),
    priority: asInteger(raw.priority),
    enabled: asBoolean(raw.enabled ?? true),
    weight: asPositiveInteger(raw.weight),
    // F-02: costMultiplier 透传——前端不解释此字段，仅 round-trip；
    // 后端 merge 时若 patch 里为空会从磁盘旧值继承，所以这里必须 carry-through，
    // 否则每次保存会把 per-adapter 倍率清空。
    costMultiplier: asString(raw.costMultiplier),
  };
}

export function normalizeModelAdapters(source) {
  return asArray(source).map((item) => normalizeModelAdapter(item));
}

export function validateModelAdapters(source) {
  const adapters = normalizeModelAdapters(source);
  const seenIdentityKeys = new Set();
  for (const [index, adapter] of adapters.entries()) {
    const prefix = `模型 ${index + 1}`;
    if (!adapter.displayName) {
      return `${prefix} 的显示名称不能为空`;
    }
    if (!SUPPORTED_MODEL_ADAPTER_TYPES.has(adapter.type)) {
      return `${prefix} 的类型仅支持 OpenAI 或 Anthropic`;
    }
    if (!adapter.baseURL) {
      return `${prefix} 的接口地址不能为空`;
    }
    if (!adapter.apiKey) {
      return `${prefix} 的访问密钥不能为空`;
    }
    if (!adapter.tooltipData) {
      return `${prefix} 的悬停提示不能为空`;
    }
    if (!adapter.modelID) {
      return `${prefix} 的模型标识不能为空`;
    }
    if (adapter.type === "openai" && !SUPPORTED_REASONING_EFFORTS.has(adapter.reasoningEffort)) {
      return `${prefix} 的推理强度仅支持 low、medium、high、xhigh、max`;
    }
    if (adapter.type === "openai" && !isValidOpenAIEndpoint(adapter.openAIEndpoint)) {
      return `${prefix} 的 OpenAI 端点仅支持 /v1/responses、/v1/chat/completions 或以 / 开头的自定义路径`;
    }
    if (adapter.type === "openai" && adapter.openAIExtraParamsEnabled) {
      const extraParamsError = validateOpenAIExtraParamsJSON(adapter.openAIExtraParamsJSON);
      if (extraParamsError) {
        return `${prefix} 的 ${extraParamsError}`;
      }
    }
    if (adapter.customHeadersEnabled) {
      const customHeadersError = validateHeadersJSON(adapter.customHeadersJSON);
      if (customHeadersError) {
        return `${prefix} 的 ${customHeadersError}`;
      }
    }
    if (adapter.type === "anthropic" && adapter.anthropicExtraParamsEnabled) {
      const extraParamsError = validateAnthropicExtraParamsJSON(adapter.anthropicExtraParamsJSON);
      if (extraParamsError) {
        return `${prefix} 的 ${extraParamsError}`;
      }
    }
    if (adapter.type === "anthropic" && !SUPPORTED_ANTHROPIC_THINKING_EFFORTS.has(adapter.anthropicThinkingEffort)) {
      return `${prefix} 的 Anthropic 思考强度仅支持 low、medium、high、xhigh、max`;
    }
    if (adapter.contextWindowTokens && (!Number.isInteger(adapter.contextWindowTokens) || adapter.contextWindowTokens <= 0)) {
      return `${prefix} 的上下文窗口必须为正整数`;
    }
    if (adapter.maxCompletionTokens && (!Number.isInteger(adapter.maxCompletionTokens) || adapter.maxCompletionTokens <= 0)) {
      return `${prefix} 的最大输出 Token 必须为正整数`;
    }
    if (adapter.anthropicMaxTokens && (!Number.isInteger(adapter.anthropicMaxTokens) || adapter.anthropicMaxTokens <= 0)) {
      return `${prefix} 的最大输出 Token 必须为正整数`;
    }
    if (adapter.thinkingBudgetTokens && (!Number.isInteger(adapter.thinkingBudgetTokens) || adapter.thinkingBudgetTokens <= 0)) {
      return `${prefix} 的思考预算 Token 必须为正整数`;
    }
    if (!Number.isInteger(adapter.priority)) {
      return `${prefix} 的优先级必须为整数`;
    }
    if (!Number.isInteger(adapter.weight) || adapter.weight < 0) {
      return `${prefix} 的权重必须为非负整数`;
    }
    const dedupeKey = buildModelAdapterIdentityKey(adapter);
    if (seenIdentityKeys.has(dedupeKey)) {
      return `模型渠道重复，请检查 url、modelID、apiKey、displayName、endpoint 组合`;
    }
    seenIdentityKeys.add(dedupeKey);
  }
  return "";
}

function validateConfigPayload(payload) {
  if (!SUPPORTED_ROUTE_MODES.has(normalizeRouteMode(payload?.routing?.mode, ""))) {
    return "运行模式仅支持 local 或 upstream";
  }
  return "";
}

function canUseLocalStorage() {
  return typeof window !== "undefined" && typeof window.localStorage !== "undefined";
}

function delay(ms) {
  return new Promise((resolve) => {
    window.setTimeout(resolve, Math.max(0, ms));
  });
}

function createEmptyHomeMetrics() {
  return {
    turnsTotal: 0,
    validTurnsTotal: 0,
    invalidTurnsTotal: 0,
    requestTokensTotal: 0,
    promptTokensTotal: 0,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    cacheHitRate: null,
  };
}

function loadCachedState() {
  if (!canUseLocalStorage()) {
    return {};
  }

  try {
    const raw = window.localStorage.getItem(APP_STATE_STORAGE_KEY);
    if (!raw) {
      return {};
    }
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") {
      return {};
    }
    return parsed;
  } catch (_error) {
    return {};
  }
}

// 导出供 normalizer 单测（审计 L3）：纯函数，导出无副作用，让 config 层契约可被回归测试锁定。
export function normalizeConfig(source) {
  const raw = source && typeof source === "object" ? source : {};
  const routing = raw.routing && typeof raw.routing === "object" ? raw.routing : {};
  const homeMetrics = raw.homeMetrics && typeof raw.homeMetrics === "object" ? raw.homeMetrics : {};
  const webTools = raw.webTools && typeof raw.webTools === "object" ? raw.webTools : {};
  return {
    log: asBoolean(raw.log),
    providerStreamIdleTimeout: asPositiveInteger(raw.providerStreamIdleTimeout),
    backendListenAddr: asString(raw.configBackendListenAddr) || asString(raw.backendListenAddr),
    proxyListenAddr: asString(raw.configProxyListenAddr) || asString(raw.proxyListenAddr),
    modelAdapters: normalizeModelAdapters(raw.modelAdapters),
    routing: {
      mode: normalizeRouteMode(routing.mode),
      // F-02: tabServerBaseURL carry-through——前端不解释，仅 round-trip。
      // 后端 merge 忠实写 patch 值，所以前端必须携带，否则每次保存会清空 tab 地址。
      tabServerBaseURL: asString(routing.tabServerBaseURL),
      // 审计第二部分「优先级 2」per-namespace 路由覆盖 round-trip——前端不解释语义，
      // 仅清洗（local/upstream 合法值）并原样回传。后端 merge 整块覆盖。
      perNamespace: normalizePerNamespace(routing.perNamespace),
    },
    homeMetrics: {
      includeCacheWriteInHitRate: asBoolean(homeMetrics.includeCacheWriteInHitRate),
    },
    // 审计「行为偏离-3」：WebSearch provider + WebFetch host 白名单 round-trip。
    // 前端清洗 provider 合法值，白名单去空白+去重+小写；后端 merge 整块覆盖。
    webTools: {
      webSearchProvider: normalizeWebSearchProvider(webTools.webSearchProvider),
      webSearchAPIKey: asString(webTools.webSearchAPIKey),
      webFetchHostAllowlist: normalizeWebFetchHostAllowlist(webTools.webFetchHostAllowlist),
    },
    lastAgentModelHash: asString(raw.lastAgentModelHash),
    // F-02: pricing 透传——前端从不解释 pricing 字段（CRUD 走 MetricsService 独立路径），
    // 但 buildConfigPayload 整包保存时若不带 pricing，后端 merge 会保留磁盘值（主防线）。
    // 这里 carry-through 仅保证 round-trip 一致，真正兜底在后端 MergeUserPatch。
    pricing: raw.pricing && typeof raw.pricing === "object" ? raw.pricing : {},
  };
}

function asNullableRate(value) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  return null;
}

function normalizeHomeMetrics(source) {
  const raw = source && typeof source === "object" ? source : {};
  const providerCallsTotal = asPositiveInteger(raw.providerCallsTotal ?? raw.turnsTotal);
  return {
    turnsTotal: providerCallsTotal,
    validTurnsTotal: providerCallsTotal,
    invalidTurnsTotal: 0,
    requestTokensTotal: asPositiveInteger(raw.requestTokensTotal),
    promptTokensTotal: asPositiveInteger(raw.promptTokensTotal),
    cacheReadTokens: asPositiveInteger(raw.cacheReadTokens),
    cacheWriteTokens: asPositiveInteger(raw.cacheWriteTokens),
    cacheHitRate: asNullableRate(raw.cacheHitRate),
  };
}

function applyHomeMetrics(raw) {
  appState.homeMetrics = normalizeHomeMetrics(raw);
  appState.homeMetricsError = "";
}

function buildConfigPayload(source = appState) {
  const normalized = normalizeConfig(source);
  return {
    log: normalized.log,
    providerStreamIdleTimeout: normalized.providerStreamIdleTimeout,
    backendListenAddr: normalized.backendListenAddr,
    proxyListenAddr: normalized.proxyListenAddr,
    modelAdapters: normalized.modelAdapters.map(({ id, ...adapter }) => adapter),
    routing: normalized.routing,
    homeMetrics: normalized.homeMetrics,
    webTools: normalized.webTools,
    lastAgentModelHash: normalized.lastAgentModelHash,
    // F-02: pricing 透传到 payload——后端 MergeUserPatch 保留磁盘 pricing，
    // 但前端必须把当前已加载的 pricing 带回去，避免 round-trip 后状态丢失。
    pricing: normalized.pricing,
  };
}

function applyConfigToState(config, { modelAdaptersOnly = false } = {}) {
  const normalized = normalizeConfig(config);
  if (modelAdaptersOnly) {
    appState.modelAdapters = normalized.modelAdapters;
    return normalized;
  }
  appState.modelAdapters = normalized.modelAdapters;
  appState.configBackendListenAddr = normalized.backendListenAddr;
  appState.configProxyListenAddr = normalized.proxyListenAddr;
  appState.routingMode = normalized.routing.mode;
  appState.tabServerBaseURL = normalized.routing.tabServerBaseURL || "";
  appState.perNamespace = normalized.routing.perNamespace;
  appState.includeCacheWriteInHitRate = normalized.homeMetrics.includeCacheWriteInHitRate;
  appState.webSearchProvider = normalized.webTools.webSearchProvider || "";
  appState.webSearchAPIKey = normalized.webTools.webSearchAPIKey || "";
  appState.webFetchHostAllowlist = normalized.webTools.webFetchHostAllowlist || [];
  return normalized;
}

async function loadPersistedUserConfig() {
  return normalizeConfig(await loadUserConfig());
}

async function persistConfigPayload(config, { modelAdaptersOnly = false } = {}) {
  const payload = buildConfigPayload(config);
  const configValidationError = validateConfigPayload(payload);
  if (configValidationError) {
    return {
      ok: false,
      error: configValidationError,
    };
  }
  const validationError = validateModelAdapters(payload.modelAdapters);
  if (validationError) {
    return {
      ok: false,
      error: validationError,
    };
  }

  appState.configSaving = true;
  // P1-8：区分"保存失败"与"保存成功但回读失败"。
  // saveUserConfig 成功后磁盘已是新值；若此时 loadPersistedUserConfig/applyConfigToState
  // 因 Wails 超时/网络抖动抛错，旧逻辑返回 ok:false → 调用方把 appState 回滚到旧值，
  // 造成 UI 显示旧值而磁盘是新值的脱节。修复：保存成功后回读失败不视为失败，
  // 保留乐观值（下次任意刷新会对齐磁盘真值），绝不回滚到与磁盘矛盾的旧值。
  let savedToDisk = false;
  try {
    await saveUserConfig(payload);
    savedToDisk = true;
    const persisted = await loadPersistedUserConfig();
    applyConfigToState(persisted, { modelAdaptersOnly });
    return {
      ok: true,
      error: "",
    };
  } catch (error) {
    if (savedToDisk) {
      // 保存已落盘，仅回读失败：不回滚（避免 UI/disk 脱节），返回成功，
      // 下次 reloadUserConfig 会把 appState 对齐磁盘真值。
      return {
        ok: true,
        error: "",
      };
    }
    return {
      ok: false,
      error: toUserError(error),
    };
  } finally {
    appState.configSaving = false;
  }
}

function applyProxyState(raw) {
  const state = raw && typeof raw === "object" ? raw : {};
  appState.backendRunning = asBoolean(state.backendRunning);
  appState.proxyRunning = asBoolean(state.proxyRunning ?? state.running);
  appState.serviceRunning = appState.proxyRunning;
  appState.serviceLastError = asString(state.lastError);
  appState.backendListenAddr = asString(state.backendListenAddr);
  appState.proxyListenAddr = asString(state.proxyListenAddr || state.listenAddr);
  appState.serviceListenAddr = appState.proxyListenAddr;
  appState.cursorSettingsApplied = asBoolean(state.cursorSettingsApplied);
  appState.netProxySource = asString(state.netProxySource);
  appState.netProxyActive = asBoolean(state.netProxyActive);
  appState.netProxyUsingSystem = asBoolean(state.netProxyUsingSystem);
  appState.netProxyUsingEnv = asBoolean(state.netProxyUsingEnv);
  appState.netProxyHttp = asString(state.netProxyHttp);
  appState.netProxyHttps = asString(state.netProxyHttps);
  appState.netProxyPacIgnored = asBoolean(state.netProxyPacIgnored);
  appState.netProxyDescription = asString(state.netProxyDescription);
}

function handleProxyStateEvent(event) {
  if (event?.data && typeof event.data === "object") {
    applyProxyState(event.data);
    return;
  }
  void syncServiceState().catch(() => {});
}

function handleUserConfigChangedEvent(event) {
  if (event?.data && typeof event.data === "object") {
    applyConfigToState(event.data);
    return;
  }
  void reloadUserConfig().catch(() => {});
}

function applyModelAdapterTestResults(source) {
  const next = {};
  for (const result of normalizeModelAdapterTestResults(source)) {
    next[result.adapterID] = result;
  }
  appState.modelAdapterTestResults = next;
  return next;
}

function handleModelAdapterTestUpdatedEvent(event) {
  if (event?.data) {
    applyModelAdapterTestResults(event.data);
    return;
  }
  void refreshModelAdapterTestResults().catch(() => {});
}

function normalizeUpdateState(value) {
  const text = asString(value).toLowerCase();
  if (["idle", "checking", "downloading", "ready", "installing", "error"].includes(text)) {
    return text;
  }
  return "idle";
}

function applyUpdateSnapshot(raw) {
  const data = raw && typeof raw === "object" ? raw : {};
  const nextState = normalizeUpdateState(data.state ?? appState.updateState);
  appState.updateState = nextState;

  const version = asString(data.version);
  if (version) {
    appState.updateVersion = version;
  } else if (nextState === "idle") {
    appState.updateVersion = "";
  }

  const releaseDate = asString(data.releaseDate);
  if (releaseDate) {
    appState.updateReleaseDate = releaseDate;
  } else if (nextState === "idle") {
    appState.updateReleaseDate = "";
  }

  if (typeof data.releaseNotes === "string") {
    appState.updateReleaseNotes = data.releaseNotes.replace(/\r\n/g, "\n");
  } else if (nextState === "idle") {
    appState.updateReleaseNotes = "";
  }

  if (typeof data.error === "string") {
    appState.updateError = data.error.trim();
  } else if (nextState !== "error") {
    appState.updateError = "";
  }

  if (typeof data.message === "string") {
    appState.updateMessage = data.message.trim();
  } else if (!data.prompt) {
    appState.updateMessage = "";
  }

  if (typeof data.downloaded === "number") {
    appState.updateProgressDownloaded = data.downloaded;
  } else if (nextState !== "downloading") {
    appState.updateProgressDownloaded = 0;
  }

  if (typeof data.total === "number") {
    appState.updateProgressTotal = data.total;
  } else if (nextState !== "downloading") {
    appState.updateProgressTotal = 0;
  }

  if (typeof data.percentage === "number") {
    appState.updateProgressPercent = Math.max(0, Math.min(100, data.percentage));
  } else if (nextState !== "downloading") {
    appState.updateProgressPercent = 0;
  }
}

function openUpdatePrompt(kind, payload = {}) {
  appState.updatePromptKind = asString(kind) || "idle";
  appState.updatePromptVisible = true;
  appState.updatePromptBusy = false;
  if (typeof payload.message === "string") {
    appState.updateMessage = payload.message.trim();
  }
  if (typeof payload.error === "string") {
    appState.updateError = payload.error.trim();
  }
}

function handleUpdateStateEvent(event) {
  const data = event?.data && typeof event.data === "object" ? event.data : {};
  applyUpdateSnapshot(data);
  if (asBoolean(data.prompt)) {
    openUpdatePrompt(asString(data.promptKind) || "idle", data);
  }
}

function handleUpdateProgressEvent(event) {
  const data = event?.data && typeof event.data === "object" ? event.data : {};
  applyUpdateSnapshot({
    ...data,
    state: "downloading",
  });
}

function handleUpdateReadyEvent(event) {
  const data = event?.data && typeof event.data === "object" ? event.data : {};
  applyUpdateSnapshot({
    ...data,
    state: "ready",
  });
  if (data.prompt !== false) {
    openUpdatePrompt("ready", data);
  }
}

function handleUpdateErrorEvent(event) {
  const data = event?.data && typeof event.data === "object" ? event.data : {};
  applyUpdateSnapshot({
    ...data,
    state: "error",
  });
  if (asBoolean(data.prompt)) {
    openUpdatePrompt("error", data);
  }
}

function extractErrorMessage(error) {
  if (typeof error === "string") {
    return error.trim();
  }
  if (error && typeof error === "object") {
    return asString(error.message) || asString(error.error);
  }
  return "";
}

const cachedState = loadCachedState();
const cachedConfig = normalizeConfig(cachedState);

export const appState = reactive({
  appVersion: "",
  modelAdapters: cachedConfig.modelAdapters,
  modelAdapterTestResults: {},
  configBackendListenAddr: cachedConfig.backendListenAddr,
  configProxyListenAddr: cachedConfig.proxyListenAddr,
  routingMode: cachedConfig.routing.mode,
  tabServerBaseURL: cachedConfig.routing.tabServerBaseURL || "",
  perNamespace: cachedConfig.routing.perNamespace,
  includeCacheWriteInHitRate: cachedConfig.homeMetrics.includeCacheWriteInHitRate,
  // 审计「行为偏离-3」：WebSearch provider/key + WebFetch host 白名单。
  webSearchProvider: cachedConfig.webTools.webSearchProvider || "duckduckgo",
  webSearchAPIKey: cachedConfig.webTools.webSearchAPIKey || "",
  webFetchHostAllowlist: cachedConfig.webTools.webFetchHostAllowlist || [],

  serviceRunning: asBoolean(cachedState.serviceRunning),
  backendRunning: asBoolean(cachedState.backendRunning),
  proxyRunning: asBoolean(cachedState.proxyRunning),
  serviceBusy: false,
  serviceLastError: asString(cachedState.serviceLastError),
  serviceListenAddr: asString(cachedState.serviceListenAddr),
  backendListenAddr: asString(cachedState.backendListenAddr),
  proxyListenAddr: asString(cachedState.proxyListenAddr),
  cursorSettingsApplied: asBoolean(cachedState.cursorSettingsApplied),
  netProxySource: asString(cachedState.netProxySource),
  netProxyActive: asBoolean(cachedState.netProxyActive),
  netProxyUsingSystem: asBoolean(cachedState.netProxyUsingSystem),
  netProxyUsingEnv: asBoolean(cachedState.netProxyUsingEnv),
  netProxyHttp: asString(cachedState.netProxyHttp),
  netProxyHttps: asString(cachedState.netProxyHttps),
  netProxyPacIgnored: asBoolean(cachedState.netProxyPacIgnored),
  netProxyDescription: asString(cachedState.netProxyDescription),

  configSaving: false,
  homeMetrics: createEmptyHomeMetrics(),
  homeMetricsLoading: false,
  homeMetricsError: "",

  updateState: "idle",
  updateVersion: "",
  updateReleaseDate: "",
  updateReleaseNotes: "",
  updateProgressDownloaded: 0,
  updateProgressTotal: 0,
  updateProgressPercent: 0,
  updateError: "",
  updateMessage: "",
  updatePromptVisible: false,
  updatePromptKind: "idle",
  updatePromptBusy: false,
});

watchSyncEffect(() => {
  if (!canUseLocalStorage()) {
    return;
  }
  try {
    window.localStorage.setItem(
      APP_STATE_STORAGE_KEY,
      JSON.stringify({
        ...buildConfigPayload(),
        serviceRunning: appState.serviceRunning,
        backendRunning: appState.backendRunning,
        proxyRunning: appState.proxyRunning,
        serviceLastError: appState.serviceLastError,
        serviceListenAddr: appState.serviceListenAddr,
        configBackendListenAddr: appState.configBackendListenAddr,
        configProxyListenAddr: appState.configProxyListenAddr,
        backendListenAddr: appState.backendListenAddr,
        proxyListenAddr: appState.proxyListenAddr,
        cursorSettingsApplied: appState.cursorSettingsApplied,
        netProxySource: appState.netProxySource,
        netProxyActive: appState.netProxyActive,
        netProxyUsingSystem: appState.netProxyUsingSystem,
        netProxyUsingEnv: appState.netProxyUsingEnv,
        netProxyHttp: appState.netProxyHttp,
        netProxyHttps: appState.netProxyHttps,
        netProxyPacIgnored: appState.netProxyPacIgnored,
        netProxyDescription: appState.netProxyDescription,
      }),
    );
  } catch (_error) {
    // ignore local persistence failures
  }
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(PROXY_STATE_EVENT, handleProxyStateEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(USER_CONFIG_CHANGED_EVENT, handleUserConfigChangedEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(MODEL_ADAPTER_TEST_UPDATED_EVENT, handleModelAdapterTestUpdatedEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(UPDATE_STATE_EVENT, handleUpdateStateEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(UPDATE_PROGRESS_EVENT, handleUpdateProgressEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(UPDATE_READY_EVENT, handleUpdateReadyEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(UPDATE_ERROR_EVENT, handleUpdateErrorEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

export const appViewState = reactive({
  serviceStatusText: computed(() => {
    if (appState.proxyRunning && appState.backendRunning) {
      return "服务运行中";
    }
    if (appState.backendRunning) {
      return "后端已启动，代理未启动";
    }
    return "服务未启动";
  }),
  serviceStatusClass: computed(() =>
    appState.serviceRunning ? "text-[#22c55e]" : "text-[#f59e0b]",
  ),
  serviceButtonText: computed(() => {
    if (appState.serviceBusy) {
      return appState.serviceRunning ? "关闭中..." : "启动中...";
    }
    return appState.serviceRunning ? "关闭服务" : "启动服务";
  }),
});

export const updateViewState = reactive({
  footerDownloading: computed(() => appState.updateState === "downloading"),
  footerBusy: computed(() => ["checking", "installing"].includes(appState.updateState)),
  footerVersionLabel: computed(() => `v${appState.appVersion || "..."}`),
  footerProgressText: computed(() => `${Math.round(appState.updateProgressPercent || 0)}%`),
  footerProgressStyle: computed(() => ({
    width: `${Math.max(0, Math.min(100, appState.updateProgressPercent || 0))}%`,
  })),
  promptTitle: computed(() => {
    switch (appState.updatePromptKind) {
      case "ready":
        return "发现新版本";
      case "error":
        return "更新失败";
      default:
        return "检查更新";
    }
  }),
  promptContent: computed(() => {
    switch (appState.updatePromptKind) {
      case "ready":
        return [
          `版本：v${appState.updateVersion || appState.appVersion || "..."}`,
          `发布时间：${formatReleaseDate(appState.updateReleaseDate)}`,
          "",
          appState.updateReleaseNotes || "无更新说明",
        ].join("\n");
      case "error":
        return appState.updateError || appState.updateMessage || GENERIC_SERVICE_ERROR;
      default:
        return appState.updateMessage || `当前已是最新版本（v${appState.appVersion || "..."}）。`;
    }
  }),
  promptConfirmText: computed(() =>
    appState.updatePromptKind === "ready" ? "立即重启更新" : "确定",
  ),
  promptCancelText: computed(() =>
    appState.updatePromptKind === "ready" ? "稍后" : "取消",
  ),
  promptShowCancel: computed(() => appState.updatePromptKind === "ready"),
});

export function getModelAdapterTestResultByID(adapterID) {
  const id = asString(adapterID);
  if (!id) {
    return null;
  }
  return appState.modelAdapterTestResults[id] ?? null;
}

export function getModelAdapterTestResult(adapter) {
  const normalized = normalizeModelAdapter(adapter);
  if (normalized.id && appState.modelAdapterTestResults[normalized.id]) {
    return appState.modelAdapterTestResults[normalized.id];
  }
  const requestHash = buildModelAdapterTestRequestHash(normalized);
  return Object.values(appState.modelAdapterTestResults).find((result) => result.requestHash === requestHash) ?? null;
}

export function isModelAdapterTestResultRunning(adapter) {
  return getModelAdapterTestResult(adapter)?.status === "running";
}

export function isModelAdapterTestResultStale(adapter, result) {
  if (!result || !result.requestHash) {
    return false;
  }
  return result.requestHash !== buildModelAdapterTestRequestHash(adapter);
}

export async function refreshModelAdapterTestResults() {
  const results = await getModelAdapterTestResults();
  applyModelAdapterTestResults(results);
  return Object.values(appState.modelAdapterTestResults);
}

export function startModelAdapterTest(adapter) {
  const normalized = normalizeModelAdapter(adapter);
  return testModelAdapter(normalized).then((rawResult) => {
    const result = normalizeModelAdapterTestResult(rawResult);
    if (result.adapterID) {
      appState.modelAdapterTestResults = {
        ...appState.modelAdapterTestResults,
        [result.adapterID]: result,
      };
    }
    return result;
  });
}

export async function runModelAdapterTest(adapter) {
  return startModelAdapterTest(adapter);
}

export async function persistUserConfig() {
  const currentConfig = await loadPersistedUserConfig();
  return persistConfigPayload({
    ...currentConfig,
    modelAdapters: normalizeModelAdapters(appState.modelAdapters),
    routing: {
      mode: appState.routingMode,
      tabServerBaseURL: appState.tabServerBaseURL || "",
      perNamespace: normalizePerNamespace(appState.perNamespace),
    },
    homeMetrics: {
      ...currentConfig.homeMetrics,
      includeCacheWriteInHitRate: appState.includeCacheWriteInHitRate,
    },
    webTools: {
      webSearchProvider: appState.webSearchProvider || "duckduckgo",
      webSearchAPIKey: appState.webSearchAPIKey || "",
      webFetchHostAllowlist: normalizeWebFetchHostAllowlist(appState.webFetchHostAllowlist),
    },
  });
}

export async function saveIncludeCacheWriteInHitRate(value) {
  const currentConfig = await loadPersistedUserConfig();
  const previousValue = appState.includeCacheWriteInHitRate;
  const nextValue = asBoolean(value);
  appState.includeCacheWriteInHitRate = nextValue;
  const result = await persistConfigPayload({
    ...currentConfig,
    homeMetrics: {
      ...currentConfig.homeMetrics,
      includeCacheWriteInHitRate: nextValue,
    },
  });
  if (!result.ok) {
    appState.includeCacheWriteInHitRate = previousValue;
  }
  return result;
}

export async function saveRoutingMode(mode) {
  const currentConfig = await loadPersistedUserConfig();
  return persistConfigPayload({
    ...currentConfig,
    routing: {
      mode: normalizeRouteMode(mode),
      tabServerBaseURL: currentConfig.routing?.tabServerBaseURL || "",
      perNamespace: normalizePerNamespace(currentConfig.routing?.perNamespace),
    },
  });
}

// savePerNamespaceRoute 设置单条路由（namespace）的模式覆盖（审计第二部分「优先级 2」）。
// mode = "local" | "upstream" | "auto"（auto = 跟随全局，等价于从覆盖表移除该路由）。
export async function savePerNamespaceRoute(routeName, mode) {
  const currentConfig = await loadPersistedUserConfig();
  const previous = appState.perNamespace;
  const next = { ...normalizePerNamespace(currentConfig.routing?.perNamespace) };
  const key = asString(routeName);
  const value = asString(mode).toLowerCase();
  if (key && (value === "local" || value === "upstream")) {
    next[key] = value;
  } else if (key && (value === "auto" || value === "")) {
    // auto / 空 = 跟随全局，从覆盖表移除。
    delete next[key];
  }
  appState.perNamespace = next;
  const result = await persistConfigPayload({
    ...currentConfig,
    routing: {
      mode: currentConfig.routing?.mode || "local",
      tabServerBaseURL: currentConfig.routing?.tabServerBaseURL || "",
      perNamespace: next,
    },
  });
  if (!result.ok) {
    appState.perNamespace = previous;
  }
  return result;
}

// saveTabServerBaseURL 设置 tab 补全/git 消息流量的上游地址（H1）。
// 空字符串 = 禁用第三方 tab server 重定向，透传官方 api2.cursor.sh（走用户自己的 Cursor 账号）。
export async function saveTabServerBaseURL(value) {
  const currentConfig = await loadPersistedUserConfig();
  const previousValue = appState.tabServerBaseURL;
  const nextValue = String(value || "").trim();
  appState.tabServerBaseURL = nextValue;
  const result = await persistConfigPayload({
    ...currentConfig,
    routing: {
      mode: currentConfig.routing?.mode || "local",
      tabServerBaseURL: nextValue,
      perNamespace: normalizePerNamespace(currentConfig.routing?.perNamespace),
    },
  });
  if (!result.ok) {
    appState.tabServerBaseURL = previousValue;
  }
  return result;
}

// saveWebSearchProvider 设置 WebSearch 上游 provider（审计「行为偏离-3」）。
// duckduckgo = 免 key 降级；bing/serper/tavily = 需对应 APIKey（BYOK）。
export async function saveWebSearchProvider(provider) {
  const currentConfig = await loadPersistedUserConfig();
  const previous = appState.webSearchProvider;
  const next = normalizeWebSearchProvider(provider);
  appState.webSearchProvider = next;
  const result = await persistConfigPayload({
    ...currentConfig,
    webTools: {
      webSearchProvider: next,
      webSearchAPIKey: currentConfig.webTools?.webSearchAPIKey || "",
      webFetchHostAllowlist: normalizeWebFetchHostAllowlist(currentConfig.webTools?.webFetchHostAllowlist),
    },
  });
  if (!result.ok) {
    appState.webSearchProvider = previous;
  }
  return result;
}

// saveWebSearchAPIKey 设置 WebSearch provider 的 API key（BYOK，明文本机存储）。
export async function saveWebSearchAPIKey(value) {
  const currentConfig = await loadPersistedUserConfig();
  const previous = appState.webSearchAPIKey;
  const next = String(value || "").trim();
  appState.webSearchAPIKey = next;
  const result = await persistConfigPayload({
    ...currentConfig,
    webTools: {
      webSearchProvider: normalizeWebSearchProvider(currentConfig.webTools?.webSearchProvider || appState.webSearchProvider),
      webSearchAPIKey: next,
      webFetchHostAllowlist: normalizeWebFetchHostAllowlist(currentConfig.webTools?.webFetchHostAllowlist),
    },
  });
  if (!result.ok) {
    appState.webSearchAPIKey = previous;
  }
  return result;
}

// saveWebFetchHostAllowlist 设置 WebFetch 允许放行的内网 host 白名单（审计「行为偏离-3」）。
// 企业内网 Wiki/Confluence 场景：默认空 = 保持 SSRF 硬拒绝基线；显式放行的 host 绕过私网拒绝。
// rawAllowlist 为字符串数组（或逗号/换行分隔字符串，由 Config.vue 拆分后传入数组）。
export async function saveWebFetchHostAllowlist(rawAllowlist) {
  const currentConfig = await loadPersistedUserConfig();
  const previous = appState.webFetchHostAllowlist;
  const next = normalizeWebFetchHostAllowlist(rawAllowlist);
  appState.webFetchHostAllowlist = next;
  const result = await persistConfigPayload({
    ...currentConfig,
    webTools: {
      webSearchProvider: normalizeWebSearchProvider(currentConfig.webTools?.webSearchProvider || appState.webSearchProvider),
      webSearchAPIKey: currentConfig.webTools?.webSearchAPIKey || "",
      webFetchHostAllowlist: next,
    },
  });
  if (!result.ok) {
    appState.webFetchHostAllowlist = previous;
  }
  return result;
}

export async function reloadUserConfig(options = {}) {
  const config = await loadPersistedUserConfig();
  applyConfigToState(config, options);
  return config;
}

export async function saveModelAdapterAt(index, adapter) {
  const currentConfig = await loadPersistedUserConfig();
  const nextAdapters = normalizeModelAdapters(currentConfig.modelAdapters);
  const nextAdapter = normalizeModelAdapter(adapter);
  const targetIndex = index >= 0 && index < nextAdapters.length ? index : nextAdapters.length;

  if (index >= 0 && index < nextAdapters.length) {
    nextAdapters.splice(index, 1, nextAdapter);
  } else {
    nextAdapters.push(nextAdapter);
  }

  const result = await persistConfigPayload(
    {
      ...currentConfig,
      modelAdapters: nextAdapters,
    },
    { modelAdaptersOnly: true },
  );
  if (!result.ok) {
    return result;
  }
  return {
    ...result,
    index: targetIndex,
    adapter: appState.modelAdapters[targetIndex] ?? null,
  };
}

// 批量追加模型适配器：从「获取模型列表」多选时使用。
// 以 templateAdapter 继承 baseURL/apiKey/type/endpoint/extra params/customHeaders，
// 仅替换每条的 modelID + displayName。与现有 adapter 按 (baseURL+modelID+apiKey) 去重，
// 已存在的跳过并计入 skipped 列表。
export async function appendModelAdaptersBatch(templateAdapter, models) {
  const currentConfig = await loadPersistedUserConfig();
  const nextAdapters = normalizeModelAdapters(currentConfig.modelAdapters);
  const template = normalizeModelAdapter(templateAdapter);

  const existingKeys = new Set(
    nextAdapters.map((adapter) => buildModelAdapterIdentityKey(adapter)),
  );

  const added = [];
  const skipped = [];
  for (const model of models ?? []) {
    const modelID = String(model?.id || "").trim();
    if (!modelID) {
      continue;
    }
    const displayName = String(model?.displayName || "").trim() || modelID;
    // 若 fetch-models 已反查到上下文窗口（内置 models.dev 缓存表），自动带入，省得用户手填。
    const contextWindowTokens = Number(model?.contextWindowTokens) || 0;
    const candidate = {
      ...template,
      id: "",
      modelID,
      displayName,
      // tooltipData 服务端要求非空：保留模板值，模板没设就用 displayName 兜底（避免批量添加卡在悬停提示校验）。
      tooltipData: template.tooltipData || displayName,
      // 仅当模板未显式设置窗口时才用反查值，尊重用户在模板里填的覆盖。
      contextWindowTokens: template.contextWindowTokens || contextWindowTokens,
    };
    const identityKey = buildModelAdapterIdentityKey(candidate);
    if (existingKeys.has(identityKey)) {
      skipped.push(modelID);
      continue;
    }
    existingKeys.add(identityKey);
    nextAdapters.push(candidate);
    added.push(modelID);
  }

  if (added.length === 0) {
    return {
      ok: false,
      error: skipped.length ? `所选模型均已存在：${skipped.join("、")}` : "没有可添加的模型",
      added: [],
      skipped,
    };
  }

  const result = await persistConfigPayload(
    {
      ...currentConfig,
      modelAdapters: nextAdapters,
    },
    { modelAdaptersOnly: true },
  );
  return {
    ...result,
    added,
    skipped,
  };
}

export async function deleteModelAdapterAt(index) {
  const currentConfig = await loadPersistedUserConfig();
  const nextAdapters = normalizeModelAdapters(currentConfig.modelAdapters);

  if (index < 0 || index >= nextAdapters.length) {
    return {
      ok: false,
      error: "模型配置不存在，无法删除",
    };
  }

  nextAdapters.splice(index, 1);

  return persistConfigPayload(
    {
      ...currentConfig,
      modelAdapters: nextAdapters,
    },
    { modelAdaptersOnly: true },
  );
}

function splitDisplayNameSeed(value) {
  const text = asString(value);
  const match = text.match(/^(.*?)(?:\s*[-+](\d+))?$/);
  if (!match) {
    return { base: text || "模型", number: 0 };
  }
  const base = asString(match[1]) || "模型";
  const number = match[2] ? Number(match[2]) : 0;
  return { base, number: Number.isFinite(number) ? number : 0 };
}

function buildNextDisplayName(existingAdapters, sourceName) {
  const { base } = splitDisplayNameSeed(sourceName);
  let next = 1;
  const taken = new Set(
    normalizeModelAdapters(existingAdapters)
      .map((adapter) => adapter.displayName)
      .filter(Boolean),
  );

  while (taken.has(`${base}-${next}`)) {
    next += 1;
  }
  return `${base}-${next}`;
}

export async function duplicateModelAdapterAt(index) {
  const currentConfig = await loadPersistedUserConfig();
  const nextAdapters = normalizeModelAdapters(currentConfig.modelAdapters);

  if (index < 0 || index >= nextAdapters.length) {
    return {
      ok: false,
      error: "模型配置不存在，无法复制",
    };
  }

  const source = normalizeModelAdapter(nextAdapters[index]);
  const duplicate = {
    ...source,
    id: "",
    displayName: buildNextDisplayName(nextAdapters, source.displayName || source.modelID || "模型"),
  };

  nextAdapters.splice(index + 1, 0, duplicate);

  return persistConfigPayload(
    {
      ...currentConfig,
      modelAdapters: nextAdapters,
    },
    { modelAdaptersOnly: true },
  );
}

export async function syncServiceState() {
  const state = await getProxyState();
  applyProxyState(state);
  return state;
}

export async function syncHomeMetrics() {
  const startedAt = Date.now();
  appState.homeMetricsLoading = true;
  try {
    const summary = await getHomeMetricsSummary();
    applyHomeMetrics(summary);
    return {
      ok: true,
      error: "",
    };
  } catch (error) {
    appState.homeMetricsError = toUserError(error);
    return {
      ok: false,
      error: appState.homeMetricsError,
    };
  } finally {
    const elapsed = Date.now() - startedAt;
    if (elapsed < HOME_METRICS_MIN_LOADING_MS) {
      await delay(HOME_METRICS_MIN_LOADING_MS - elapsed);
    }
    appState.homeMetricsLoading = false;
  }
}

export async function startService() {
  if (appState.serviceBusy) {
    return { ok: false, error: "服务状态更新中，请稍后再试" };
  }
  appState.serviceBusy = true;
  try {
    const saved = await persistUserConfig();
    if (!saved.ok) {
      return saved;
    }
    const state = await startProxyService();
    applyProxyState(state);
    return { ok: true, error: "" };
  } catch (error) {
    await syncServiceState().catch(() => {});
    return { ok: false, error: toUserError(error) };
  } finally {
    appState.serviceBusy = false;
  }
}

export async function stopService() {
  if (appState.serviceBusy) {
    return { ok: false, error: "服务状态更新中，请稍后再试" };
  }
  appState.serviceBusy = true;
  try {
    const state = await stopProxyService();
    applyProxyState(state);
    return { ok: true, error: "" };
  } catch (error) {
    await syncServiceState().catch(() => {});
    return { ok: false, error: toUserError(error) };
  } finally {
    appState.serviceBusy = false;
  }
}

export async function toggleService() {
  if (appState.serviceRunning) {
    return stopService();
  }
  return startService();
}

export async function openLocalLogsDirectory() {
  await openLogsDirectory();
}

export async function openConfigWindow() {
  await openConfig();
}

export async function openModelConfigWindow() {
  await openModelConfig();
}

export async function openModelEditorWindow(index, adapter) {
  const adapterJSON = JSON.stringify(normalizeModelAdapter(adapter));
  await openModelEditor(index, adapterJSON);
}

export async function checkForAppUpdates() {
  await checkForUpdates();
}

export function dismissUpdatePrompt() {
  appState.updatePromptVisible = false;
  appState.updatePromptBusy = false;
}

export async function confirmUpdatePrompt() {
  if (appState.updatePromptKind !== "ready") {
    dismissUpdatePrompt();
    return;
  }
  if (appState.updatePromptBusy) {
    return;
  }
  appState.updatePromptBusy = true;
  try {
    await installReadyUpdate();
  } catch (error) {
    appState.updatePromptBusy = false;
    const message = toUserError(error);
    appState.updateError = message;
    openUpdatePrompt("error", { error: message });
  }
}

export function toUserError(error) {
  const message = extractErrorMessage(error);
  return message || GENERIC_SERVICE_ERROR;
}

export async function bootstrapAppState() {
  try {
    await reloadUserConfig();
  } catch (_error) {
    // keep cached config if loading fails
  }
  await refreshModelAdapterTestResults().catch(() => {});
  try {
    appState.appVersion = await getAppVersion();
  } catch (_error) {
    appState.appVersion = "";
  }
  await syncServiceState().catch(() => {});
  await syncHomeMetrics().catch(() => {});
}
