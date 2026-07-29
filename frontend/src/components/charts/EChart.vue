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
// P2-7：ResizeObserver 监听容器尺寸变化重绘。window.resize 只在窗口缩放时触发，
// 容器因侧栏伸缩/日期范围切换/滚动条出现导致尺寸变化时图表不自适应。
let resizeObserver = null;

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
  // main.js 已 polyfill ResizeObserver，安全使用。
  if (typeof ResizeObserver !== "undefined" && containerRef.value) {
    resizeObserver = new ResizeObserver(() => {
      chart.value?.resize();
    });
    resizeObserver.observe(containerRef.value);
  }
});

onBeforeUnmount(() => {
  window.removeEventListener("resize", resize);
  if (resizeObserver) {
    resizeObserver.disconnect();
    resizeObserver = null;
  }
  chart.value?.dispose();
  chart.value = null;
});

watch(() => props.option, render, { deep: true });
</script>

<template>
  <div ref="containerRef" class="w-full" :style="{ height }"></div>
</template>
