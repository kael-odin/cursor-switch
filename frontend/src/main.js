import { createApp } from "vue";
import ResizeObserver from "resize-observer-polyfill";
import App from "@/App.vue";
import { installI18nRuntime } from "@/i18n/runtime";
import router from "@/router";
import { bootstrapAppState } from "@/state/appState";
import "@/style/global.css";
import "@/style/tailwind.css";

if (typeof window !== "undefined" && typeof window.ResizeObserver === "undefined") {
  window.ResizeObserver = ResizeObserver;
}

function updateFlexGapSupportClass() {
  if (typeof document === "undefined" || !document.body) {
    return;
  }
  const flex = document.createElement("div");
  flex.style.position = "absolute";
  flex.style.visibility = "hidden";
  flex.style.display = "flex";
  flex.style.flexDirection = "column";
  flex.style.rowGap = "1px";
  flex.appendChild(document.createElement("div"));
  flex.appendChild(document.createElement("div"));
  document.body.appendChild(flex);
  document.documentElement.classList.toggle("no-flex-gap", flex.scrollHeight !== 1);
  flex.parentNode?.removeChild(flex);
}

updateFlexGapSupportClass();

const app = createApp(App);
installI18nRuntime(app);
app.use(router);
// 全局错误兜底：任何渲染/JS 异常都用独立 overlay 无条件显示，避免整页黑屏无从排查。
function showErrorOverlay(label, err, info) {
  try {
    const msg = String((err && (err.stack || err.message)) || err || "").replace(/</g, "&lt;");
    let overlay = document.getElementById("__err_overlay");
    if (!overlay) {
      overlay = document.createElement("div");
      overlay.id = "__err_overlay";
      overlay.style.cssText =
        "position:fixed;inset:0;z-index:2147483647;background:#1a1a1a;color:#f87171;font-family:monospace;font-size:12px;white-space:pre-wrap;padding:44px 16px 16px;overflow:auto;";
      document.body.appendChild(overlay);
    }
    overlay.textContent = `[${label}] ${err && err.message ? err.message : err}\n\n${String(info || "")}\n\n${msg}`;
  } catch (_e) {
    /* overlay 本身失败也不阻断 */
  }
}
app.config.errorHandler = (err, instance, info) => {
  console.error("[vue errorHandler]", err, info);
  showErrorOverlay("vue", err, info);
};
if (typeof window !== "undefined") {
  window.addEventListener("error", (e) => showErrorOverlay("window.onerror", e.error || e.message, ""));
  window.addEventListener("unhandledrejection", (e) => showErrorOverlay("promise", e.reason, ""));
}
app.mount("#root");

bootstrapAppState().catch(() => {
  // 启动阶段失败时保持界面可用，错误在业务交互中再提示。
});
