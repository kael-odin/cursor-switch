package modelchannel

import "strings"

func IsMetaModelAlias(modelRef string) bool {
	switch strings.ToLower(strings.TrimSpace(modelRef)) {
	case "fast", "default", "auto":
		return true
	default:
		return false
	}
}

func ResolveAdapterIndex[T any](adapters []T, requestedModelRef string, id func(T) string, providerModelID func(T) string, legacyIDs ...func(T) string) (int, bool) {
	if len(adapters) == 0 {
		return -1, false
	}

	targetModelRef := strings.TrimSpace(requestedModelRef)
	if targetModelRef == "" || IsMetaModelAlias(targetModelRef) {
		targetModelRef = strings.TrimSpace(id(adapters[0]))
	}
	if targetModelRef == "" {
		return -1, false
	}

	for index, adapter := range adapters {
		if strings.TrimSpace(id(adapter)) == targetModelRef {
			return index, true
		}
	}

	legacyIndex := -1
	for _, legacyID := range legacyIDs {
		if legacyID == nil {
			continue
		}
		for index, adapter := range adapters {
			if strings.TrimSpace(legacyID(adapter)) != targetModelRef {
				continue
			}
			if legacyIndex >= 0 && legacyIndex != index {
				return -1, false
			}
			legacyIndex = index
		}
	}
	if legacyIndex >= 0 {
		return legacyIndex, true
	}

	fallbackIndex := -1
	for index, adapter := range adapters {
		if strings.TrimSpace(providerModelID(adapter)) != targetModelRef {
			continue
		}
		if fallbackIndex >= 0 {
			return -1, false
		}
		fallbackIndex = index
	}
	if fallbackIndex < 0 {
		return -1, false
	}
	return fallbackIndex, true
}

// ResolveAdapterIndexes 是 ResolveAdapterIndex 的复数版（B2 候选链）。
// 返回**全部**匹配 index，按精确 ID → legacy ID → providerModelID 顺序去重收集，
// 不再"多命中即拒绝"——同 modelID 多 adapter 视为合法的 failover 候选链。
//
// 顺序保证：精确 ID 命中的候选排最前（用户显式指定），其次 legacy ID，最后 providerModelID。
// 同一 index 只入列一次。空列表/无命中返回空切片。
func ResolveAdapterIndexes[T any](adapters []T, requestedModelRef string, id func(T) string, providerModelID func(T) string, legacyIDs ...func(T) string) []int {
	if len(adapters) == 0 {
		return nil
	}

	targetModelRef := strings.TrimSpace(requestedModelRef)
	if targetModelRef == "" || IsMetaModelAlias(targetModelRef) {
		targetModelRef = strings.TrimSpace(id(adapters[0]))
	}
	if targetModelRef == "" {
		return nil
	}

	seen := make(map[int]struct{})
	candidates := make([]int, 0, 4)

	add := func(index int) {
		if _, ok := seen[index]; ok {
			return
		}
		seen[index] = struct{}{}
		candidates = append(candidates, index)
	}

	// 1. 精确 ID 匹配（可能有多个 adapter 用同一显式 ID，虽然不常见）。
	for index, adapter := range adapters {
		if strings.TrimSpace(id(adapter)) == targetModelRef {
			add(index)
		}
	}

	// 2. legacy ID 匹配。
	for _, legacyID := range legacyIDs {
		if legacyID == nil {
			continue
		}
		for index, adapter := range adapters {
			if strings.TrimSpace(legacyID(adapter)) == targetModelRef {
				add(index)
			}
		}
	}

	// 3. providerModelID fallback。
	for index, adapter := range adapters {
		if strings.TrimSpace(providerModelID(adapter)) == targetModelRef {
			add(index)
		}
	}

	return candidates
}
