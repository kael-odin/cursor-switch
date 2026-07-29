// vitest 独立配置——故意不复用 vite.config.js：后者挂载 @wailsio/runtime/plugins/vite
// 等 wails 插件，在纯 Node 测试环境会拉起 wails 绑定导致加载失败。这里只设最小测试环境：
// @ / @bindings 别名（与 vite.config / jsconfig 一致）+ happy-dom（提供 localStorage/window，
// appState.js 模块加载时 migrateLegacyStorageKeys 与 loadCachedState 依赖之）。
//
// 审计 L3：normalizer 是 config 层契约边界（F-02/L5/v2.0.3 per-namespace 等改动都改过它们），
// 锁住回归。生产代码不动，仅加测试。
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
      "@bindings": path.resolve(__dirname, "./bindings"),
    },
  },
  test: {
    environment: "happy-dom",
    globals: true,
    include: ["src/**/*.test.js"],
  },
});
