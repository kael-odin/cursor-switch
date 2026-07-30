<script setup>
import Button from "@/components/ui/Button.vue";
import Input from "@/components/ui/Input.vue";
import ModelAdapterTestCard from "@/components/ModelAdapterTestCard.vue";
import Select from "@/components/ui/Select.vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { getModelEditorContext, fetchProviderModels } from "@/services/clientApi";
import {
  ANTHROPIC_THINKING_EFFORT_DEFAULT,
  appState,
  buildModelAdapterTestRequestHash,
  createEmptyModelAdapter,
  CUSTOM_HEADERS_DEFAULT_JSON,
  EXTRA_PARAMS_DEFAULT_JSON,
  getModelAdapterTestResult,
  getModelAdapterTestResultByID,
  isModelAdapterTestResultStale,
  normalizeModelAdapter,
  OPENAI_ENDPOINT_CHAT_COMPLETIONS,
  OPENAI_ENDPOINT_CUSTOM,
  OPENAI_ENDPOINT_RESPONSES,
  OPENAI_EXTRA_PARAMS_DEFAULT_JSON,
  appendModelAdaptersBatch,
  runModelAdapterTest,
  saveModelAdapterAt,
  toUserError,
  validateModelAdapters,
} from "@/state/appState";
import { Window } from "@wailsio/runtime";
import { computed, onMounted, reactive, ref, watch } from "vue";

const modelTypeTabs = [
  { label: "OpenAI", value: "openai", icon: "icon-[bxl--openai]" },
  { label: "Anthropic", value: "anthropic", icon: "icon-[logos--claude-icon]" },
];

const reasoningEffortOptions = [
  { label: "低", value: "low", icon: "icon-[mdi--head-outline]" },
  { label: "中", value: "medium", icon: "icon-[mdi--head-lightbulb-outline]" },
  { label: "高", value: "high", icon: "icon-[mdi--brain]" },
  { label: "极高", value: "xhigh", icon: "icon-[mdi--head-cog-outline]" },
  { label: "最高", value: "max", icon: "icon-[mdi--brain]" },
];

const anthropicThinkingEffortOptions = [
  { label: "低", value: "low", icon: "icon-[mdi--head-outline]" },
  { label: "中", value: "medium", icon: "icon-[mdi--head-lightbulb-outline]" },
  { label: "高", value: "high", icon: "icon-[mdi--brain]" },
  { label: "极高", value: "xhigh", icon: "icon-[mdi--head-cog-outline]" },
  { label: "Max", value: "max", icon: "icon-[mdi--brain]" },
];

const openAIEndpointOptions = [
  { label: "/v1/responses", value: OPENAI_ENDPOINT_RESPONSES, icon: "icon-[mdi--api]" },
  { label: "/v1/chat/completions", value: OPENAI_ENDPOINT_CHAT_COMPLETIONS, icon: "icon-[mdi--message-text-outline]" },
  { label: "自定义路径(请输入完整请求地址)", value: OPENAI_ENDPOINT_CUSTOM, icon: "icon-[mdi--pencil-outline]" },
];

const roleOptions = [
  { label: "chat — 仅聊天", value: "chat", icon: "icon-[mdi--chat-outline]" },
  { label: "image — 仅生图", value: "image", icon: "icon-[mdi--image-edit-outline]" },
  { label: "both — 聊天 + 生图", value: "both", icon: "icon-[mdi--image-multiple-outline]" },
];

const editorIndex = ref(-1);
const draft = reactive(createEmptyModelAdapter());
const errorMessage = ref("");
const loading = ref(true);
const lastTestAdapterID = ref("");
const localTestFailure = ref("");

function createOptionalPositiveIntegerModel(key) {
  return computed({
    get() {
      return draft[key] > 0 ? String(draft[key]) : "";
    },
    set(value) {
      const text = String(value || "").trim();
      draft[key] = /^\d+$/.test(text) && Number(text) > 0 ? Number(text) : 0;
    },
  });
}

// createIntegerModel 支持负整数的双向绑定（用于 priority 等允许负值的字段）。
function createIntegerModel(key) {
  return computed({
    get() {
      const value = Number(draft[key]);
      return Number.isInteger(value) ? String(value) : "0";
    },
    set(value) {
      const text = String(value || "").trim();
      draft[key] = /^-?\d+$/.test(text) ? Number(text) : 0;
    },
  });
}

const maxCompletionTokensInput = createOptionalPositiveIntegerModel("maxCompletionTokens");
const anthropicMaxTokensInput = createOptionalPositiveIntegerModel("anthropicMaxTokens");
const contextWindowTokensInput = createOptionalPositiveIntegerModel("contextWindowTokens");
const priorityInput = createIntegerModel("priority");
const interfacePlaceholder = computed(() =>
  draft.type === "anthropic" ? "例如：https://api.anthropic.com" : "例如：https://api.openai.com/v1",
);
const currentRequestHash = computed(() => buildModelAdapterTestRequestHash(draft));
const directModelTestResult = computed(() => getModelAdapterTestResult(draft));
const rememberedModelTestResult = computed(() =>
  lastTestAdapterID.value ? getModelAdapterTestResultByID(lastTestAdapterID.value) : null,
);
const activeModelTestResult = computed(() => directModelTestResult.value || rememberedModelTestResult.value);
const modelTestResultStale = computed(() =>
  isModelAdapterTestResultStale(draft, activeModelTestResult.value),
);
const isCurrentConfigTesting = computed(() => directModelTestResult.value?.status === "running");
const modelTestSummary = computed(() => {
  if (localTestFailure.value) {
    return localTestFailure.value;
  }
  return activeModelTestResult.value?.summaryText || "尚未测试";
});

const title = computed(() => (editorIndex.value >= 0 ? "编辑模型配置" : "新增模型配置"));

function ensureOpenAIExtraParamsJSON() {
  if (!String(draft.openAIExtraParamsJSON || "").trim()) {
    draft.openAIExtraParamsJSON = OPENAI_EXTRA_PARAMS_DEFAULT_JSON;
  }
}

function ensureCustomHeadersJSON() {
  if (!String(draft.customHeadersJSON || "").trim()) {
    draft.customHeadersJSON = CUSTOM_HEADERS_DEFAULT_JSON;
  }
}

function ensureAnthropicExtraParamsJSON() {
  if (!String(draft.anthropicExtraParamsJSON || "").trim()) {
    draft.anthropicExtraParamsJSON = EXTRA_PARAMS_DEFAULT_JSON;
  }
}

function ensureAnthropicThinkingEffort() {
  if (!String(draft.anthropicThinkingEffort || "").trim()) {
    draft.anthropicThinkingEffort = ANTHROPIC_THINKING_EFFORT_DEFAULT;
  }
}

const fieldTips = {
  displayName: "仅用于界面展示，便于你区分不同模型。可随便起名，不影响实际请求。",
  providerLabel: "类型是协议选择器，仅 openai/anthropic 两种。这里是使用统计展示的品牌标签，留空则回退类型。比如接 deepseek 走 openai 协议：类型=openai、标签=deepseek，使用统计就归到 deepseek 而非 openai。",
  imageModelID: "生图时调用的模型（如 gpt-image-2），留空则回退 ModelID。GenerateImage 工具用此模型打 {baseURL}/v1/images/generations。同一 adapter 既能 chat（ModelID）又能生图（Image 模型）——比如 gpt-5.6-sol adapter 填 imageModelID=gpt-image-2 即可生图。",
  role: "用途：chat=仅聊天（ModelID 必填）；image=仅生图（Image 模型必填，ModelID 可空——独立生图 adapter，不参与聊天路由）；both=既能聊天又能生图（ModelID 与 Image 模型各自必填）。填错位置生图会失败——生图模型请填到「Image 模型」而非 ModelID。",
  modelID: "请求实际发送给服务端的模型名称，必须和 provider 上一致，例如 gpt-4.1 或 claude-sonnet。点上方「获取模型列表」可直接拉取可选值。",
  baseURL: "模型服务的 API 根地址，通常为兼容 OpenAI 或 Anthropic 的接口入口。例如 https://api.openai.com 或 https://api.anthropic.com。",
  apiKey: "调用该模型服务需要使用的访问密钥（形如 sk-xxx）。仅存在本地，不会上传。",
  contextWindowTokens: "模型一次能「读懂」多少内容的上限（包含你的提问 + 历史 + 模型回复）。常见值：200000=200K，1000000=1M。留空按默认 200K 处理。填错会导致压缩过早或过晚：填小了对话还没长就被压缩，填大了可能超出模型真实上限报错。获取模型列表时能自动反查的就不用手填。",
  reasoningEffort: "推理强度仅对部分支持 reasoning_effort 的模型生效，并不是所有模型都支持。越高通常回答越稳，但也更慢、更费 token。日常用 medium，难题用 high。",
  maxCompletionTokens: "模型单次回复最多能「写」多少内容的上限。常见值 65536~131072。留空按默认 131072（128K）处理。注意这是「思考 + 正文」合计的硬顶，设太大可能撞模型上限被截断。",
  openAIEndpoint: "选择接口协议端点。选“自定义路径”时，请在接口地址栏填写完整请求地址（含 /chat/completions 或 /responses 路径后缀），系统会根据末段自动判断协议形态。",
  openAIExtraParams: "开启后会把 JSON 对象覆盖到 OpenAI 请求体。同名字段以这里为准。OpenAI service_tier 支持 auto、default、flex、scale、priority。",
  customHeaders: "开启后会把 JSON 对象覆盖到最终请求头。同名请求头以这里为准，值必须是字符串。",
  anthropicExtraParams: "开启后会把 JSON 对象覆盖到 Anthropic 请求体。同名字段以这里为准。",
  anthropicMaxTokens: "Anthropic 模型单次回复允许生成的最大 Token 数。留空按默认 131072（128K）处理。Opus / Sonnet 系列支持 128K，Haiku 为 64K。",
  anthropicThinkingEffort: "Anthropic adaptive thinking 的思考强度。请求会固定使用新版 thinking.type=adaptive。越高模型「想」得越久，回答质量通常更好但更慢。",
  tooltipData: "模型列表 hover 时显示的备注说明，写给自己看的备忘。",
  priority: "故障转移候选链的排序优先级：数字小的优先。同 modelID 的多个 enabled 适配器按 Priority 升序组成主→备候选链，主候选失败（连接错误/5xx/429/流超时）且尚未输出内容时自动切到下一个。默认 0。",
  enabled: "关闭后该适配器保留配置但不参与路由（不进候选链）。可用于临时摘除某个 provider 而不删除其配置。",
};

// 一键获取模型列表：根据已填的 type/baseURL/apiKey 调 provider 的 /v1/models。
// 支持多选批量添加：以当前 draft 为模板（继承 baseURL/apiKey/endpoint/extra params/customHeaders），
// 仅替换每条 modelID + displayName，一次性追加到模型配置。
const fetchModelsVisible = ref(false);
const fetchModelsLoading = ref(false);
const fetchModelsError = ref("");
const fetchedModels = ref([]);
const fetchModelsSourceURL = ref("");
const fetchModelsQuery = ref("");
const fetchSelectedIDs = ref(new Set());
const fetchBatchSaving = ref(false);
const fetchBatchResult = ref(null); // { added:[], skipped:[] }

const filteredFetchedModels = computed(() => {
  const q = fetchModelsQuery.value.trim().toLowerCase();
  if (!q) return fetchedModels.value;
  return fetchedModels.value.filter(
    (m) =>
      String(m.id || "").toLowerCase().includes(q) ||
      String(m.displayName || "").toLowerCase().includes(q),
  );
});

const fetchSelectedCount = computed(() => fetchSelectedIDs.value.size);

function toggleFetchSelect(id) {
  const next = new Set(fetchSelectedIDs.value);
  if (next.has(id)) {
    next.delete(id);
  } else {
    next.add(id);
  }
  fetchSelectedIDs.value = next;
}

function selectAllFiltered() {
  const next = new Set(fetchSelectedIDs.value);
  for (const m of filteredFetchedModels.value) {
    next.add(m.id);
  }
  fetchSelectedIDs.value = next;
}

function clearFetchSelection() {
  fetchSelectedIDs.value = new Set();
}

async function openFetchModels() {
  // 只要求 type/baseURL/apiKey；modelID 可空（拉列表发生在选模型之前）
  const missing = [];
  if (!String(draft.type || "").trim()) missing.push("provider 类型");
  if (!String(draft.baseURL || "").trim()) missing.push("接口地址");
  if (!String(draft.apiKey || "").trim()) missing.push("访问密钥");
  fetchModelsVisible.value = true;
  fetchBatchResult.value = null;
  fetchSelectedIDs.value = new Set();
  fetchModelsQuery.value = "";
  fetchedModels.value = [];
  if (missing.length) {
    fetchModelsError.value = `请先填写：${missing.join("、")}`;
    return;
  }
  fetchModelsLoading.value = true;
  fetchModelsError.value = "";
  try {
    const payload = await fetchProviderModels(normalizeModelAdapter(draft));
    fetchedModels.value = Array.isArray(payload?.models) ? payload.models : [];
    fetchModelsSourceURL.value = String(payload?.sourceURL || "");
    if (!fetchedModels.value.length) {
      fetchModelsError.value = "provider 返回的模型列表为空";
    }
  } catch (e) {
    fetchModelsError.value = String(e?.message || e || "获取模型列表失败");
  } finally {
    fetchModelsLoading.value = false;
  }
}

// 单选即用：点模型名直接填回当前 draft（保持原「选一个就编辑」的快速路径）
function pickFetchedModel(model) {
  if (!model) return;
  draft.modelID = model.id;
  if (!String(draft.displayName || "").trim() || draft.displayName === draft.modelID) {
    draft.displayName = model.displayName || model.id;
  }
  // 若反查到上下文窗口且当前 draft 未显式设置，一并填回。
  const cw = Number(model?.contextWindowTokens) || 0;
  if (cw > 0 && !Number(draft.contextWindowTokens)) {
    draft.contextWindowTokens = cw;
  }
  fetchModelsVisible.value = false;
}

// 把 token 数格式化成人类可读的「xxxK」徽标，方便在模型列表里一眼看出上下文窗口大小。
function fmtContextBadge(tokens) {
  const v = Number(tokens) || 0;
  if (v <= 0) return "";
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(v % 1_000_000 ? 1 : 0)}M`;
  if (v >= 1000) return `${Math.round(v / 1000)}K`;
  return String(v);
}

// 批量添加：以当前 draft 为模板，把选中的模型一次性追加到配置
async function addSelectedBatch() {
  if (fetchSelectedCount.value === 0 || fetchBatchSaving.value) return;
  fetchBatchSaving.value = true;
  fetchBatchResult.value = null;
  try {
    const selected = fetchedModels.value.filter((m) => fetchSelectedIDs.value.has(m.id));
    const result = await appendModelAdaptersBatch(draft, selected);
    fetchBatchResult.value = {
      added: result.added || [],
      skipped: result.skipped || [],
      ok: result.ok,
      error: result.error || "",
    };
    if (result.ok) {
      // 成功追加：清空选择，保留面板让用户看到结果，可继续选或关闭
      fetchSelectedIDs.value = new Set();
    }
  } catch (e) {
    fetchBatchResult.value = {
      added: [],
      skipped: [],
      ok: false,
      error: String(e?.message || e || "批量添加失败"),
    };
  } finally {
    fetchBatchSaving.value = false;
  }
}

async function closeFetchModelsAfterBatch() {
  const r = fetchBatchResult.value;
  if (r && r.ok && r.added.length && !r.skipped.length) {
    // 全部成功且无跳过：关闭编辑器回列表
    await Window.Close();
    return;
  }
  fetchModelsVisible.value = false;
}

function closeFetchModels() {
  fetchModelsVisible.value = false;
}

async function loadContext() {
  try {
    const ctx = await getModelEditorContext();
    editorIndex.value = typeof ctx.index === "number" ? ctx.index : -1;
    const parsed = JSON.parse(ctx.adapterJSON || "{}");
    Object.assign(draft, normalizeModelAdapter(parsed));
    if (!draft.type) {
      draft.type = "openai";
    }
  } catch (_error) {
    Object.assign(draft, createEmptyModelAdapter());
    draft.type = "openai";
  } finally {
    loading.value = false;
  }
}

async function persistDraft() {
  const adapter = normalizeModelAdapter(draft);

  const singleCheck = validateModelAdapters([adapter]);
  if (singleCheck) {
    errorMessage.value = singleCheck;
    return { ok: false, error: singleCheck, adapter: null };
  }

  const result = await saveModelAdapterAt(editorIndex.value, adapter);
  if (!result.ok) {
    errorMessage.value = result.error;
    return { ok: false, error: result.error, adapter: null };
  }

  if (typeof result.index === "number") {
    editorIndex.value = result.index;
  }
  if (result.adapter) {
    Object.assign(draft, normalizeModelAdapter(result.adapter));
  }
  errorMessage.value = "";
  return {
    ok: true,
    error: "",
    adapter: result.adapter ? normalizeModelAdapter(result.adapter) : normalizeModelAdapter(draft),
  };
}

async function handleSave() {
  const result = await persistDraft();
  if (!result.ok) {
    return;
  }
  await Window.Close();
}

async function handleCancel() {
  await Window.Close();
}

function handleModelTypeChange(type) {
  draft.type = type;
  if (type === "openai" && !draft.openAIEndpoint) {
    draft.openAIEndpoint = OPENAI_ENDPOINT_RESPONSES;
  } else if (type === "anthropic") {
    ensureAnthropicThinkingEffort();
  }
}

async function handleTest() {
  localTestFailure.value = "";
  try {
    const saved = await persistDraft();
    if (!saved.ok || !saved.adapter) {
      return;
    }
    const result = await runModelAdapterTest(saved.adapter);
    if (result?.adapterID) {
      lastTestAdapterID.value = result.adapterID;
    }
  } catch (error) {
    const latest = getModelAdapterTestResult(draft);
    if (latest?.adapterID) {
      lastTestAdapterID.value = latest.adapterID;
      return;
    }
    localTestFailure.value = toUserError(error);
  }
}

watch(
  directModelTestResult,
  (result) => {
    if (!result?.adapterID) {
      return;
    }
    lastTestAdapterID.value = result.adapterID;
    if (result.status !== "running") {
      localTestFailure.value = "";
    }
  },
  { immediate: true },
);

watch(currentRequestHash, () => {
  localTestFailure.value = "";
});

watch(
  () => draft.openAIExtraParamsEnabled,
  (enabled) => {
    if (enabled) {
      ensureOpenAIExtraParamsJSON();
    }
  },
);

watch(
  () => draft.customHeadersEnabled,
  (enabled) => {
    if (enabled) {
      ensureCustomHeadersJSON();
    }
  },
);

watch(
  () => draft.anthropicExtraParamsEnabled,
  (enabled) => {
    if (enabled) {
      ensureAnthropicExtraParamsJSON();
    }
  },
);

onMounted(async () => {
  await loadContext();
});
</script>

<template>
  <div class="flex h-full flex-col text-[#e5e5e5]">
    <div class="flex shrink-0 items-center justify-between px-4 pb-2">
      <h2 class="text-base font-medium text-white">{{ title }}</h2>
      <div class="flex items-center gap-2">
        <Button variant="default" @click="handleCancel">取消</Button>
        <Button variant="default" :disabled="isCurrentConfigTesting || appState.configSaving" @click="handleTest">
          {{ isCurrentConfigTesting ? "测试中..." : "保存并测试" }}
        </Button>
        <Button variant="primary" :disabled="appState.configSaving" @click="handleSave">
          {{ appState.configSaving ? "保存中..." : "保存" }}
        </Button>
      </div>
    </div>

    <div v-if="loading" class="flex flex-1 items-center justify-center text-sm text-[#a3a3a3]">
      加载中...
    </div>

    <div v-else class="flex-1 overflow-y-auto min-h-0 px-4 pb-4">
      <div class="flex flex-col gap-4">
        <div class="center-row gap-2">
          <button
            v-for="tab in modelTypeTabs"
            :key="tab.value"
            type="button"
            class="center-row gap-2 rounded-[8px] border px-3 py-2 text-sm transition-colors duration-150"
            :class="draft.type === tab.value
              ? 'border-[#1ca35a] bg-[#123322] text-white'
              : 'border-[#343434] bg-[#252525] text-[#a3a3a3] hover:border-[#4a4a4a] hover:text-[#e5e5e5]'"
            @click="handleModelTypeChange(tab.value)"
          >
            <span :class="[tab.icon, 'text-[16px]']"></span>
            <span>{{ tab.label }}</span>
          </button>
        </div>

        <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.providerLabel" />
              <span>Provider 标签</span>
            </span>
            <input
              v-model="draft.providerLabel"
              type="text"
              placeholder="deepseek / qwen / glm（留空则用类型）"
              class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
            />
          </label>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.displayName" />
              <span>显示名称</span>
            </span>
            <input
              v-model="draft.displayName"
              type="text"
              placeholder="例如：OpenAI - GPT-4.1"
              class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
            />
          </label>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.modelID" />
              <span>模型标识</span>
            </span>
            <div class="flex gap-2">
              <input
                v-model="draft.modelID"
                type="text"
                placeholder="例如：gpt-4.1"
                class="h-9 flex-1 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
              />
              <Button
                variant="default"
                class="!h-9 shrink-0 whitespace-nowrap"
                :disabled="fetchModelsLoading"
                @click="openFetchModels"
              >
                {{ fetchModelsLoading ? "获取中…" : "获取模型列表" }}
              </Button>
            </div>
          </label>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.imageModelID" />
              <span>Image 模型</span>
            </span>
            <input
              v-model="draft.imageModelID"
              type="text"
              placeholder="gpt-image-2（留空则用 ModelID）"
              class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
            />
          </label>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.role" />
              <span>用途 (Role)</span>
            </span>
            <Select
              v-model="draft.role"
              :options="roleOptions"
            />
          </label>

          <div
            v-if="fetchModelsVisible"
            class="col-span-full mb-2 rounded-[6px] border border-[#3f3f3f] bg-[#1c1c1c] p-3"
          >
            <div class="mb-2 flex items-center justify-between gap-2">
              <span class="text-sm text-[#d4d4d4]">
                {{ fetchModelsLoading ? "正在拉取模型列表…" : `模型列表（${fetchedModels.length}）` }}
              </span>
              <div class="flex items-center gap-3">
                <span v-if="fetchSelectedCount > 0" class="text-xs text-[#10AD5D]">
                  已选 {{ fetchSelectedCount }}
                </span>
                <button
                  type="button"
                  class="text-[#8f8f8f] hover:text-[#e5e5e5]"
                  @click="closeFetchModels"
                >
                  关闭
                </button>
              </div>
            </div>
            <p v-if="fetchModelsError" class="mb-2 text-xs text-red-400">{{ fetchModelsError }}</p>
            <p
              v-else-if="fetchModelsSourceURL"
              class="mb-2 truncate text-xs text-[#737373]"
              :title="fetchModelsSourceURL"
            >
              来源：{{ fetchModelsSourceURL }}
            </p>
            <p v-if="!fetchModelsLoading && fetchedModels.length" class="mb-2 text-xs text-[#737373]">
              列表里带「窗口 xxxK」标记的是已自动反查到上下文窗口的模型，批量添加时会一并填入；
              没有标记的需要你后续在下方「上下文窗口」手填（不填按默认 200K 处理）。
            </p>

            <div
              v-if="fetchBatchResult"
              class="mb-2 rounded-[4px] border px-3 py-2 text-xs"
              :class="fetchBatchResult.ok ? 'border-[#10AD5D]/40 bg-[#10AD5D]/10 text-[#10AD5D]' : 'border-red-500/40 bg-red-500/10 text-red-400'"
            >
              <template v-if="fetchBatchResult.ok">
                <p>已添加 {{ fetchBatchResult.added.length }} 个模型</p>
                <p v-if="fetchBatchResult.skipped.length" class="text-[#8f8f8f]">
                  跳过（已存在）{{ fetchBatchResult.skipped.length }} 个：{{ fetchBatchResult.skipped.join("、") }}
                </p>
              </template>
              <p v-else>{{ fetchBatchResult.error || "批量添加失败" }}</p>
            </div>

            <div
              v-if="!fetchModelsLoading && fetchedModels.length"
              class="mb-2 flex items-center gap-3"
            >
              <input
                v-model="fetchModelsQuery"
                type="text"
                placeholder="搜索模型 id 或显示名…"
                class="h-8 flex-1 rounded-[4px] border border-[#3f3f3f] bg-[#232323] px-2 text-xs text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
              />
              <button
                type="button"
                class="text-xs text-[#8f8f8f] hover:text-[#e5e5e5]"
                @click="selectAllFiltered"
              >
                全选当前
              </button>
              <button
                type="button"
                class="text-xs text-[#8f8f8f] hover:text-[#e5e5e5]"
                @click="clearFetchSelection"
              >
                清空选择
              </button>
            </div>

            <div
              v-if="!fetchModelsLoading && filteredFetchedModels.length"
              class="max-h-56 overflow-y-auto rounded-[4px] border border-[#2a2a2a]"
            >
              <label
                v-for="m in filteredFetchedModels"
                :key="m.id"
                class="flex cursor-pointer items-center gap-3 border-b border-[#2a2a2a] px-3 py-2 text-xs text-[#d4d4d4] last:border-b-0 hover:bg-[#232323]"
                :class="fetchSelectedIDs.has(m.id) ? 'bg-[#1a3a2a]' : ''"
              >
                <input
                  type="checkbox"
                  class="h-3.5 w-3.5 accent-[#10AD5D]"
                  :checked="fetchSelectedIDs.has(m.id)"
                  @change="toggleFetchSelect(m.id)"
                />
                <span class="flex-1 font-mono">{{ m.id }}</span>
                <span v-if="m.displayName && m.displayName !== m.id" class="truncate text-[#8f8f8f]">{{ m.displayName }}</span>
                <span
                  v-if="fmtContextBadge(m.contextWindowTokens)"
                  class="ml-1 shrink-0 rounded-[3px] border border-[#10AD5D]/30 bg-[#10AD5D]/10 px-1.5 py-0.5 text-[10px] text-[#10AD5D]"
                  :title="`上下文窗口 ${m.contextWindowTokens} tokens（自动反查，可手填覆盖）`"
                >
                  窗口 {{ fmtContextBadge(m.contextWindowTokens) }}
                </span>
                <button
                  type="button"
                  class="ml-2 text-[10px] text-[#737373] hover:text-[#10AD5D]"
                  title="仅填回当前模型，不批量添加"
                  @click.prevent.stop="pickFetchedModel(m)"
                >
                  仅填回
                </button>
              </label>
            </div>

            <div
              v-if="!fetchModelsLoading && filteredFetchedModels.length"
              class="mt-2 flex items-center justify-between gap-2"
            >
              <span class="text-xs text-[#737373]">
                勾选后批量追加，继承当前表单的接口地址/密钥/端点/自定义参数
              </span>
              <div class="flex gap-2">
                <Button
                  v-if="fetchBatchResult && fetchBatchResult.ok && fetchBatchResult.added.length"
                  variant="default"
                  :disabled="fetchBatchSaving"
                  @click="closeFetchModelsAfterBatch"
                >
                  完成
                </Button>
                <Button
                  variant="primary"
                  :disabled="fetchSelectedCount === 0 || fetchBatchSaving"
                  @click="addSelectedBatch"
                >
                  {{ fetchBatchSaving ? "添加中…" : `批量添加 ${fetchSelectedCount > 0 ? fetchSelectedCount : ""}` }}
                </Button>
              </div>
            </div>
          </div>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.apiKey" />
              <span>访问密钥</span>
            </span>
            <Input
              v-model="draft.apiKey"
              type="password"
              allow-visibility-toggle
              placeholder="例如：sk-xxxxxx"
              autocomplete="off"
            />
          </label>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.baseURL" />
              <span>接口地址</span>
            </span>
            <input
              v-model="draft.baseURL"
              type="text"
              :placeholder="interfacePlaceholder"
              class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
            />
          </label>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.contextWindowTokens" />
              <span>上下文窗口</span>
            </span>
            <input
              v-model="contextWindowTokensInput"
              type="text"
              inputmode="numeric"
              placeholder="例如：200000（留空用默认值）"
              class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
            />
          </label>

          <label v-if="draft.type === 'openai'" class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.reasoningEffort" />
              <span>推理强度</span>
            </span>
            <Select
              v-model="draft.reasoningEffort"
              :options="reasoningEffortOptions"
            />
          </label>

          <label v-if="draft.type === 'anthropic'" class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.anthropicMaxTokens" />
              <span>最大输出 Token</span>
            </span>
            <input
              v-model="anthropicMaxTokensInput"
              type="text"
              inputmode="numeric"
              placeholder="例如：65536（留空用默认值）"
              class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
            />
          </label>

          <label v-if="draft.type === 'anthropic'" class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.anthropicThinkingEffort" />
              <span>思考强度</span>
            </span>
            <Select
              v-model="draft.anthropicThinkingEffort"
              :options="anthropicThinkingEffortOptions"
            />
          </label>

        </div>

        <div v-if="draft.type === 'openai'" class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.maxCompletionTokens" />
              <span>最大输出 Token</span>
            </span>
            <input
              v-model="maxCompletionTokensInput"
              type="text"
              inputmode="numeric"
              placeholder="例如：65536（留空用默认值）"
              class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
            />
          </label>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.openAIEndpoint" />
              <span>接口端点</span>
            </span>
            <Select
              v-model="draft.openAIEndpoint"
              :options="openAIEndpointOptions"
            />
          </label>
        </div>

        <div v-if="draft.type === 'openai'" class="rounded-[8px] border border-[#343434] bg-[#252525] p-3">
          <div class="flex items-center justify-between gap-3">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.openAIExtraParams" />
              <span>额外参数 JSON</span>
            </span>
            <label class="center-row gap-2 text-xs text-[#d4d4d4]">
              <input
                v-model="draft.openAIExtraParamsEnabled"
                type="checkbox"
                class="size-4 accent-[#10AD5D]"
              />
              <span>启用</span>
            </label>
          </div>
          <textarea
            v-if="draft.openAIExtraParamsEnabled"
            v-model="draft.openAIExtraParamsJSON"
            rows="5"
            spellcheck="false"
            class="mt-3 min-h-[120px] w-full resize-none rounded-[6px] border border-[#3f3f3f] bg-[#1f1f1f] px-3 py-2 font-mono text-xs text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
          />
        </div>

        <div v-if="draft.type === 'anthropic'" class="rounded-[8px] border border-[#343434] bg-[#252525] p-3">
          <div class="flex items-center justify-between gap-3">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.anthropicExtraParams" />
              <span>Anthropic 额外参数 JSON</span>
            </span>
            <label class="center-row gap-2 text-xs text-[#d4d4d4]">
              <input
                v-model="draft.anthropicExtraParamsEnabled"
                type="checkbox"
                class="size-4 accent-[#10AD5D]"
              />
              <span>启用</span>
            </label>
          </div>
          <textarea
            v-if="draft.anthropicExtraParamsEnabled"
            v-model="draft.anthropicExtraParamsJSON"
            rows="5"
            spellcheck="false"
            class="mt-3 min-h-[120px] w-full resize-none rounded-[6px] border border-[#3f3f3f] bg-[#1f1f1f] px-3 py-2 font-mono text-xs text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
          />
        </div>

        <div class="rounded-[8px] border border-[#343434] bg-[#252525] p-3">
          <div class="flex items-center justify-between gap-3">
            <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
              <Tooltip :content="fieldTips.customHeaders" />
              <span>自定义请求头 JSON</span>
            </span>
            <label class="center-row gap-2 text-xs text-[#d4d4d4]">
              <input
                v-model="draft.customHeadersEnabled"
                type="checkbox"
                class="size-4 accent-[#10AD5D]"
              />
              <span>启用</span>
            </label>
          </div>
          <textarea
            v-if="draft.customHeadersEnabled"
            v-model="draft.customHeadersJSON"
            rows="5"
            spellcheck="false"
            class="mt-3 min-h-[120px] w-full resize-none rounded-[6px] border border-[#3f3f3f] bg-[#1f1f1f] px-3 py-2 font-mono text-xs text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
          />
        </div>

        <div class="rounded-[8px] border border-[#343434] bg-[#252525] p-3">
          <div class="mb-2 text-xs text-[#a1a1a1]">
            故障转移候选链：同 modelID 的多个启用适配器按优先级数字升序组成主→备链路，主候选在输出内容前失败（连接错误 / 5xx / 429 / 流超时）时自动切到下一个。
          </div>
          <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
            <label class="flex flex-col gap-1">
              <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
                <Tooltip :content="fieldTips.priority" />
                <span>优先级</span>
              </span>
              <input
                v-model="priorityInput"
                type="text"
                inputmode="numeric"
                placeholder="0（数字小的优先）"
                class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
              />
            </label>
          </div>
          <label class="center-row mt-3 gap-2 text-sm text-[#d4d4d4]">
            <input
              v-model="draft.enabled"
              type="checkbox"
              class="size-4 accent-[#10AD5D]"
            />
            <Tooltip :content="fieldTips.enabled" />
            <span>启用（关闭则保留配置但不参与路由）</span>
          </label>
        </div>

        <label class="flex flex-col gap-1">
          <span class="center-row justify-start gap-1.5 text-sm text-[#d4d4d4]">
            <Tooltip :content="fieldTips.tooltipData" />
            <span>备注</span>
          </span>
          <textarea
            v-model="draft.tooltipData"
            rows="3"
            placeholder="例如：用于日常代码补全与问答"
            class="min-h-[96px] resize-none rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 py-2 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
          />
        </label>

        <ModelAdapterTestCard
          :result="localTestFailure ? { status: 'error', error: '测试失败', summaryText: '测试失败', rawResponse: modelTestSummary } : activeModelTestResult"
          :stale="modelTestResultStale"
          :show-metrics="true"
        />

        <div
          v-if="errorMessage"
          class="rounded-[8px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]"
        >
          {{ errorMessage }}
        </div>
      </div>
    </div>
  </div>
</template>
