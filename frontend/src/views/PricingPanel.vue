<script setup>
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import Button from "@/components/ui/Button.vue";
import Input from "@/components/ui/Input.vue";
import { showModal } from "@/composables/useModal";
import {
  deleteModelPricing,
  getPricingSnapshot,
  setDefaultCostMultiplier,
  updateModelPricing,
} from "@/services/clientApi";

const router = useRouter();
function backHome() {
  router.push("/");
}

const models = ref([]);
const defaultMultiplier = ref(1);
const loading = ref(false);
const errorMsg = ref("");

const editorVisible = ref(false);
const editorIsNew = ref(true);
const editorForm = ref(emptyForm());
const editorError = ref("");
const saving = ref(false);

const multiplierInput = ref("1");
const multiplierDirty = ref(false);
const multiplierSaving = ref(false);

function emptyForm() {
  return {
    modelId: "",
    displayName: "",
    inputPerMillion: 0,
    outputPerMillion: 0,
    cacheReadPerMillion: 0,
    cacheWritePerMillion: 0,
  };
}

async function refresh() {
  loading.value = true;
  errorMsg.value = "";
  try {
    const snap = await getPricingSnapshot();
    models.value = snap?.models ?? [];
    defaultMultiplier.value = snap?.defaultCostMultiplier ?? 1;
    multiplierInput.value = String(defaultMultiplier.value);
    multiplierDirty.value = false;
  } catch (e) {
    errorMsg.value = `加载定价失败: ${e?.message ?? e}`;
  } finally {
    loading.value = false;
  }
}

const sortedModels = computed(() =>
  [...models.value].sort((a, b) =>
    String(a.displayName || a.modelId).localeCompare(String(b.displayName || b.modelId)),
  ),
);

function openAdd() {
  editorIsNew.value = true;
  editorForm.value = emptyForm();
  editorError.value = "";
  editorVisible.value = true;
}

function openEdit(model) {
  editorIsNew.value = false;
  editorForm.value = {
    modelId: model.modelId,
    displayName: model.displayName,
    inputPerMillion: model.inputPerMillion,
    outputPerMillion: model.outputPerMillion,
    cacheReadPerMillion: model.cacheReadPerMillion,
    cacheWritePerMillion: model.cacheWritePerMillion,
  };
  editorError.value = "";
  editorVisible.value = true;
}

function closeEditor() {
  editorVisible.value = false;
  editorError.value = "";
}

async function saveModel() {
  const f = editorForm.value;
  if (!String(f.modelId || "").trim()) {
    editorError.value = "modelId 不能为空";
    return;
  }
  if (Number(f.inputPerMillion) < 0 || Number(f.outputPerMillion) < 0) {
    editorError.value = "价格不能为负";
    return;
  }
  saving.value = true;
  editorError.value = "";
  try {
    await updateModelPricing({
      modelId: String(f.modelId).trim(),
      displayName: String(f.displayName || "").trim() || String(f.modelId).trim(),
      inputPerMillion: Number(f.inputPerMillion) || 0,
      outputPerMillion: Number(f.outputPerMillion) || 0,
      cacheReadPerMillion: Number(f.cacheReadPerMillion) || 0,
      cacheWritePerMillion: Number(f.cacheWritePerMillion) || 0,
    });
    editorVisible.value = false;
    await refresh();
  } catch (e) {
    editorError.value = `保存失败: ${e?.message ?? e}`;
  } finally {
    saving.value = false;
  }
}

async function confirmDelete(model) {
  const ok = await showModal({
    title: "删除定价记录",
    content: `确认删除「${model.modelId}」的定价记录？删除后该模型成本将按 0 计算。`,
    confirmText: "删除",
    cancelText: "取消",
  });
  if (!ok) return;
  try {
    await deleteModelPricing(model.modelId);
    await refresh();
  } catch (e) {
    errorMsg.value = `删除失败: ${e?.message ?? e}`;
  }
}

async function saveMultiplier() {
  const value = Number(multiplierInput.value);
  // F-34：与后端 validatePositiveFiniteMultiplier 同规则——必须有限且大于零。
  if (!Number.isFinite(value) || value <= 0) {
    errorMsg.value = "倍率必须是大于零的有限数字";
    return;
  }
  multiplierSaving.value = true;
  errorMsg.value = "";
  try {
    await setDefaultCostMultiplier(String(value));
    defaultMultiplier.value = value;
    multiplierDirty.value = false;
  } catch (e) {
    errorMsg.value = `保存倍率失败: ${e?.message ?? e}`;
  } finally {
    multiplierSaving.value = false;
  }
}

function onMultiplierInput() {
  multiplierDirty.value = Number(multiplierInput.value) !== defaultMultiplier.value;
}

function formatPrice(v) {
  const n = Number(v) || 0;
  return n.toFixed(n > 0 && n < 1 ? 4 : 2);
}

onMounted(refresh);
</script>

<template>
  <div class="flex h-full min-h-0 flex-col overflow-hidden text-[#e5e5e5]">
    <div class="flex-1 min-h-0 overflow-y-auto px-6 py-5 text-sm text-[#d4d4d4]">
      <div class="mb-4 flex items-center gap-3">
        <Button variant="text" @click="backHome">← 返回首页</Button>
      </div>
      <section class="mb-6 rounded-lg border border-[#2a2a2a] bg-[#1a1a1a] p-4">
        <div class="flex flex-wrap items-center gap-3">
          <span class="text-[#a3a3a3]">全局成本倍率</span>
          <Input
            v-model="multiplierInput"
            type="number"
            placeholder="1"
            class="!w-28"
            @update:model-value="onMultiplierInput"
          />
          <Button
            :variant="multiplierDirty ? 'primary' : 'default'"
            :disabled="!multiplierDirty || multiplierSaving"
            @click="saveMultiplier"
          >
            {{ multiplierSaving ? "保存中…" : "保存倍率" }}
          </Button>
          <span class="text-xs text-[#737373]">
            应用于所有模型总成本；单个模型可在「模型配置」里设 CostMultiplier 覆盖此值
          </span>
        </div>
      </section>

      <p v-if="errorMsg" class="mb-4 text-xs text-red-400">{{ errorMsg }}</p>

      <section class="mb-6">
        <div class="mb-3 flex items-center justify-between">
          <h2 class="text-base font-medium text-[#e5e5e5]">模型定价（USD / 百万 token）</h2>
          <Button variant="primary" @click="openAdd">+ 添加自定义模型</Button>
        </div>

        <div v-if="loading" class="py-8 text-center text-[#737373]">加载中…</div>
        <div
          v-else-if="!sortedModels.length"
          class="py-8 text-center text-[#737373]"
        >
          暂无定价记录
        </div>
        <div v-else class="overflow-x-auto rounded-lg border border-[#2a2a2a]">
          <table class="w-full text-xs">
            <thead class="bg-[#222] text-[#a3a3a3]">
              <tr>
                <th class="px-3 py-2 text-left font-medium">模型 ID</th>
                <th class="px-3 py-2 text-left font-medium">显示名</th>
                <th class="px-3 py-2 text-right font-medium">输入</th>
                <th class="px-3 py-2 text-right font-medium">输出</th>
                <th class="px-3 py-2 text-right font-medium">缓存读</th>
                <th class="px-3 py-2 text-right font-medium">缓存写</th>
                <th class="px-3 py-2 text-center font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="m in sortedModels"
                :key="m.modelId"
                class="border-t border-[#2a2a2a] hover:bg-[#1f1f1f]"
              >
                <td class="px-3 py-2 font-mono text-[#d4d4d4]">{{ m.modelId }}</td>
                <td class="px-3 py-2">{{ m.displayName }}</td>
                <td class="px-3 py-2 text-right tabular-nums">{{ formatPrice(m.inputPerMillion) }}</td>
                <td class="px-3 py-2 text-right tabular-nums">{{ formatPrice(m.outputPerMillion) }}</td>
                <td class="px-3 py-2 text-right tabular-nums">{{ formatPrice(m.cacheReadPerMillion) }}</td>
                <td class="px-3 py-2 text-right tabular-nums">{{ formatPrice(m.cacheWritePerMillion) }}</td>
                <td class="whitespace-nowrap px-3 py-2 text-center">
                  <Button variant="text" @click="openEdit(m)">编辑</Button>
                  <Button variant="text" @click="confirmDelete(m)">删除</Button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="mt-2 text-xs text-[#737373]">
          内置标准价目表来自公开模型定价；点「编辑」可改任意模型价格，点「+ 添加自定义模型」可为你接入的第三方模型设价。
        </p>
      </section>

      <section
        v-if="editorVisible"
        class="mb-6 rounded-lg border border-[#2a2a2a] bg-[#1a1a1a] p-4"
      >
        <h3 class="mb-3 text-base font-medium text-[#e5e5e5]">
          {{ editorIsNew ? "添加模型定价" : "编辑模型定价" }}
        </h3>
        <div class="grid grid-cols-2 gap-3">
          <label class="block">
            <span class="text-xs text-[#a3a3a3]">模型 ID</span>
            <Input
              v-model="editorForm.modelId"
              :disabled="!editorIsNew"
              placeholder="例如 gpt-5.6-sol"
              class="mt-1 w-full"
            />
          </label>
          <label class="block">
            <span class="text-xs text-[#a3a3a3]">显示名</span>
            <Input
              v-model="editorForm.displayName"
              placeholder="例如 GPT-5.6 Sol"
              class="mt-1 w-full"
            />
          </label>
          <label class="block">
            <span class="text-xs text-[#a3a3a3]">输入 $/M</span>
            <Input
              v-model="editorForm.inputPerMillion"
              type="number"
              class="mt-1 w-full"
            />
          </label>
          <label class="block">
            <span class="text-xs text-[#a3a3a3]">输出 $/M</span>
            <Input
              v-model="editorForm.outputPerMillion"
              type="number"
              class="mt-1 w-full"
            />
          </label>
          <label class="block">
            <span class="text-xs text-[#a3a3a3]">缓存读 $/M</span>
            <Input
              v-model="editorForm.cacheReadPerMillion"
              type="number"
              class="mt-1 w-full"
            />
          </label>
          <label class="block">
            <span class="text-xs text-[#a3a3a3]">缓存写 $/M</span>
            <Input
              v-model="editorForm.cacheWritePerMillion"
              type="number"
              class="mt-1 w-full"
            />
          </label>
        </div>
        <p v-if="editorError" class="mt-3 text-xs text-red-400">{{ editorError }}</p>
        <div class="mt-4 flex justify-end gap-2">
          <Button variant="default" @click="closeEditor">取消</Button>
          <Button variant="primary" :disabled="saving" @click="saveModel">
            {{ saving ? "保存中…" : editorIsNew ? "添加" : "保存" }}
          </Button>
        </div>
      </section>
    </div>
  </div>
</template>
