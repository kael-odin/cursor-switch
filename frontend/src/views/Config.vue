<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Input from "@/components/ui/Input.vue";
import LocaleSelect from "@/components/LocaleSelect.vue";
import Select from "@/components/ui/Select.vue";
import { showModal } from "@/composables/useModal";
import {
  appState,
  openModelConfigWindow,
  persistUserConfig,
  reloadUserConfig,
  ROUTE_MODE_OPTIONS,
  PER_NAMESPACE_ROUTES,
  savePerNamespaceRoute,
  saveTabServerBaseURL,
  toUserError,
} from "@/state/appState";
import { computed, onMounted, ref } from "vue";

const routeModeOptions = ROUTE_MODE_OPTIONS;
const tabServerSaving = ref(false);

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

async function handleOpenModelConfig() {
  try {
    await openModelConfigWindow();
  } catch (error) {
    await showActionError("打开失败", toUserError(error));
  }
}

onMounted(async () => {
  await reloadUserConfig().catch(() => {});
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
            v-model="appState.routingMode"
            :options="routeModeOptions"
            placeholder="选择模式"
          />
        </div>
      </div>
    </Card>

    <Card>
      <div class="flex flex-col gap-3">
        <div class="flex items-center justify-between gap-4">
          <div>
            <h2 class="text-base font-medium text-white">Tab 补全服务地址</h2>
            <div class="text-sm text-[#a3a3a3]">
              控制 Tab 代码补全 / Git Commit / 分支名生成等流量的上游地址。留空 = 走官方 Cursor 上游（用你自己的 Cursor 账号额度）；填自建 cursor-tab-server 地址可回源自己的账号
            </div>
          </div>
          <Button
            variant="primary"
            :disabled="tabServerSaving"
            @click="handleSaveTabServerBaseURL"
          >
            {{ tabServerSaving ? "保存中..." : "保存地址" }}
          </Button>
        </div>
        <Input
          v-model="appState.tabServerBaseURL"
          placeholder="留空 = 走官方 api2.cursor.sh；例如 https://tab.example.com"
        />
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
          推理面（RunSSE / BidiAppend）始终走本地 byok，不在此覆盖——强制本地是项目目标。仅「云端一体化服务」可按需透传到本人 Cursor 账号（代码/文档会经 Cursor 云）。
        </p>
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
