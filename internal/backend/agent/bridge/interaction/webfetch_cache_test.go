package interaction

import (
	"strings"
	"testing"
	"time"
)

// TestWebFetchCacheHitAndMiss 验证 put 后 get 命中,未 put 的 key 未命中。
func TestWebFetchCacheHitAndMiss(t *testing.T) {
	c := newWebFetchCache(1024, time.Minute)
	c.put("k1", "payload-1")
	if got, ok := c.get("k1"); !ok || got != "payload-1" {
		t.Fatalf("expected hit payload-1, got %q ok=%v", got, ok)
	}
	if _, ok := c.get("k2"); ok {
		t.Fatal("k2 should miss")
	}
}

// TestWebFetchCacheTTLExpiry 验证过期条目视为未命中并淘汰。
func TestWebFetchCacheTTLExpiry(t *testing.T) {
	c := newWebFetchCache(1024, 10*time.Millisecond)
	c.put("k", "v")
	time.Sleep(30 * time.Millisecond)
	if _, ok := c.get("k"); ok {
		t.Fatal("expired entry should miss")
	}
}

// TestWebFetchCacheLRUEviction 验证超容量时按 LRU 淘汰最久未访问项。
func TestWebFetchCacheLRUEviction(t *testing.T) {
	// 每条目 3 字节,容量 6 字节 → 最多 2 条共存,第 3 条触发淘汰 LRU。
	c := newWebFetchCache(6, time.Minute)
	c.put("a", "aaa")
	c.put("b", "bbb")
	// 访问 a,使 b 成为 LRU。
	if _, ok := c.get("a"); !ok {
		t.Fatal("a should hit before eviction pressure")
	}
	c.put("c", "ccc")
	// b 是 LRU,应被淘汰;a 仍在。
	if _, ok := c.get("b"); ok {
		t.Fatal("b should have been evicted as LRU")
	}
	if _, ok := c.get("a"); !ok {
		t.Fatal("a (MRU) should survive")
	}
	if _, ok := c.get("c"); !ok {
		t.Fatal("c should survive")
	}
}

// TestWebFetchCacheEmptyNotCached 验证空 payload 不缓存。
func TestWebFetchCacheEmptyNotCached(t *testing.T) {
	c := newWebFetchCache(1024, time.Minute)
	c.put("k", "")
	c.put("k2", "   ")
	if _, ok := c.get("k"); ok {
		t.Fatal("empty payload should not be cached")
	}
	if _, ok := c.get("k2"); ok {
		t.Fatal("whitespace-only payload should not be cached")
	}
}

// TestWebFetchCacheKeyNormalization 验证不同写法的等价 URL 命中同一缓存项。
func TestWebFetchCacheKeyNormalization(t *testing.T) {
	// 同一资源的多种写法,规范化后应得到同一 key。
	canonical := normalizeWebFetchCacheKey("https://Example.com:443/path?b=2&a=1#section")
	equivalent := []string{
		"https://example.com/path?a=1&b=2",            // 默认端口去除 + query 排序 + 大小写
		"https://example.com:443/path?a=1&b=2",        // 默认端口显式写出
		"https://example.com/path?b=2&a=1",            // query 乱序
		"https://example.com/path?a=1&b=2#fragment",   // fragment 应被去除
	}
	for i, alt := range equivalent {
		got := normalizeWebFetchCacheKey(alt)
		if got != canonical {
			t.Fatalf("equivalent[%d] %q -> %q, want canonical %q", i, alt, got, canonical)
		}
	}
}

// TestWebFetchCacheOversizedEntrySkipped 验证单条目超过总容量时不缓存(不独占)。
func TestWebFetchCacheOversizedEntrySkipped(t *testing.T) {
	c := newWebFetchCache(8, time.Minute)
	big := strings.Repeat("x", 100)
	c.put("big", big)
	if _, ok := c.get("big"); ok {
		t.Fatal("oversized entry should not be cached")
	}
}
