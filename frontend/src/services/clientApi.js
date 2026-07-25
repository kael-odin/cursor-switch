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

function logSuccess(name, payload, result) {
  console.log(`${API_LOG_PREFIX} ${name} response`, {
    payload,
    result,
  });
}

function logError(name, payload, error) {
  console.error(`${API_LOG_PREFIX} ${name} error`, {
    payload,
    error,
  });
}

function withApiLogging(name, payload, runner) {
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

const METRICS_SERVICE_NAME = "cursor/internal/bridge.MetricsService";

export function getTokenPricing() {
  return withApiLogging("GetTokenPricing", undefined, () =>
    Call.ByName(`${METRICS_SERVICE_NAME}.GetTokenPricing`),
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
