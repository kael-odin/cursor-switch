// migrateStorage 把旧品牌 localStorage key（cursor-client:）一次性迁移到 cursor-switch: 前缀。
// 旧 key 属非保留项（审计 L5），改名会丢用户缓存态——本迁移把运行态/语言偏好搬过来，
// 升级后用户界面状态与语言选择保持不变。幂等：新 key 已有值则不动，迁移后清旧 key。
//
// 形如 migrate({ oldKey, newKey })：若新 key 无值且旧 key 有值，复制后删旧 key；否则仅删旧 key（新值优先）。
// 任何异常吞掉并回退（localStorage 不可用 / JSON 损坏 / quota），绝不阻断 UI 启动。
function canUseLocalStorage() {
  return typeof window !== "undefined" && typeof window.localStorage !== "undefined";
}

// migrateStorageEntry 把单个旧 key 的值搬到新 key。返回是否真的执行了搬运。
// 行为：新 key 已有值 → 保留新值，仅清旧 key（用户在新版已产生新状态，以新值为准）；
//       新 key 无值 + 旧 key 有值 → 复制旧值到新 key 再清旧 key；
//       旧 key 无值 → no-op。
function migrateStorageEntry(oldKey, newKey) {
  if (!canUseLocalStorage()) {
    return false;
  }
  try {
    const oldValue = window.localStorage.getItem(oldKey);
    if (oldValue === null) {
      return false;
    }
    const newValue = window.localStorage.getItem(newKey);
    if (newValue === null) {
      // 新 key 无值：把旧值搬过来。
      window.localStorage.setItem(newKey, oldValue);
    }
    // 无论新 key 是否已有值，旧 key 都清掉——旧品牌前缀的 key 不再保留。
    window.localStorage.removeItem(oldKey);
    return true;
  } catch {
    // quota / 损坏 / 隐私模式：静默回退，UI 走默认态。
    return false;
  }
}

// migrateLegacyStorageKeys 统一入口：把所有已知的旧品牌 key 迁到新前缀。
// 在 appState 与 i18n runtime 初始化早期调用一次（各自幂等，重复调无副作用）。
export function migrateLegacyStorageKeys() {
  // appState 运行态：cursor-client:runtime-state:v2 → cursor-switch:runtime-state:v2
  migrateStorageEntry("cursor-client:runtime-state:v2", "cursor-switch:runtime-state:v2");
  // i18n 语言偏好：cursor-client:locale:v1 → cursor-switch:locale:v1
  migrateStorageEntry("cursor-client:locale:v1", "cursor-switch:locale:v1");
  // i18n 语言来源标记：cursor-client:locale-source:v1 → cursor-switch:locale-source:v1
  migrateStorageEntry("cursor-client:locale-source:v1", "cursor-switch:locale-source:v1");
}
