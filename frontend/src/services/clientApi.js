import {
  GetState,
  LoadUserConfig,
  SaveUserConfig,
  StartProxy,
  StopProxy,
} from "@bindings/cursor/internal/bridge/proxyservice.js";
import { GetHomeMetricsSummary } from "@bindings/cursor/internal/bridge/metricsservice.js";
import {
  CheckForUpdates,
  GetAppVersion,
  InstallReadyUpdate,
  GetModelEditorContext,
  OpenConfigWindow,
  OpenHistoryWindow,
  OpenModelConfigWindow,
  OpenModelEditorWindow,
} from "@bindings/cursor/internal/bridge/windowservice.js";
import { Call } from "@wailsio/runtime";

const API_LOG_PREFIX = "[clientApi]";
const PROXY_SERVICE_NAME = "cursor/internal/bridge.ProxyService";
const METRICS_SERVICE_NAME = "cursor/internal/bridge.MetricsService";

// F-37：生产构建移除 API 日志。这些 console 调用会把 SaveUserConfig / testModelAdapter /
// fetchProviderModels 等的完整 payload（含 apiKey / customHeadersJSON / Authorization）和
// 原始响应打印到 WebView 控制台——本机调试面虽不外传，但生产 WebView 不应留存。
// 仅 dev 构建保留日志；prod 构建经 import.meta.env.DEV 静态替换为 false 后，
// Vite tree-shaking 会整段删除 logSuccess/logError 调用。
const LOG_API = import.meta.env.DEV;

function logSuccess(name, payload, result) {
  if (!LOG_API) return;
  console.log(`${API_LOG_PREFIX} ${name} response`, {
    payload,
    result,
  });
}

function logError(name, payload, error) {
  if (!LOG_API) return;
  console.error(`${API_LOG_PREFIX} ${name} error`, {
    payload,
    error,
  });
}

function withApiLogging(name, payload, runner) {
  if (!LOG_API) {
    // 生产构建：直接跑 runner，不记录任何 payload/result，避免敏感字段进控制台。
    return Promise.resolve().then(runner);
  }
  return Promise.resolve()
    .then(() => runner())
    .then((result) => {
      logSuccess(name, payload, result);
      return result;
    })
    .catch((error) => {
      logError(name, payload, error);
      throw error;
    });
}

export function loadUserConfig() {
  return withApiLogging("LoadUserConfig", undefined, () => LoadUserConfig());
}

export function saveUserConfig(payload) {
  return withApiLogging("SaveUserConfig", payload, () => SaveUserConfig(payload));
}

export function getProxyState() {
  return withApiLogging("GetState", undefined, () => GetState());
}

export function getHomeMetricsSummary() {
  return withApiLogging("GetHomeMetricsSummary", undefined, () => GetHomeMetricsSummary());
}

// 使用统计仪表盘：返回 totals/daily/byModel/byProvider/recentEvents 完整数据。
export function getUsageDashboard() {
  return withApiLogging("GetUsageDashboard", undefined, () =>
    Call.ByName(`${METRICS_SERVICE_NAME}.GetUsageDashboard`),
  );
}

// 清零使用统计：把 usage.json 重写为空文档。不可逆，前端调用前应弹确认框。
export function resetUsageStats() {
  return withApiLogging("ResetUsageStats", undefined, () =>
    Call.ByName(`${METRICS_SERVICE_NAME}.ResetUsageStats`),
  );
}

// getCursorAccountStatus 只读探测 Cursor 客户端是否登录了官方账号。
// 用于前端"Tab 补全依赖官方账号"的缺失告警（能力缺失标注）。
export function getCursorAccountStatus() {
  return withApiLogging("GetCursorAccountStatus", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.GetCursorAccountStatus`),
  );
}

// 定价管理（照搬 cc-switch 的成本定价：预设价目 + 自定义 CRUD + 倍率）
export function getPricingSnapshot() {
  return withApiLogging("GetPricingSnapshot", undefined, () =>
    Call.ByName(`${METRICS_SERVICE_NAME}.GetPricingSnapshot`),
  );
}

export function updateModelPricing(pricing) {
  return withApiLogging("UpdateModelPricing", pricing, () =>
    Call.ByName(`${METRICS_SERVICE_NAME}.UpdateModelPricing`, pricing),
  );
}

export function deleteModelPricing(modelId) {
  return withApiLogging("DeleteModelPricing", modelId, () =>
    Call.ByName(`${METRICS_SERVICE_NAME}.DeleteModelPricing`, modelId),
  );
}

export function restoreDefaultPricing(modelId) {
  return withApiLogging("RestoreDefaultPricing", modelId, () =>
    Call.ByName(`${METRICS_SERVICE_NAME}.RestoreDefaultPricing`, modelId),
  );
}

export function setDefaultCostMultiplier(value) {
  return withApiLogging("SetDefaultCostMultiplier", value, () =>
    Call.ByName(`${METRICS_SERVICE_NAME}.SetDefaultCostMultiplier`, value),
  );
}

export function startProxyService() {
  return withApiLogging("StartProxy", undefined, () => StartProxy());
}

export function stopProxyService() {
  return withApiLogging("StopProxy", undefined, () => StopProxy());
}

export function openLogsDirectory() {
  return withApiLogging("OpenHistoryWindow", undefined, () => OpenHistoryWindow());
}

export function openConfigWindow() {
  return withApiLogging("OpenConfigWindow", undefined, () => OpenConfigWindow());
}

export function getAppVersion() {
  return withApiLogging("GetAppVersion", undefined, () => GetAppVersion());
}

export function checkForUpdates() {
  return withApiLogging("CheckForUpdates", undefined, () => CheckForUpdates());
}

export function installReadyUpdate() {
  return withApiLogging("InstallReadyUpdate", undefined, () => InstallReadyUpdate());
}

export function openModelConfig() {
  return withApiLogging("OpenModelConfigWindow", undefined, () => OpenModelConfigWindow());
}

const WINDOW_SERVICE_NAME = "cursor/internal/bridge.WindowService";

export function openPricing() {
  return withApiLogging("OpenPricingWindow", undefined, () =>
    Call.ByName(`${WINDOW_SERVICE_NAME}.OpenPricingWindow`),
  );
}

export function openConfigWebview() {
  return withApiLogging("OpenConfigWebviewWindow", undefined, () =>
    Call.ByName(`${WINDOW_SERVICE_NAME}.OpenConfigWebviewWindow`),
  );
}

export function openModelEditor(index, adapterJSON) {
  return withApiLogging("OpenModelEditorWindow", { index, adapterJSON }, () =>
    OpenModelEditorWindow(index, adapterJSON),
  );
}

export function getModelEditorContext() {
  return withApiLogging("GetModelEditorContext", undefined, () => GetModelEditorContext());
}

export function testModelAdapter(adapter) {
  return Call.ByName(`${PROXY_SERVICE_NAME}.TestModelAdapter`, adapter).then(
    (result) => {
      logSuccess("TestModelAdapter", adapter, result);
      return result;
    },
    (error) => {
      logError("TestModelAdapter", adapter, error);
      throw error;
    },
  );
}

export function getModelAdapterTestResults() {
  return withApiLogging("GetModelAdapterTestResults", undefined, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.GetModelAdapterTestResults`),
  );
}

// 探活 Tab 服务 / WebSearch / WebFetch 配置：把当前表单值打包传入，后端按 kind 探活。
// 与 testModelAdapter 同语义——测的是表单当前值（未必已保存），成功/失败给友好摘要。
export function testWebTools(request) {
  return withApiLogging("TestWebTools", request, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.TestWebTools`, request),
  );
}

// 一键获取模型列表：根据已填写的 baseURL+apiKey+type 调用 provider 的 /v1/models。
// adapter 只需 type/baseURL/apiKey（modelID 可空，拉列表发生在选模型之前）。
export function fetchProviderModels(adapter) {
  return withApiLogging("FetchProviderModels", adapter, () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.FetchProviderModels`, adapter),
  );
}
