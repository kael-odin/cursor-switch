// modelsdev.go 实现 fetch-models 的 models.dev 在线回退：当内置静态上下文窗口表
// 未命中时，可选地从 models.dev 公开 API（https://models.dev/models.json）反查。
//
// 设计：
//   - models.dev 返回 { "<lab>/<model>": { limit: { context: N } } } 大对象。
//     key 带 lab 前缀（如 anthropic/claude-opus-5），需用候选匹配对齐 provider
//     返回的裸 modelID（去 lab 前缀，复用 pricing 候选算法）。
//   - 进程级内存缓存 + 落盘 TTL 缓存（~/.cursor-local-assistant-v2/models-dev-cache.json，
//     默认 7 天），避免每次 fetch 都打 models.dev。
//   - 全程 best-effort：网络失败/解析失败/超时一律静默返回 0，不阻断模型拉取主流程。
//   - 离线/未命中时仍由静态表 + resolver 200K 默认值兜底。
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/netproxy"
)

const (
	modelsDevURL         = "https://models.dev/models.json"
	modelsDevTimeout     = 10 * time.Second
	modelsDevMaxBodyBytes = 4 << 20 // 4MiB，models.dev 全量模型约 1-2MB
	modelsDevCacheTTL    = 7 * 24 * time.Hour
	modelsDevCacheFile   = "models-dev-cache.json"
	modelsDevUserAgent   = "cursor-switch/1.0 model-fetch"
)

// modelsDevEntry 是 models.dev 单条模型的读取视图（只取需要的字段）。
type modelsDevEntry struct {
	Limit struct {
		Context int64 `json:"context"`
		Output  int64 `json:"output"`
	} `json:"limit"`
}

// modelsDevCacheDoc 是落盘缓存结构。
type modelsDevCacheDoc struct {
	FetchedAt time.Time                  `json:"fetched_at"`
	Models    map[string]modelsDevEntry  `json:"models"`
}

var (
	modelsDevMu       sync.Mutex
	modelsDevMemCache map[string]modelsDevEntry
	modelsDevMemAt    time.Time
)

// lookupContextWindowOnline 用 models.dev 在线反查上下文窗口。best-effort，失败返回 0。
// 只在静态表 miss 时调用。命中会更新内存缓存（落盘缓存由首次拉取时统一写）。
//
// 匹配策略：models.dev 的 key 是 "<lab>/<id>"（如 anthropic/claude-opus-5）。
// 查询时对 query modelID 和每个 models.dev key 的裸 id 都生成候选序列，
// 取两者候选集的交集（任一候选相等即命中）。这样 query "claude-opus-5-20260101"
// 与 key "anthropic/claude-opus-5" 的候选都含 "claude-opus-5"，命中。
func lookupContextWindowOnline(modelID string, cacheRoot string) int64 {
	if strings.TrimSpace(modelID) == "" {
		return 0
	}
	doc, ok := loadModelsDevCache(cacheRoot)
	if !ok || len(doc.Models) == 0 {
		return 0
	}
	queryCandidates := serverconfig.MatchPricingCandidates(modelID)
	if len(queryCandidates) == 0 {
		return 0
	}
	querySet := make(map[string]struct{}, len(queryCandidates))
	for _, c := range queryCandidates {
		querySet[c] = struct{}{}
	}
	// 第一遍：精确匹配（裸 id 直接相等），最快。
	for key, entry := range doc.Models {
		if entry.Limit.Context <= 0 {
			continue
		}
		if _, ok := querySet[stripLabPrefix(key)]; ok {
			return entry.Limit.Context
		}
	}
	// 第二遍：候选交集（处理日期/版本/努力后缀变体）。
	for key, entry := range doc.Models {
		if entry.Limit.Context <= 0 {
			continue
		}
		bare := stripLabPrefix(key)
		for _, c := range serverconfig.MatchPricingCandidates(bare) {
			if _, ok := querySet[c]; ok {
				return entry.Limit.Context
			}
		}
	}
	return 0
}

// stripLabPrefix 去掉 models.dev key 的 lab 前缀："anthropic/claude-opus-5" → "claude-opus-5"。
// 无 "/" 时原样返回。
func stripLabPrefix(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if idx := strings.LastIndex(key, "/"); idx >= 0 {
		return key[idx+1:]
	}
	return key
}

// loadModelsDevCache 优先用内存缓存，其次落盘缓存（未过期则用），最后在线拉取。
// 拉取成功后写内存 + 落盘。返回 ok=false 表示无可用数据（调用方回退到 0）。
func loadModelsDevCache(cacheRoot string) (modelsDevCacheDoc, bool) {
	modelsDevMu.Lock()
	defer modelsDevMu.Unlock()

	now := time.Now()
	// 内存缓存有效
	if modelsDevMemCache != nil && now.Sub(modelsDevMemAt) < modelsDevCacheTTL {
		return modelsDevCacheDoc{FetchedAt: modelsDevMemAt, Models: modelsDevMemCache}, true
	}

	// 落盘缓存有效
	if doc, ok := readModelsDevCacheDisk(cacheRoot); ok && now.Sub(doc.FetchedAt) < modelsDevCacheTTL {
		modelsDevMemCache = doc.Models
		modelsDevMemAt = doc.FetchedAt
		return doc, true
	}

	// 在线拉取（best-effort，失败不报错）
	body, err := fetchModelsDevOnline()
	if err != nil || len(body) == 0 {
		// 拉取失败但落盘有旧缓存（即使过期）仍凑合用
		if doc, ok := readModelsDevCacheDisk(cacheRoot); ok && len(doc.Models) > 0 {
			modelsDevMemCache = doc.Models
			modelsDevMemAt = doc.FetchedAt
			return doc, true
		}
		return modelsDevCacheDoc{}, false
	}

	models := parseModelsDevBody(body)
	if len(models) == 0 {
		return modelsDevCacheDoc{}, false
	}
	doc := modelsDevCacheDoc{FetchedAt: now, Models: models}
	modelsDevMemCache = models
	modelsDevMemAt = now
	_ = writeModelsDevCacheDisk(cacheRoot, doc) // 落盘失败不影响内存命中
	return doc, true
}

// fetchModelsDevOnline 拉取 models.dev 全量模型 JSON。
func fetchModelsDevOnline() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelsDevTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", modelsDevUserAgent)
	req.Header.Set("Accept", "application/json")
	client := netproxy.NewHTTPClient(modelsDevTimeout)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models.dev status=%d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, modelsDevMaxBodyBytes))
}

// parseModelsDevBody 解析 models.dev 的 {key: entry} 大对象。
func parseModelsDevBody(body []byte) map[string]modelsDevEntry {
	var raw map[string]modelsDevEntry
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	// 过滤掉无 context 的条目，减小内存占用
	out := make(map[string]modelsDevEntry, len(raw))
	for k, v := range raw {
		if v.Limit.Context > 0 {
			out[strings.ToLower(strings.TrimSpace(k))] = v
		}
	}
	return out
}

// readModelsDevCacheDisk 从 cacheRoot 读落盘缓存。
func readModelsDevCacheDisk(cacheRoot string) (modelsDevCacheDoc, bool) {
	root := strings.TrimSpace(cacheRoot)
	if root == "" {
		return modelsDevCacheDoc{}, false
	}
	body, err := os.ReadFile(filepath.Join(root, modelsDevCacheFile))
	if err != nil {
		return modelsDevCacheDoc{}, false
	}
	var doc modelsDevCacheDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return modelsDevCacheDoc{}, false
	}
	if doc.Models == nil {
		return modelsDevCacheDoc{}, false
	}
	return doc, true
}

// writeModelsDevCacheDisk 写落盘缓存。
func writeModelsDevCacheDisk(cacheRoot string, doc modelsDevCacheDoc) error {
	root := strings.TrimSpace(cacheRoot)
	if root == "" {
		return nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, modelsDevCacheFile), body, 0o644)
}
