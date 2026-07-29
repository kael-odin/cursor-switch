// webfetch_cache.go 实现 WebFetch 正文的进程内 LRU 缓存。
//
// 目的:同一 URL 短期内反复 WebFetch(模型在多步推理里反复抓同一页面,或多个
// 并发 stream 命中同一文档)时,免重复外网请求 + readability 解析,降延迟与
// 被封概率。免费、零部署、惠及所有用户(无需配置)。
//
// 设计:
//   - 容量按字节数计(默认 4MiB 总量),LRU 淘汰最久未访问条目。
//   - 每条目带 TTL(默认 10min),过期即视为未命中,重新抓取——避免给模型陈旧正文。
//   - 键为规范化 URL(去 fragment、去默认端口、query 按字典序排序),使不同写法的
//     同一资源命中同一缓存项。
//   - 值为 executeWebFetch 的最终 payload(已转 markdown + 截断),命中即原样返回,
//     跳过 HTTP + readability 全流程。
//   - 失败结果不缓存(让下游每次重试,而非把瞬时网络错误固化)。
//
// 并发安全:一把 sync.Mutex 串行所有读写(LRU 命中/淘汰是 O(1)~O(陈旧量),WebFetch
// 本身是秒级外网 IO,锁竞争可忽略)。
package interaction

import (
	"container/list"
	"net/url"
	"strings"
	"sync"
	"time"
)

// webFetchCache 默认参数。
const (
	webFetchCacheTTL      = 10 * time.Minute
	webFetchCacheMaxBytes = 4 * 1024 * 1024
)

// webFetchCacheEntry 是一个缓存条目。
type webFetchCacheEntry struct {
	key       string
	payload   string
	size      int       // = len(payload)
	createdAt time.Time // 写入时刻,用于 TTL 判定
	elem      *list.Element
}

// webFetchCache 是按字节数上限淘汰的 TTL+LRU 缓存。零值即可用(进程级单例)。
type webFetchCache struct {
	mu       sync.Mutex
	items    map[string]*webFetchCacheEntry
	order    *list.List // 前=最近使用;尾=最久未使用,优先淘汰
	totalBytes int
	maxBytes int
	ttl      time.Duration
}

// globalWebFetchCache 是进程级单例,所有 Bridge 实例共享(交互桥是无状态复用的)。
var globalWebFetchCache = newWebFetchCache(webFetchCacheMaxBytes, webFetchCacheTTL)

// newWebFetchCache 构造一个指定上限与 TTL 的缓存。
func newWebFetchCache(maxBytes int, ttl time.Duration) *webFetchCache {
	return &webFetchCache{
		items:    make(map[string]*webFetchCacheEntry),
		order:    list.New(),
		maxBytes: maxBytes,
		ttl:      ttl,
	}
}

// get 命中返回 payload 与 true;未命中或过期返回 ""/false(过期项在此淘汰)。
func (c *webFetchCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		return "", false
	}
	if time.Since(entry.createdAt) > c.ttl {
		c.evictLocked(entry)
		return "", false
	}
	// 命中:提到队首(MRU)。
	c.order.MoveToFront(entry.elem)
	return entry.payload, true
}

// put 写入一条。超容量时从队尾(LRU)淘汰直至够小。空 payload 不缓存。
func (c *webFetchCache) put(key, payload string) {
	if strings.TrimSpace(payload) == "" {
		return
	}
	size := len(payload)
	if size > c.maxBytes {
		return // 单条目比总容量还大,不缓存(避免一条独占)。
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// 已存在则先淘汰旧的(随后以新值重插)。
	if old, ok := c.items[key]; ok {
		c.evictLocked(old)
	}
	entry := &webFetchCacheEntry{
		key:       key,
		payload:   payload,
		size:      size,
		createdAt: time.Now(),
	}
	entry.elem = c.order.PushFront(entry)
	c.items[key] = entry
	c.totalBytes += size
	for c.totalBytes > c.maxBytes {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.evictLocked(oldest.Value.(*webFetchCacheEntry))
	}
}

// evictLocked 删除一个条目(调用方持锁)。
func (c *webFetchCache) evictLocked(entry *webFetchCacheEntry) {
	if entry == nil {
		return
	}
	if _, ok := c.items[entry.key]; !ok {
		return
	}
	delete(c.items, entry.key)
	c.order.Remove(entry.elem)
	c.totalBytes -= entry.size
	if c.totalBytes < 0 {
		c.totalBytes = 0
	}
}

// normalizeWebFetchCacheKey 把 URL 规范化为缓存键。
//   - 去 fragment(#后)
//   - 默认端口(http:80/https:443)去除
//   - query 按键字典序排序,使命不同写法但等价的 URL 命中同一项
//   - scheme/host 小写
//
// 解析失败时回退到 trim 后的原始串(仍可缓存,只是命中面变窄)。
func normalizeWebFetchCacheKey(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return trimmed
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	if parsed.Host != "" {
		host := strings.ToLower(parsed.Hostname())
		port := parsed.Port()
		hasDefaultPort := (parsed.Scheme == "http" && port == "80") ||
			(parsed.Scheme == "https" && port == "443")
		if port == "" || hasDefaultPort {
			parsed.Host = host
		} else {
			parsed.Host = host + ":" + port
		}
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	// query 排序:重写 RawQuery 为排序后的形式。
	query := parsed.Query()
	if len(query) > 0 {
		parsed.RawQuery = query.Encode() // url.Values.Encode 已按键排序
	}
	return parsed.String()
}
