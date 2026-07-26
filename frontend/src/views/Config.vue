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
  saveTabServerBaseURL,
  toUserError,
} from "@/state/appState";
import { onMounted, ref } from "vue";

const routeModeOptions = ROUTE_MODE_OPTIONS;
const tabServerSaving = ref(false);

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
