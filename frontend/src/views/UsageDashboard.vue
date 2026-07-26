<script setup>
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import Button from "@/components/ui/Button.vue";
import EChart from "@/components/charts/EChart.vue";
import { getUsageDashboard } from "@/services/clientApi";

const router = useRouter();
function backHome() {
  router.push("/");
}

const dashboard = ref(null);
const loading = ref(false);
const errorMsg = ref("");
const autoRefresh = ref(true);
const refreshInterval = ref(30); // 秒
let timer = null;

// 日期范围：today / 7d / 30d / all。作用于趋势图与请求日志。
const dateRange = ref("all");
// 请求日志分页
const eventPage = ref(1);
const eventPageSize = 20;

async function refresh() {
  loading.value = true;
  errorMsg.value = "";
  try {
    dashboard.value = await getUsageDashboard();
  } catch (e) {
    errorMsg.value = `加载统计失败: ${e?.message ?? e}`;
  } finally {
    loading.value = false;
  }
}

onMounted(async () => {
  await refresh();
  startTimer();
});

function startTimer() {
  stopTimer();
  if (!autoRefresh.value) return;
  const ms = Math.max(5, Number(refreshInterval.value) || 30) * 1000;
  timer = window.setInterval(refresh, ms);
}
function stopTimer() {
  if (timer) {
    window.clearInterval(timer);
    timer = null;
  }
}
function onIntervalChange() {
  startTimer();
}
function toggleAutoRefresh() {
  autoRefresh.value = !autoRefresh.value;
  startTimer();
}
function onDateRangeChange() {
  eventPage.value = 1;
}

// 日期范围边界（本地时区当天 00:00）
function rangeStart() {
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  if (dateRange.value === "today") return today;
  if (dateRange.value === "7d") return new Date(today.getTime() - 6 * 86400000);
  if (dateRange.value === "30d") return new Date(today.getTime() - 29 * 86400000);
  return null; // all
}
function inRange(t) {
  const start = rangeStart();
  if (!start) return true;
  return new Date(t) >= start;
}
// daily 的 date 是 "YYYY-MM-DD" UTC；转成本地日期对象比较
function dateStrInRange(dateStr) {
  const start = rangeStart();
  if (!start) return true;
  if (!dateStr) return false;
  // dateStr 是 UTC 日，用本地 00:00 解析近似（趋势图按日聚合，跨时区误差 1 天可接受）
  const parts = dateStr.split("-").map(Number);
  if (parts.length !== 3) return true;
  const d = new Date(parts[0], parts[1] - 1, parts[2]);
  return d >= start;
}

const totals = computed(() => dashboard.value?.totals ?? null);
const dailyAll = computed(() => dashboard.value?.daily ?? []);
const daily = computed(() => dailyAll.value.filter((r) => dateStrInRange(r.date)));
const byModel = computed(() => dashboard.value?.byModel ?? []);
const byProvider = computed(() => dashboard.value?.byProvider ?? []);
const recentEventsAll = computed(() => dashboard.value?.recentEvents ?? []);
const recentEvents = computed(() => recentEventsAll.value.filter((e) => inRange(e.at)));
const updatedAt = computed(() => {
  const t = dashboard.value?.updatedAt;
  if (!t) return "";
  try {
    return new Date(t).toLocaleString();
  } catch {
    return String(t);
  }
});

// 是否有任意一天的日成本是近似的（旧 usage.json 无 daily.by_model）
const hasApproximateDaily = computed(() => daily.value.some((r) => r.costApproximate));

// M9：口径异常请求数（基于 recent_events 扫描，非全量）。
// input < cacheRead+cacheWrite 表示 provider 返回的 input 未正确包含缓存，成本/token 可能失真。
const calibrationAnomalyCount = computed(() => totals.value?.calibrationAnomalyCount ?? 0);
const anomalyEvents = computed(() => recentEvents.value.filter((e) => e.calibrationAnomaly));

// 请求日志分页
const eventTotalPages = computed(() => Math.max(1, Math.ceil(recentEvents.value.length / eventPageSize)));
const eventPaged = computed(() => {
  const start = (eventPage.value - 1) * eventPageSize;
  return recentEvents.value.slice(start, start + eventPageSize);
});
function goEventPage(delta) {
  const next = eventPage.value + delta;
  if (next < 1 || next > eventTotalPages.value) return;
  eventPage.value = next;
}

function fmtNum(n) {
  const v = Number(n) || 0;
  if (v >= 1e9) return (v / 1e9).toFixed(2) + "B";
  if (v >= 1e6) return (v / 1e6).toFixed(2) + "M";
  if (v >= 1e3) return (v / 1e3).toFixed(1) + "K";
  return String(v);
}
function fmtUsd(v, digits = 4) {
  const n = Number(v) || 0;
  return "$" + n.toFixed(n > 0 && n < 1 ? digits : 2);
}
function fmtPct(v) {
  if (v == null) return "—";
  const n = Number(v) || 0;
  return (n >= 0.9995 ? (n * 100).toFixed(0) : (n * 100).toFixed(1)) + "%";
}
function fmtTime(t) {
  if (!t) return "";
  try {
    return new Date(t).toLocaleString();
  } catch {
    return String(t);
  }
}

// ECharts 趋势图：堆叠面积（四类 token）+ 成本折线（右轴）
const trendOption = computed(() => {
  const rows = daily.value;
  const dates = rows.map((r) => r.date);
  const base = {
    backgroundColor: "transparent",
    textStyle: { color: "#d4d4d4" },
    tooltip: { trigger: "axis" },
    legend: {
      data: ["输入", "输出", "缓存读", "缓存写", "成本"],
      top: 0,
      textStyle: { color: "#a3a3a3" },
    },
    grid: { left: 50, right: 60, top: 36, bottom: 40 },
    xAxis: { type: "category", data: dates, axisLabel: { color: "#737373" } },
    yAxis: [
      { type: "value", name: "tokens", axisLabel: { color: "#737373", formatter: (v) => fmtNum(v) } },
      { type: "value", name: "USD", axisLabel: { color: "#737373" }, splitLine: { show: false } },
    ],
    series: [
      seriesArea("输入", rows.map((r) => r.inputTokens), "#3b82f6"),
      seriesArea("输出", rows.map((r) => r.outputTokens), "#22c55e"),
      seriesArea("缓存读", rows.map((r) => r.cacheReadTokens), "#a855f7"),
      seriesArea("缓存写", rows.map((r) => r.cacheWriteTokens), "#f97316"),
      {
        name: "成本",
        type: "line",
        yAxisIndex: 1,
        data: rows.map((r) => Number(r.costUSD) || 0),
        lineStyle: { color: "#f43f5e", type: "dashed" },
        itemStyle: { color: "#f43f5e" },
        symbol: "circle",
        symbolSize: 5,
      },
    ],
  };
  return base;
});

function seriesArea(name, data, color) {
  return {
    name,
    type: "line",
    stack: "tokens",
    smooth: true,
    showSymbol: false,
    data,
    lineStyle: { color },
    itemStyle: { color },
    areaStyle: { opacity: 0.25 },
  };
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col overflow-hidden text-[#e5e5e5]">
    <div class="flex-1 min-h-0 overflow-y-auto px-6 py-5 text-sm text-[#d4d4d4]">
      <div class="mb-4 flex items-center justify-between gap-3">
        <Button variant="text" @click="backHome">← 返回首页</Button>
        <div class="flex items-center gap-3 text-xs text-[#737373]">
          <span v-if="updatedAt">刷新于 {{ updatedAt }}</span>
          <label class="flex items-center gap-1">
            <input
              type="checkbox"
              class="h-3 w-3 accent-[#10AD5D]"
              :checked="autoRefresh"
              @change="toggleAutoRefresh"
            />
            <span>自动刷新</span>
          </label>
          <select
            v-model="refreshInterval"
            class="h-6 rounded-[4px] border border-[#3f3f3f] bg-[#232323] px-1 text-xs text-[#d4d4d4] outline-none"
            @change="onIntervalChange"
          >
            <option :value="10">10s</option>
            <option :value="30">30s</option>
            <option :value="60">60s</option>
          </select>
          <!-- 日期范围选择器 -->
          <select
            v-model="dateRange"
            class="h-6 rounded-[4px] border border-[#3f3f3f] bg-[#232323] px-1 text-xs text-[#d4d4d4] outline-none"
            @change="onDateRangeChange"
          >
            <option value="today">今天</option>
            <option value="7d">近 7 天</option>
            <option value="30d">近 30 天</option>
            <option value="all">全部</option>
          </select>
          <Button variant="text" :disabled="loading" @click="refresh">
            {{ loading ? "刷新中…" : "刷新" }}
          </Button>
        </div>
      </div>

      <p v-if="errorMsg" class="mb-4 text-xs text-red-400">{{ errorMsg }}</p>

      <!-- 通俗说明：给非技术用户解释这些数字到底是什么 -->
      <section class="mb-5 rounded-lg border border-[#2a2a2a] bg-[#1a1a1a] p-3 text-xs leading-relaxed text-[#9a9a9a]">
        <span class="text-[#d4d4d4]">怎么看这张表：</span>
        「真实消耗 Tokens」是你实际「烧掉」的 token 总量，= 本次新输入 + 模型输出 + 缓存写入 + 缓存读取，
        是衡量用量的最准数字。「总成本」按各模型的官方单价 × 你设的倍率估算（美元），仅供参考，
        实际账单以 provider 为准。<template v-if="hasApproximateDaily">带「≈」的日成本是近似值
        （当天数据缺模型维度，按均价估算）；升级后新数据已精确按模型计算。</template>
        <template v-else>日成本已按各模型精确计算（per-model 价格 × 倍率）。</template>
      </section>

      <!-- M9 口径异常提示：仅当存在 input < 缓存 的请求时显示 -->
      <section
        v-if="calibrationAnomalyCount > 0"
        class="mb-5 rounded-lg border border-amber-600/50 bg-amber-950/30 p-3 text-xs leading-relaxed text-amber-300"
      >
        <span class="font-medium text-amber-200">⚠ 检测到 {{ calibrationAnomalyCount }} 条请求存在成本口径异常</span>
        ——这些请求的 input token 数小于缓存读取+缓存写入，说明该 provider 返回的 input
        未正确包含缓存部分，导致 input 成本可能被低估、真实消耗 token 数可能失真。
        请在下方请求日志中查看标记「口径异常」的条目，核对对应模型的「输入 token 语义」配置（TOTAL/legacy/FRESH）。
      </section>

      <!-- Hero: 真实消耗 token 板 -->
      <section class="mb-6 grid grid-cols-2 gap-3 md:grid-cols-4 lg:grid-cols-6">
        <div class="rounded-lg border border-[#2a2a2a] bg-[#1a1a1a] p-3">
          <div class="text-xs text-[#737373]">真实消耗 Tokens</div>
          <div class="mt-1 text-lg font-semibold text-[#e5e5e5] tabular-nums">
            {{ fmtNum(totals?.realTotalTokens) }}
          </div>
        </div>
        <div class="rounded-lg border border-[#2a2a2a] bg-[#1a1a1a] p-3">
          <div class="text-xs text-[#737373]">总请求</div>
          <div class="mt-1 text-lg font-semibold text-[#e5e5e5] tabular-nums">
            {{ fmtNum(totals?.providerCalls) }}
          </div>
        </div>
        <div class="rounded-lg border border-[#2a2a2a] bg-[#1a1a1a] p-3">
          <div class="text-xs text-[#737373]">总成本</div>
          <div class="mt-1 text-lg font-semibold text-[#10AD5D] tabular-nums">
            {{ fmtUsd(totals?.totalCostUSD) }}
          </div>
        </div>
        <div class="rounded-lg border border-[#2a2a2a] bg-[#1a1a1a] p-3">
          <div class="text-xs text-[#737373]">缓存命中率</div>
          <div class="mt-1 text-lg font-semibold text-[#a855f7] tabular-nums">
            {{ fmtPct(totals?.cacheHitRate) }}
          </div>
        </div>
        <div class="rounded-lg border border-[#2a2a2a] bg-[#1a1a1a] p-3">
          <div class="text-xs text-[#737373]">输入 Tokens</div>
          <div class="mt-1 text-base font-medium text-[#3b82f6] tabular-nums">
            {{ fmtNum(totals?.inputTokens) }}
          </div>
        </div>
        <div class="rounded-lg border border-[#2a2a2a] bg-[#1a1a1a] p-3">
          <div class="text-xs text-[#737373]">输出 Tokens</div>
          <div class="mt-1 text-base font-medium text-[#22c55e] tabular-nums">
            {{ fmtNum(totals?.outputTokens) }}
          </div>
        </div>
      </section>

      <!-- 趋势图 -->
      <section class="mb-6 rounded-lg border border-[#2a2a2a] bg-[#1a1a1a] p-4">
        <h2 class="mb-1 text-base font-medium text-[#e5e5e5]">使用趋势（按日）</h2>
        <p class="mb-3 text-xs text-[#737373]">
          <template v-if="hasApproximateDaily">
            成本折线（虚线）含近似值（部分日期缺模型维度，按均价估算）；精确成本见下方模型统计。
          </template>
          <template v-else>
            成本折线（虚线）已按当天各模型精确计算（per-model 价格 × 倍率）。
          </template>
        </p>
        <div v-if="!daily.length" class="py-8 text-center text-[#737373]">该范围内暂无数据</div>
        <EChart v-else :option="trendOption" height="320px" />
      </section>

      <!-- 模型统计 -->
      <section class="mb-6">
        <h2 class="mb-1 text-base font-medium text-[#e5e5e5]">模型统计</h2>
        <p class="mb-3 text-xs text-[#737373]">
          按模型拆分的精确成本（全量累计，不受上方日期范围影响）。「倍率」是你在定价管理里给该模型设的加价倍数（1=按官方原价）；
          成本已按各家的计费口径自动校准（OpenAI 系列的 input 已扣除缓存部分，避免重复计费）。
        </p>
        <div v-if="!byModel.length" class="py-4 text-center text-[#737373]">暂无数据</div>
        <div v-else class="overflow-x-auto rounded-lg border border-[#2a2a2a]">
          <table class="w-full text-xs">
            <thead class="bg-[#222] text-[#a3a3a3]">
              <tr>
                <th class="px-3 py-2 text-left font-medium">模型</th>
                <th class="px-3 py-2 text-left font-medium">Provider</th>
                <th class="px-3 py-2 text-right font-medium">请求</th>
                <th class="px-3 py-2 text-right font-medium">真实 Tokens</th>
                <th class="px-3 py-2 text-right font-medium">总成本</th>
                <th class="px-3 py-2 text-right font-medium">均价/请求</th>
                <th class="px-3 py-2 text-right font-medium">倍率</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="m in byModel"
                :key="m.modelId"
                class="border-t border-[#2a2a2a] hover:bg-[#1f1f1f]"
              >
                <td class="px-3 py-2 font-mono">{{ m.modelName || m.modelId }}</td>
                <td class="px-3 py-2 text-[#8f8f8f]">{{ m.provider || "—" }}</td>
                <td class="px-3 py-2 text-right tabular-nums">{{ fmtNum(m.providerCalls) }}</td>
                <td class="px-3 py-2 text-right tabular-nums">{{ fmtNum(m.realTotalTokens) }}</td>
                <td class="px-3 py-2 text-right tabular-nums text-[#10AD5D]">{{ fmtUsd(m.totalCost) }}</td>
                <td class="px-3 py-2 text-right tabular-nums">{{ fmtUsd(m.avgCostPerRequest) }}</td>
                <td class="px-3 py-2 text-right tabular-nums text-[#8f8f8f]">{{ m.costMultiplier }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <!-- Provider 统计 -->
      <section class="mb-6">
        <h2 class="mb-3 text-base font-medium text-[#e5e5e5]">Provider 统计</h2>
        <div v-if="!byProvider.length" class="py-4 text-center text-[#737373]">暂无数据</div>
        <div v-else class="overflow-x-auto rounded-lg border border-[#2a2a2a]">
          <table class="w-full text-xs">
            <thead class="bg-[#222] text-[#a3a3a3]">
              <tr>
                <th class="px-3 py-2 text-left font-medium">Provider</th>
                <th class="px-3 py-2 text-right font-medium">请求</th>
                <th class="px-3 py-2 text-right font-medium">真实 Tokens</th>
                <th class="px-3 py-2 text-right font-medium">总成本</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="p in byProvider"
                :key="p.provider"
                class="border-t border-[#2a2a2a] hover:bg-[#1f1f1f]"
              >
                <td class="px-3 py-2">{{ p.provider }}</td>
                <td class="px-3 py-2 text-right tabular-nums">{{ fmtNum(p.providerCalls) }}</td>
                <td class="px-3 py-2 text-right tabular-nums">{{ fmtNum(p.realTotalTokens) }}</td>
                <td class="px-3 py-2 text-right tabular-nums text-[#10AD5D]">{{ fmtUsd(p.totalCost) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <!-- 请求日志 -->
      <section class="mb-6">
        <div class="mb-3 flex items-center justify-between">
          <h2 class="text-base font-medium text-[#e5e5e5]">请求日志</h2>
          <span class="text-xs text-[#737373]">
            当前范围 {{ recentEvents.length }} 条 / 共 {{ recentEventsAll.length }} 条
          </span>
        </div>
        <div v-if="!recentEvents.length" class="py-4 text-center text-[#737373]">该范围内暂无数据</div>
        <div v-else>
          <div class="max-h-96 overflow-y-auto rounded-lg border border-[#2a2a2a]">
            <table class="w-full text-xs">
              <thead class="sticky top-0 bg-[#222] text-[#a3a3a3]">
                <tr>
                  <th class="px-3 py-2 text-left font-medium">时间</th>
                  <th class="px-3 py-2 text-left font-medium">模型</th>
                  <th class="px-3 py-2 text-left font-medium">Provider</th>
                  <th class="px-3 py-2 text-right font-medium">输入</th>
                  <th class="px-3 py-2 text-right font-medium">输出</th>
                  <th class="px-3 py-2 text-right font-medium">缓存读</th>
                  <th class="px-3 py-2 text-right font-medium">成本</th>
                  <th class="px-3 py-2 text-center font-medium">状态</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="e in eventPaged"
                  :key="e.eventId"
                  :class="['border-t border-[#2a2a2a] hover:bg-[#1f1f1f]', e.calibrationAnomaly ? 'bg-amber-950/20' : '']"
                >
                  <td class="px-3 py-2 whitespace-nowrap text-[#8f8f8f]">{{ fmtTime(e.at) }}</td>
                  <td class="px-3 py-2 font-mono">{{ e.modelName || e.modelId || "—" }}</td>
                  <td class="px-3 py-2 text-[#8f8f8f]">{{ e.provider || "—" }}</td>
                  <td class="px-3 py-2 text-right tabular-nums">{{ fmtNum(e.inputTokens) }}</td>
                  <td class="px-3 py-2 text-right tabular-nums">{{ fmtNum(e.outputTokens) }}</td>
                  <td class="px-3 py-2 text-right tabular-nums">{{ fmtNum(e.cacheReadTokens) }}</td>
                  <td class="px-3 py-2 text-right tabular-nums text-[#10AD5D]">{{ fmtUsd(e.costUSD) }}</td>
                  <td class="px-3 py-2 text-center">
                    <span
                      v-if="e.calibrationAnomaly"
                      class="text-amber-400"
                      title="input < 缓存读取+缓存写入，provider 返回的 input 口径可能未包含缓存"
                    >⚠</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <!-- 分页 -->
          <div class="mt-2 flex items-center justify-between text-xs text-[#737373]">
            <span>每页 {{ eventPageSize }} 条，第 {{ eventPage }} / {{ eventTotalPages }} 页</span>
            <div class="flex items-center gap-2">
              <button
                class="rounded border border-[#3f3f3f] px-2 py-0.5 disabled:opacity-40"
                :disabled="eventPage <= 1"
                @click="goEventPage(-1)"
              >
                上一页
              </button>
              <button
                class="rounded border border-[#3f3f3f] px-2 py-0.5 disabled:opacity-40"
                :disabled="eventPage >= eventTotalPages"
                @click="goEventPage(1)"
              >
                下一页
              </button>
            </div>
          </div>
          <p class="mt-2 text-xs text-[#737373]">
            recent_events 仅保留最近 500 条；历史累计见上方 Hero 与模型/Provider 统计。
          </p>
        </div>
      </section>
    </div>
  </div>
</template>
