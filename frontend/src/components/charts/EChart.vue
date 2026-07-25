<script setup>
// ECharts 封装：按需引入堆叠面积图所需组件，避免全量打包。
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import {
  GridComponent,
  LegendComponent,
  TooltipComponent,
  DataZoomComponent,
} from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";

echarts.use([
  LineChart,
  GridComponent,
  LegendComponent,
  TooltipComponent,
  DataZoomComponent,
  CanvasRenderer,
]);

const props = defineProps({
  option: { type: Object, required: true },
  height: { type: String, default: "320px" },
});

const containerRef = ref(null);
const chart = shallowRef(null);

function render() {
  if (!chart.value || !props.option) return;
  chart.value.setOption(props.option, true);
}

function resize() {
  chart.value?.resize();
}

onMounted(() => {
  chart.value = echarts.init(containerRef.value, "dark");
  render();
  window.addEventListener("resize", resize);
});

onBeforeUnmount(() => {
  window.removeEventListener("resize", resize);
  chart.value?.dispose();
  chart.value = null;
});

watch(() => props.option, render, { deep: true });
</script>

<template>
  <div ref="containerRef" class="w-full" :style="{ height }"></div>
</template>
