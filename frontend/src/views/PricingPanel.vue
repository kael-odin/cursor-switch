<script setup>
import { computed, nextTick, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import Button from "@/components/ui/Button.vue";
import Input from "@/components/ui/Input.vue";
import { showModal } from "@/composables/useModal";
import {
  deleteModelPricing,
  getPricingSnapshot,
  restoreDefaultPricing,
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
// 编辑器 section 的 ref：openEdit/openAdd 后 nextTick 把它滚进视口 + 聚焦首个输入框，
// 避免「点了编辑没反应」的错觉（编辑器出现在表格下方，长列表时需滚动才可见）。
const editorSectionRef = ref(null);
const firstFieldRef = ref(null);

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
  focusEditor();
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
  focusEditor();
}

// 编辑器出现在表格下方，长列表时不在视口内——打开后滚进视口并聚焦首个可编辑输入框，
// 让用户立刻看到「点击编辑」确实展开了编辑 UI，不再误以为点击无效。
// nextTick 等 v-if 渲染出 DOM 后再操作。
function focusEditor() {
  nextTick(() => {
    const section = editorSectionRef.value;
    if (!section) return;
    section.scrollIntoView({ behavior: "smooth", block: "center" });
    // 聚焦首个可编辑 input（编辑态 modelId 禁用，故跳过 disabled）。
    const input =
      section.querySelector("input:not([disabled])") ||
      section.querySelector("textarea:not([disabled])");
    if (input) input.focus();
  });
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

// F-17：内置模型（isBuiltin）"删除"实为逻辑删除（后端置 Disabled=true tombstone），
// seed 不再补回、成本按 0 计；可经"恢复默认价"还原。自定义模型才物理删除。
async function confirmDelete(model) {
  const isBuiltin = !!model.isBuiltin;
  const verb = isBuiltin ? "禁用" : "删除";
  const ok = await showModal({
    title: `${verb}定价记录`,
    content: isBuiltin
      ? `「${model.modelId}」是内置标准价目。${verb}后该模型成本将按 0 计算，可随时点「恢复默认价」还原。`
      : `确认${verb}「${model.modelId}」的定价记录？${verb}后该模型成本将按 0 计算。`,
    confirmText: verb,
    cancelText: "取消",
  });
  if (!ok) return;
  try {
    await deleteModelPricing(model.modelId);
    await refresh();
  } catch (e) {
    errorMsg.value = `${verb}失败: ${e?.message ?? e}`;
  }
}

async function confirmRestore(model) {
  const ok = await showModal({
    title: "恢复默认价",
    content: `把「${model.modelId}」的价格重置为内置标准价目？`,
    confirmText: "恢复",
    cancelText: "取消",
  });
  if (!ok) return;
  try {
    await restoreDefaultPricing(model.modelId);
    await refresh();
  } catch (e) {
    errorMsg.value = `恢复失败: ${e?.message ?? e}`;
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

// 行内直接编辑表格价格单元格：点价格 → 该格变 input → 回车/失焦保存。
// 规避「点编辑滚到底下找表单」的来回，常用微调（改输入价/缓存价）就地完成。
// modelId/displayName 不开放行内编辑（是键/标识，仍走下方编辑器）。
const inlineEdit = ref({ modelId: "", field: "", value: "" });
const inlineSaving = ref(false);

function startInlineEdit(model, field) {
  if (inlineSaving.value) return;
  inlineEdit.value = { modelId: model.modelId, field, value: String(model[field] ?? "") };
  nextTick(() => {
    // 聚焦刚出现的 input 并全选，方便直接覆盖输入。
    // 用 data-row + data-field 精确定位当前行的当前字段，避免多行同字段歧义。
    const el = document.querySelector(
      `[data-row="${cssEscape(model.modelId)}"][data-field="${field}"] input`,
    );
    if (el) {
      el.focus();
      el.select();
    }
  });
}

// cssEscape 转义 modelId 里的特殊字符用于 attribute 选择器（modelId 可能含 / . : 等）。
function cssEscape(s) {
  return (window.CSS && window.CSS.escape) ? window.CSS.escape(s) : String(s).replace(/["\\]/g, "\\$&");
}

function cancelInlineEdit() {
  inlineEdit.value = { modelId: "", field: "", value: "" };
}

async function commitInlineEdit(model) {
  const { modelId, field, value } = inlineEdit.value;
  if (!modelId || !field) return;
  // 重入保护：Enter 会触发 commit，随后 input 卸载又触发 blur→commit，避免双发请求。
  // 先清 inlineEdit 使第二次调用命中顶部 guard 直接返回。
  cancelInlineEdit();
  const num = Number(value);
  if (!Number.isFinite(num) || num < 0) {
    errorMsg.value = `${field} 必须是 ≥ 0 的数字`;
    return;
  }
  if (num === Number(model[field] ?? 0)) {
    // 未改动，不请求。
    return;
  }
  // UpdateModelPricing 是整记录覆盖（后端把 0 也当有效价写盘），故须用行现值补齐全部四价，
  // 仅替换当前编辑字段。
  const payload = {
    modelId,
    displayName: model.displayName || modelId,
    inputPerMillion: Number(model.inputPerMillion) || 0,
    outputPerMillion: Number(model.outputPerMillion) || 0,
    cacheReadPerMillion: Number(model.cacheReadPerMillion) || 0,
    cacheWritePerMillion: Number(model.cacheWritePerMillion) || 0,
  };
  payload[field] = num;
  inlineSaving.value = true;
  errorMsg.value = "";
  try {
    await updateModelPricing(payload);
    cancelInlineEdit();
    await refresh();
  } catch (e) {
    errorMsg.value = `保存失败: ${e?.message ?? e}`;
  } finally {
    inlineSaving.value = false;
  }
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
                :class="[
                  'border-t border-[#2a2a2a] hover:bg-[#1f1f1f]',
                  m.disabled ? 'opacity-50' : '',
                ]"
              >
                <td class="px-3 py-2 font-mono text-[#d4d4d4]">
                  {{ m.modelId }}
                  <span
                    v-if="m.isBuiltin"
                    class="ml-1 rounded bg-[#333] px-1 text-[10px] text-[#a3a3a3]"
                    >内置</span
                  >
                  <span
                    v-if="m.disabled"
                    class="ml-1 rounded bg-[#3a2a2a] px-1 text-[10px] text-[#d97777]"
                    >已禁用</span
                  >
                </td>
                <td class="px-3 py-2">{{ m.displayName }}</td>
                <td
                  class="px-3 py-2 text-right tabular-nums"
                  :class="m.disabled ? '' : 'cursor-text hover:bg-[#262626]'"
                  :data-row="m.modelId"
                  data-field="inputPerMillion"
                  @click="m.disabled ? null : startInlineEdit(m, 'inputPerMillion')"
                >
                  <Input
                    v-if="!m.disabled && inlineEdit.modelId === m.modelId && inlineEdit.field === 'inputPerMillion'"
                    v-model="inlineEdit.value"
                    type="number"
                    class="!w-20"
                    @blur="commitInlineEdit(m)"
                    @keyup.enter="commitInlineEdit(m)"
                    @keyup.esc="cancelInlineEdit"
                  />
                  <span v-else>{{ formatPrice(m.inputPerMillion) }}</span>
                </td>
                <td
                  class="px-3 py-2 text-right tabular-nums"
                  :class="m.disabled ? '' : 'cursor-text hover:bg-[#262626]'"
                  :data-row="m.modelId"
                  data-field="outputPerMillion"
                  @click="m.disabled ? null : startInlineEdit(m, 'outputPerMillion')"
                >
                  <Input
                    v-if="!m.disabled && inlineEdit.modelId === m.modelId && inlineEdit.field === 'outputPerMillion'"
                    v-model="inlineEdit.value"
                    type="number"
                    class="!w-20"
                    @blur="commitInlineEdit(m)"
                    @keyup.enter="commitInlineEdit(m)"
                    @keyup.esc="cancelInlineEdit"
                  />
                  <span v-else>{{ formatPrice(m.outputPerMillion) }}</span>
                </td>
                <td
                  class="px-3 py-2 text-right tabular-nums"
                  :class="m.disabled ? '' : 'cursor-text hover:bg-[#262626]'"
                  :data-row="m.modelId"
                  data-field="cacheReadPerMillion"
                  @click="m.disabled ? null : startInlineEdit(m, 'cacheReadPerMillion')"
                >
                  <Input
                    v-if="!m.disabled && inlineEdit.modelId === m.modelId && inlineEdit.field === 'cacheReadPerMillion'"
                    v-model="inlineEdit.value"
                    type="number"
                    class="!w-20"
                    @blur="commitInlineEdit(m)"
                    @keyup.enter="commitInlineEdit(m)"
                    @keyup.esc="cancelInlineEdit"
                  />
                  <span v-else>{{ formatPrice(m.cacheReadPerMillion) }}</span>
                </td>
                <td
                  class="px-3 py-2 text-right tabular-nums"
                  :class="m.disabled ? '' : 'cursor-text hover:bg-[#262626]'"
                  :data-row="m.modelId"
                  data-field="cacheWritePerMillion"
                  @click="m.disabled ? null : startInlineEdit(m, 'cacheWritePerMillion')"
                >
                  <Input
                    v-if="!m.disabled && inlineEdit.modelId === m.modelId && inlineEdit.field === 'cacheWritePerMillion'"
                    v-model="inlineEdit.value"
                    type="number"
                    class="!w-20"
                    @blur="commitInlineEdit(m)"
                    @keyup.enter="commitInlineEdit(m)"
                    @keyup.esc="cancelInlineEdit"
                  />
                  <span v-else>{{ formatPrice(m.cacheWritePerMillion) }}</span>
                </td>
                <td class="whitespace-nowrap px-3 py-2 text-center">
                  <Button variant="text" @click="openEdit(m)">编辑</Button>
                  <Button v-if="m.disabled" variant="text" @click="confirmRestore(m)">恢复默认价</Button>
                  <Button v-else variant="text" @click="confirmDelete(m)">{{
                    m.isBuiltin ? "禁用" : "删除"
                  }}</Button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="mt-2 text-xs text-[#737373]">
          内置标准价目表来自公开模型定价。<span class="text-[#a3a3a3]">直接点击表格中的价格格即可就地修改</span>（回车保存 / Esc 取消），点「编辑」可改模型 ID / 显示名等全部字段，点「+ 添加自定义模型」可为你接入的第三方模型设价。
        </p>
      </section>

      <section
        v-if="editorVisible"
        ref="editorSectionRef"
        class="mb-6 rounded-lg border border-[#3a3a3a] bg-[#1a1a1a] p-4 shadow-[0_0_0_1px_rgba(96,165,250,0.15)] ring-1 ring-blue-500/20"
      >
        <h3 class="mb-1 text-base font-medium text-[#e5e5e5]">
          {{ editorIsNew ? "添加模型定价" : "编辑模型定价" }}
        </h3>
        <p class="mb-3 text-xs text-[#737373]">
          {{ editorIsNew ? "填入新模型的价格后点「添加」。" : "修改价格后点「保存」生效。" }}
        </p>
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
