package modeladapter

import (
	"fmt"
)

// F-21：Provider 流资源预算。
//
// 各 adapter 的 scanner 已有单事件字节上限（OpenAI 64MB / Anthropic 1MB），但缺总量预算——
// 恶意/异常 provider 可用海量小事件或长期慢发持续消耗内存与连接。streamBudget 在扫描循环中
// 累计总原始字节数与事件数，超限即返回可重试错误（与 F-20 截断错误同属 isRetryableChannelError
// 可重试集合），让 Router B2 failover 换候选重试。
//
// 选值保守：单 provider turn 正常远低于这些阈值，仅拦恶意/失控场景。
const (
	defaultProviderStreamMaxBytes  int64 = 256 * 1024 * 1024 // 256 MiB 原始响应总量
	defaultProviderStreamMaxEvents int64 = 200_000            // 事件数上限
)

// streamBudget 跟踪 provider 流的总量预算。零值可用，默认阈值生效。
type streamBudget struct {
	maxBytes  int64
	maxEvents int64
	bytes     int64
	events    int64
	exceeded  string // 非空时记录触发项，便于错误信息
}

func newStreamBudget() *streamBudget {
	return &streamBudget{
		maxBytes:  defaultProviderStreamMaxBytes,
		maxEvents: defaultProviderStreamMaxEvents,
	}
}

// addBytes 累加原始字节数，超 maxBytes 返回可重试的预算超限错误。
func (b *streamBudget) addBytes(n int64) error {
	if b == nil || n <= 0 {
		return nil
	}
	b.bytes += n
	if b.maxBytes > 0 && b.bytes > b.maxBytes {
		b.exceeded = "bytes"
		return providerStreamBudgetExceededError("bytes", b.bytes, b.maxBytes)
	}
	return nil
}

// addEvent 累加事件数，超 maxEvents 返回可重试的预算超限错误。
func (b *streamBudget) addEvent() error {
	if b == nil {
		return nil
	}
	b.events++
	if b.maxEvents > 0 && b.events > b.maxEvents {
		b.exceeded = "events"
		return providerStreamBudgetExceededError("events", b.events, b.maxEvents)
	}
	return nil
}

// providerStreamBudgetExceededError 返回预算超限错误。错误形态以 "provider stream budget exceeded: "
// 开头，被 isRetryableChannelError 识别为可重试（换 provider 可能不超限）。
func providerStreamBudgetExceededError(dimension string, seen int64, limit int64) error {
	return fmt.Errorf("provider stream budget exceeded: %s %d > %d", dimension, seen, limit)
}

func isProviderStreamBudgetExceededError(err error) bool {
	if err == nil {
		return false
	}
	return hasErrorPrefix(err, "provider stream budget exceeded: ")
}

func hasErrorPrefix(err error, prefix string) bool {
	if err == nil {
		return false
	}
	return len(err.Error()) >= len(prefix) && err.Error()[:len(prefix)] == prefix
}
