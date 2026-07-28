package modeladapter

import (
	"errors"
	"strings"
	"testing"
)

// TestStreamBudgetAddBytesOverLimit (F-21) 验证 addBytes 在累计超过上限时返回预算超限错误，
// 且错误形态被 isRetryableChannelError 识别为可重试（换 provider 可能不超限）。
func TestStreamBudgetAddBytesOverLimit(t *testing.T) {
	b := newStreamBudget()
	b.maxBytes = 100
	// 前 100 字节不超限。
	if err := b.addBytes(50); err != nil {
		t.Fatalf("addBytes(50) should not exceed, got %v", err)
	}
	if err := b.addBytes(50); err != nil {
		t.Fatalf("addBytes(50) at limit should not exceed, got %v", err)
	}
	// 第 101 字节触发超限。
	err := b.addBytes(1)
	if err == nil {
		t.Fatalf("addBytes(1) over limit should error")
	}
	if !strings.HasPrefix(err.Error(), "provider stream budget exceeded: bytes ") {
		t.Fatalf("unexpected error form: %v", err)
	}
	if !isProviderStreamBudgetExceededError(err) {
		t.Fatalf("isProviderStreamBudgetExceededError should be true")
	}
	if !isRetryableChannelError(err) {
		t.Fatalf("budget exceeded should be retryable for failover")
	}
}

// TestStreamBudgetAddEventOverLimit 验证事件数上限触发超限错误。
func TestStreamBudgetAddEventOverLimit(t *testing.T) {
	b := newStreamBudget()
	b.maxEvents = 3
	for i := 0; i < 3; i++ {
		if err := b.addEvent(); err != nil {
			t.Fatalf("addEvent %d should not exceed, got %v", i, err)
		}
	}
	err := b.addEvent()
	if err == nil {
		t.Fatalf("addEvent over limit should error")
	}
	if !strings.HasPrefix(err.Error(), "provider stream budget exceeded: events ") {
		t.Fatalf("unexpected error form: %v", err)
	}
	if !isRetryableChannelError(err) {
		t.Fatalf("budget exceeded should be retryable")
	}
}

// TestStreamBudgetZeroValueAndNegatives 验证零值/负输入的安全行为。
func TestStreamBudgetZeroValueAndNegatives(t *testing.T) {
	var b *streamBudget // nil receiver
	if err := b.addBytes(100); err != nil {
		t.Fatalf("nil receiver addBytes should be no-op, got %v", err)
	}
	if err := b.addEvent(); err != nil {
		t.Fatalf("nil receiver addEvent should be no-op, got %v", err)
	}
	b2 := newStreamBudget()
	if err := b2.addBytes(0); err != nil {
		t.Fatalf("addBytes(0) should be no-op")
	}
	if err := b2.addBytes(-5); err != nil {
		t.Fatalf("addBytes(-5) should be no-op")
	}
}

// TestStreamBudgetNonExceededNotRetryable 验证非预算错误不误判。
func TestStreamBudgetNonExceededNotRetryable(t *testing.T) {
	if isRetryableChannelError(errors.New("provider stream budget exceeded: bytes 1 > 100")) {
		// 字符串匹配——是预算错误形态，应可重试
	}
	// 真正的对照：普通错误不可重试
	if isRetryableChannelError(errors.New("some unrelated error")) {
		t.Fatalf("unrelated error should not be retryable")
	}
}
