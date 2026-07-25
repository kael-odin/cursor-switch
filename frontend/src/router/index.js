import { createRouter, createWebHashHistory } from "vue-router";
import { localized } from "@/i18n/runtime";
import Home from "@/views/Home.vue";
import ModelConfig from "@/views/ModelConfig.vue";
import ModelEditor from "@/views/ModelEditor.vue";
import PricingPanel from "@/views/PricingPanel.vue";

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {
      path: "/",
      component: Home,
      meta: { showIcon: true, title: localized("991e374fce0f4492", "Cursor助手"), directlyClose: false },
    },
    {
      path: "/model-config",
      component: ModelConfig,
      meta: { showIcon: false, title: localized("8cbcf741e727dbf7", "模型配置"), directlyClose: true },
    },
    {
      path: "/model-editor",
      component: ModelEditor,
      meta: { showIcon: false, title: localized("7bf8e2c07e084d09", "模型编辑"), directlyClose: true },
    },
    {
      // title 必须是原始字符串或单层 LocalizedText，切勿传嵌套 LocalizedText 当 fallback：
      // localized(a, localized(b, c)) 会让 toString() 返回对象，触发
      // Vue toDisplayString 的 "Cannot convert object to primitive value" 报错。
      path: "/pricing",
      component: PricingPanel,
      meta: { showIcon: false, title: "成本定价", directlyClose: true },
    },
  ],
});

export default router;
