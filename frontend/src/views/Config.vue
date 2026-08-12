<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Input from "@/components/ui/Input.vue";
import LocaleSelect from "@/components/LocaleSelect.vue";
import Select from "@/components/ui/Select.vue";
import Switch from "@/components/ui/Switch.vue";
import { showModal } from "@/composables/useModal";
import {
  appState,
  openModelConfigWindow,
  persistUserConfig,
  reloadUserConfig,
  ROUTE_MODE_OPTIONS,
  PER_NAMESPACE_ROUTES,
  WEB_SEARCH_PROVIDER_OPTIONS,
  savePerNamespaceRoute,
  saveRoutingMode,
  saveTabServerBaseURL,
  saveTabUseCursorCredentials,
  saveWebSearchProvider,
  saveWebSearchAPIKey,
  saveWebFetchHostAllowlist,
  toUserError,
} from "@/state/appState";
import { computed, onMounted, ref } from "vue";
import { getCursorAccountStatus, testWebTools } from "@/services/clientApi";

const routeModeOptions = ROUTE_MODE_OPTIONS;
const tabServerSaving = ref(false);
const tabCredSaving = ref(false);

// per-namespace 路由覆盖的可选项：auto = 跟随全局（不覆盖）。
const NAMESPACE_MODE_OPTIONS = [
  { label: "跟随全局", value: "auto" },
  { label: "本地 byok", value: "local" },
  { label: "直连 Cursor", value: "upstream" },
];

const namespaceSaving = ref("");

// 某路由当前选中的模式：覆盖表里有就用其值，否则 auto（跟随全局）。
function namespaceMode(routeName) {
  const v = appState.perNamespace?.[routeName];
  return v === "local" || v === "upstream" ? v : "auto";
}

// 当前全局模式的人类可读描述，用于说明「跟随全局」实际值。
const globalModeLabel = computed(() => {
  if (appState.routingMode === "upstream") return "直连 Cursor";
  return "本地 byok";
});

const routeModeSaving = ref(false);

async function handleSaveRoutingMode(nextMode) {
  // P1-7：此前运行模式 Select 仅 v-model 改状态无 @change 持久化，
  // 用户切换后不点"保存配置"直接关窗会丢失。此处立即持久化，
  // 与 Home.vue 直连模式 Switch 的立即保存行为对齐。
  const previous = appState.routingMode;
  appState.routingMode = nextMode;
  routeModeSaving.value = true;
  try {
    const result = await saveRoutingMode(nextMode);
    if (!result.ok) {
      appState.routingMode = previous;
      await showActionError("保存失败", result.error);
    }
  } catch (error) {
    appState.routingMode = previous;
    await showActionError("保存失败", toUserError(error));
  } finally {
    routeModeSaving.value = false;
  }
}

async function handleSaveNamespaceRoute(route, nextMode) {
  namespaceSaving.value = route.name;
  try {
    const result = await savePerNamespaceRoute(route.name, nextMode);
    if (!result.ok) {
      await showActionError("保存失败", result.error);
      return;
    }
  } finally {
    namespaceSaving.value = "";
  }
}

async function showActionError(title, error) {
  await showModal({
    title,
    content: String(error || "服务错误").trim() || "服务错误",
  });
}

async function handleSaveConfig() {
  const result = await persistUserConfig();
  if (!result.ok) {
    await showActionError("保存失败", result.error);
    return;
  }
  await showModal({
    title: "提示",
    content: "本地配置已保存",
  });
}

async function handleSaveTabServerBaseURL() {
  tabServerSaving.value = true;
  try {
    const result = await saveTabServerBaseURL(appState.tabServerBaseURL);
    if (!result.ok) {
      await showActionError("保存失败", result.error);
      return;
    }
    await showModal({
      title: "提示",
      content: "Tab 服务地址已保存",
    });
  } finally {
    tabServerSaving.value = false;
  }
}

// Tab 补全「留空时带本人 Cursor 凭证走官方」开关。仅留空（走官方 api2.cursor.sh）时生效；
// 填了自建 tab server 时由 server 端账号回源，此开关无意义。开启后补全消耗本人 Cursor 账号额度。
async function handleSaveTabUseCursorCredentials(next) {
  tabCredSaving.value = true;
  const previous = appState.tabUseCursorCredentials;
  appState.tabUseCursorCredentials = !!next;
  try {
    const result = await saveTabUseCursorCredentials(next);
    if (!result.ok) {
      appState.tabUseCursorCredentials = previous;
      await showActionError("保存失败", result.error);
    }
  } catch (error) {
    appState.tabUseCursorCredentials = previous;
    await showActionError("保存失败", toUserError(error));
  } finally {
    tabCredSaving.value = false;
  }
}

// Web 工具探活：对 Tab 服务 / WebSearch / WebFetch 做一次连通性测试，
// 复用 testModelAdapter 的「测的是表单当前值」语义（未必已保存）。结果内联显示。
const tabServerTesting = ref(false);
const tabServerTestResult = ref("");
const webSearchTesting = ref(false);
const webSearchTestResult = ref("");
const webFetchTesting = ref(false);
const webFetchTestResult = ref("");

function resultText(result) {
  if (!result) return "";
  return result.status === "success" ? `✓ ${result.detail}` : `✗ ${result.detail}`;
}

async function handleTestTabServer() {
  tabServerTesting.value = true;
  tabServerTestResult.value = "";
  try {
    tabServerTestResult.value = resultText(
      await testWebTools({ kind: "tabserver", tabServerBaseURL: appState.tabServerBaseURL }),
    );
  } catch (error) {
    tabServerTestResult.value = `✗ ${toUserError(error)}`;
  } finally {
    tabServerTesting.value = false;
  }
}

async function handleTestWebSearch() {
  webSearchTesting.value = true;
  webSearchTestResult.value = "";
  try {
    webSearchTestResult.value = resultText(
      await testWebTools({
        kind: "websearch",
        webSearchProvider: appState.webSearchProvider,
        webSearchAPIKey: appState.webSearchAPIKey,
      }),
    );
  } catch (error) {
    webSearchTestResult.value = `✗ ${toUserError(error)}`;
  } finally {
    webSearchTesting.value = false;
  }
}

async function handleTestWebFetch() {
  webFetchTesting.value = true;
  webFetchTestResult.value = "";
  try {
    webFetchTestResult.value = resultText(await testWebTools({ kind: "webfetch" }));
  } catch (error) {
    webFetchTestResult.value = `✗ ${toUserError(error)}`;
  } finally {
    webFetchTesting.value = false;
  }
}

async function handleOpenModelConfig() {
  try {
    await openModelConfigWindow();
  } catch (error) {
    await showActionError("打开失败", toUserError(error));
  }
}

// Tab 补全依赖官方 Cursor 账号（是 BYOK 的例外能力）。
// 探测 Cursor 客户端是否已登录账号；未登录则 Tab 补全/Git 消息等不可用，
// 前端明确告警而非静默失败。直连模式（tabServerBaseURL 空 = 走官方上游）依赖此账号。
const cursorAccountStatus = ref({ accountPresent: false, email: "", dbExists: false, probeError: "" });
const cursorAccountLoading = ref(false);

async function refreshCursorAccountStatus() {
  cursorAccountLoading.value = true;
  try {
    const status = await getCursorAccountStatus();
    cursorAccountStatus.value = status || cursorAccountStatus.value;
  } catch (error) {
    // 探针失败不阻断页面，保留上次状态并把错误塞进 probeError 字段。
    cursorAccountStatus.value = { ...cursorAccountStatus.value, probeError: toUserError(error) };
  } finally {
    cursorAccountLoading.value = false;
  }
}

// 仅当直连官方上游（tabServerBaseURL 空）时，Tab 补全依赖本机 Cursor 账号；
// 配了自建 cursor-tab-server 则由该 server 自带账号，不在此探测。
const tabDependsOnLocalCursorAccount = computed(() => !String(appState.tabServerBaseURL || "").trim());

// 审计「行为偏离-3」：WebSearch 多 provider + WebFetch host 白名单。
const webSearchProviderOptions = WEB_SEARCH_PROVIDER_OPTIONS;
const webSearchSaving = ref(false);
const webSearchKeySaving = ref(false);
const webFetchAllowlistSaving = ref(false);
// 白名单输入框：用逗号/换行/空格分隔的字符串编辑，提交时拆成数组。
const webFetchAllowlistDraft = ref("");

function syncWebFetchAllowlistDraft() {
  webFetchAllowlistDraft.value = (appState.webFetchHostAllowlist || []).join("\n");
}

// 选了需 key 的 provider 但 key 空 → 前端告警（与后端缺 key 错误对齐，不静默失败）。
// bing 免 key 可走 HTML 抓取，不在告警之列（仅 serper/tavily 必填 key）。
const webSearchNeedsKey = computed(() => {
  const p = String(appState.webSearchProvider || "").toLowerCase();
  return (p === "serper" || p === "tavily") && !String(appState.webSearchAPIKey || "").trim();
});

async function handleSaveWebSearchProvider(nextProvider) {
  const previous = appState.webSearchProvider;
  appState.webSearchProvider = nextProvider;
  webSearchSaving.value = true;
  try {
    const result = await saveWebSearchProvider(nextProvider);
    if (!result.ok) {
      appState.webSearchProvider = previous;
      await showActionError("保存失败", result.error);
    }
  } catch (error) {
    appState.webSearchProvider = previous;
    await showActionError("保存失败", toUserError(error));
  } finally {
    webSearchSaving.value = false;
  }
}

async function handleSaveWebSearchAPIKey() {
  webSearchKeySaving.value = true;
  try {
    const result = await saveWebSearchAPIKey(appState.webSearchAPIKey);
    if (!result.ok) {
      await showActionError("保存失败", result.error);
    }
  } catch (error) {
    await showActionError("保存失败", toUserError(error));
  } finally {
    webSearchKeySaving.value = false;
  }
}

async function handleSaveWebFetchAllowlist() {
  // 把文本框按逗号/换行/空白拆成 host 数组，去空白+去重交给 normalizer。
  const raw = String(webFetchAllowlistDraft.value || "")
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
  webFetchAllowlistSaving.value = true;
  try {
    const result = await saveWebFetchHostAllowlist(raw);
    if (result.ok) {
      syncWebFetchAllowlistDraft();
    } else {
      await showActionError("保存失败", result.error);
    }
  } catch (error) {
    await showActionError("保存失败", toUserError(error));
  } finally {
    webFetchAllowlistSaving.value = false;
  }
}

onMounted(async () => {
  await reloadUserConfig().catch(() => {});
  syncWebFetchAllowlistDraft();
  await refreshCursorAccountStatus();
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-y-auto p-4 pt-0 text-[#e5e5e5]">
    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">本地配置</h2>
          <div class="text-sm text-[#a3a3a3]">
            可配置运行模式和模型渠道；运行日志位于 <code>~/.cursor-local-assistant-v2/logs/</code>
          </div>
        </div>
        <Button variant="primary" :disabled="appState.configSaving" @click="handleSaveConfig">
          {{ appState.configSaving ? "保存中..." : "保存配置" }}
        </Button>
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">运行模式</h2>
          <div class="text-sm text-[#a3a3a3]">
            控制白名单主链路请求走本地服务，还是回到原始 Cursor 上游地址
          </div>
        </div>
        <div class="w-[220px] max-w-full">
          <Select
            :model-value="appState.routingMode"
            :options="routeModeOptions"
            placeholder="选择模式"
            @update:model-value="handleSaveRoutingMode"
          />
        </div>
      </div>
    </Card>

    <Card>
      <div class="flex flex-col gap-3">
        <div class="flex items-center justify-between gap-4">
          <div>
            <div class="flex items-center gap-2">
              <h2 class="text-base font-medium text-white">Tab 补全服务地址</h2>
              <span class="rounded bg-amber-500/15 px-1.5 py-0.5 text-xs font-medium text-amber-300 ring-1 ring-amber-500/30">依赖官方账号</span>
            </div>
            <div class="text-sm text-[#a3a3a3]">
              控制 Tab 代码补全 / Git Commit / 分支名生成等流量的上游地址。留空 = 走官方 Cursor 上游；填自建 cursor-tab-server 地址可回源自己的账号
            </div>
            <div class="mt-1 text-xs text-[#737373]">
              此能力为 BYOK 的例外：补全流量不经你的 BYOK key，无法用自有 provider 替代。留空走官方时，默认不带凭证会被官方拒绝（401）；需开启下方「留空时带本人 Cursor 凭证」开关补全才可用——开启后消耗你 Cursor 账号的补全额度。
            </div>
          </div>
          <Button
            variant="primary"
            :disabled="tabServerSaving || tabServerTesting"
            @click="handleSaveTabServerBaseURL"
          >
            {{ tabServerSaving ? "保存中..." : "保存地址" }}
          </Button>
          <Button
            variant="default"
            :disabled="tabServerSaving || tabServerTesting"
            @click="handleTestTabServer"
          >
            {{ tabServerTesting ? "测试中..." : "测试" }}
          </Button>
        </div>
        <Input
          v-model="appState.tabServerBaseURL"
          placeholder="留空 = 走官方 api2.cursor.sh；例如 https://tab.example.com"
        />
        <!-- 留空（走官方）时显示「带本人 Cursor 凭证」开关：默认关=不带凭证官方 401 不可用；
             开=带真实凭证补全可用但消耗账号补全额度。填了自建 tab server 时此开关不显示
             （由 server 端账号回源，本机凭证绝不外泄）。 -->
        <div v-if="tabDependsOnLocalCursorAccount" class="flex items-center justify-between gap-3 rounded-lg border border-[#2a2a2a] bg-[#1a1a1a] p-3">
          <div class="min-w-0">
            <div class="text-sm font-medium text-[#e5e5e5]">留空时带本人 Cursor 凭证走官方</div>
            <div class="text-xs text-[#737373]">开启后 Tab 补全 / Git 消息带你的真实 Cursor 凭证透传官方，补全可用；消耗你账号的补全额度（免费账号高频补全可能耗尽）。关闭则留空转发不带凭证，官方会拒绝（401）。</div>
          </div>
          <Switch
            :enabled="!!appState.tabUseCursorCredentials"
            :busy="tabCredSaving"
            enabled-text="带凭证"
            disabled-text="不带凭证"
            @change="handleSaveTabUseCursorCredentials"
          />
        </div>
        <div v-if="tabServerTestResult" class="text-xs" :class="tabServerTestResult.startsWith('✓') ? 'text-emerald-300' : 'text-red-300'">
          {{ tabServerTestResult }}
        </div>
        <!-- 直连官方上游时探测本机 Cursor 账号登录状态，缺失则告警 -->
        <div
          v-if="tabDependsOnLocalCursorAccount"
          class="flex items-center gap-2 rounded-lg border p-2 text-xs"
          :class="cursorAccountStatus.accountPresent
            ? 'border-emerald-500/30 bg-emerald-500/5 text-emerald-300'
            : 'border-red-500/30 bg-red-500/5 text-red-300'"
        >
          <span v-if="cursorAccountLoading" class="text-[#a3a3a3]">正在检测 Cursor 账号登录状态…</span>
          <template v-else>
            <span v-if="cursorAccountStatus.probeError">检测失败：{{ cursorAccountStatus.probeError }}（不影响其他能力）</span>
            <span v-else-if="!cursorAccountStatus.dbExists">未检测到 Cursor 状态库：请先启动并登录 Cursor 客户端，否则 Tab 补全 / Git 消息不可用</span>
            <span v-else-if="cursorAccountStatus.accountPresent">已检测到 Cursor 账号登录{{ cursorAccountStatus.email ? `（${cursorAccountStatus.email}）` : "" }}，Tab 补全可用</span>
            <span v-else>未检测到 Cursor 账号登录：请先在 Cursor 客户端登录账号，否则 Tab 补全 / Git 消息将静默失败</span>
          </template>
        </div>
        <div v-if="!tabDependsOnLocalCursorAccount" class="text-xs text-[#737373]">
          已配置自建 cursor-tab-server：由该 server 自带账号回源，不再探测本机账号。
        </div>
      </div>
    </Card>

    <Card>
      <div class="flex flex-col gap-3">
        <div>
          <h2 class="text-base font-medium text-white">按路由面覆盖</h2>
          <div class="text-sm text-[#a3a3a3]">
            全局运行模式之外，单独让某个面走本地 byok 或直连 Cursor（审计第二部分能力损失优化）。跟随全局 = 不覆盖；当前全局为
            <span class="text-[#d4d4d4]">{{ globalModeLabel }}</span>
          </div>
        </div>
        <div
          v-for="route in PER_NAMESPACE_ROUTES"
          :key="route.name"
          class="flex flex-col gap-2 rounded-lg border border-[#2a2a2a] bg-[#1a1a1a] p-3"
        >
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div class="min-w-0">
              <div class="text-sm font-medium text-[#e5e5e5]">{{ route.label }}</div>
              <div class="text-xs text-[#737373]">{{ route.hint }}</div>
            </div>
            <div class="flex items-center gap-2">
              <Select
                :model-value="namespaceMode(route.name)"
                :options="NAMESPACE_MODE_OPTIONS"
                placeholder="跟随全局"
                class="w-[180px]"
                @update:model-value="(v) => handleSaveNamespaceRoute(route, v)"
              />
            </div>
          </div>
          <div v-if="namespaceSaving === route.name" class="text-xs text-[#737373]">
            保存中…
          </div>
        </div>
        <p class="text-xs text-[#737373]">
          推理面（RunSSE / BidiAppend）默认本地 byok 省钱；按需可切「直连 Cursor」走本人 Cursor 账号——让 BugBot 等「调度云端 + 推理本地」断裂的能力端到端可用（按订阅计费）。其余「云端一体化服务」同理可按需透传。
        </p>
      </div>
    </Card>

    <Card>
      <div class="flex flex-col gap-4">
        <div>
          <h2 class="text-base font-medium text-white">Web 工具（WebSearch / WebFetch）</h2>
          <div class="text-sm text-[#a3a3a3]">
            agent 交互桥的外网工具。WebSearch 默认走 DuckDuckGo（免 key，降级质量，易被封）；免 key provider 失败会按固定顺序回退（duckduckgo→必应→百度），也可配置 Bing/Serper/Tavily 走自带 key（BYOK，Bing 填 key 自动升级官方 API）。WebFetch 默认拒绝所有内网 host（SSRF 防护），可放行企业内网 Wiki/Confluence。
          </div>
        </div>

        <div class="flex flex-col gap-2">
          <div class="text-sm font-medium text-[#e5e5e5]">WebSearch 上游 provider</div>
          <Select
            :model-value="appState.webSearchProvider || 'duckduckgo'"
            :options="webSearchProviderOptions"
            placeholder="选择 provider"
            class="w-full"
            @update:model-value="handleSaveWebSearchProvider"
          />
          <div v-if="webSearchSaving" class="text-xs text-[#737373]">保存中…</div>
        </div>

        <div class="flex flex-col gap-2">
          <div class="text-sm font-medium text-[#e5e5e5]">WebSearch API Key（Serper / Tavily 必填，Bing 可选）</div>
          <Input
            v-model="appState.webSearchAPIKey"
            type="password"
            placeholder="Serper/Tavily 必填；Bing 填了升级官方 API，不填走免费 HTML"
            class="w-full"
            @blur="handleSaveWebSearchAPIKey"
          />
          <div v-if="webSearchKeySaving" class="text-xs text-[#737373]">保存中…</div>
          <div class="flex items-center gap-3">
            <Button variant="default" :disabled="webSearchTesting" @click="handleTestWebSearch">
              {{ webSearchTesting ? "测试中..." : "测试搜索" }}
            </Button>
            <span class="text-xs text-[#737373]">用当前 provider + key 发一次真实搜索验证连通性。</span>
          </div>
          <div v-if="webSearchTestResult" class="text-xs" :class="webSearchTestResult.startsWith('✓') ? 'text-emerald-300' : 'text-red-300'">
            {{ webSearchTestResult }}
          </div>
          <div
            v-if="webSearchNeedsKey"
            class="rounded border border-amber-500/30 bg-amber-500/10 px-2 py-1.5 text-xs text-amber-300"
          >
            当前 provider 需要 API key 才可用，未填写时 WebSearch 将报错提示缺 key（不静默失败）。DuckDuckGo/百度/Bing（免 key）不需要 key。
          </div>
        </div>

        <div class="flex flex-col gap-2">
          <div class="text-sm font-medium text-[#e5e5e5]">WebFetch 内网 host 白名单</div>
          <textarea
            v-model="webFetchAllowlistDraft"
            rows="3"
            placeholder="每行一个 host，如 wiki.internal.corp（留空 = 保持 SSRF 硬拒绝基线）"
            class="w-full rounded border border-[#2a2a2a] bg-[#1a1a1a] p-2 text-sm text-[#e5e5e5] outline-none focus:border-[#3a3a3a]"
          ></textarea>
          <div class="flex items-center gap-3">
            <Button variant="primary" :disabled="webFetchAllowlistSaving || webFetchTesting" @click="handleSaveWebFetchAllowlist">
              {{ webFetchAllowlistSaving ? "保存中..." : "保存白名单" }}
            </Button>
            <Button variant="default" :disabled="webFetchAllowlistSaving || webFetchTesting" @click="handleTestWebFetch">
              {{ webFetchTesting ? "测试中..." : "测试抓取" }}
            </Button>
            <span class="text-xs text-[#737373]">显式放行的 host 绕过私网拒绝；空表 = 最安全（拒绝所有内网）。</span>
          </div>
          <div v-if="webFetchTestResult" class="text-xs" :class="webFetchTestResult.startsWith('✓') ? 'text-emerald-300' : 'text-red-300'">
            {{ webFetchTestResult }}
          </div>
        </div>
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">界面语言</h2>
          <div class="text-sm text-[#a3a3a3]">
            切换当前界面显示语言，设置会立即生效并保存在本机
          </div>
        </div>
        <LocaleSelect wrapper-class="w-[220px] max-w-full" />
      </div>
    </Card>

    <Card>
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-medium text-white">模型配置</h2>
          <div class="text-sm text-[#a3a3a3]">
            已配置 {{ appState.modelAdapters.length }} 个模型适配器
          </div>
        </div>
        <Button variant="primary" @click="handleOpenModelConfig">打开模型配置</Button>
      </div>
    </Card>
  </div>
</template>
