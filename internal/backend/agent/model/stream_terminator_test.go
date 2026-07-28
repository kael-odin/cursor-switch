package modeladapter

import (
	"errors"
	"strings"
	"testing"
)

// TestStreamTerminatorMissingErrorFormat 校验 F-20 错误形态以 "provider stream truncated: " 开头，
// 且 provider 名嵌入错误信息——这是 isRetryableChannelError / isStreamTerminatorMissingError 判定的契约。
func TestStreamTerminatorMissingErrorFormat(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "custom-provider"} {
		err := streamTerminatorMissingError(provider)
		if err == nil {
			t.Fatalf("provider %s: expected non-nil error", provider)
		}
		text := err.Error()
		if !strings.HasPrefix(text, "provider stream truncated: ") {
			t.Fatalf("provider %s: error %q missing required prefix", provider, text)
		}
		if !strings.Contains(text, provider) {
			t.Fatalf("provider %s: error %q does not mention provider", provider, text)
		}
	}
}

// TestIsStreamTerminatorMissingError 校验 isStreamTerminatorMissingError 只识别 F-20 截断错误，
// 普通错误（含 status=NNN、idle timeout、字符串拼接外层包装）不被误判。
//
// 注意：判定走 err.Error() 字符串前缀匹配（契约一致于 isRetryableChannelError 对 status= 的判定），
// 因此纯字符串拼接的外层包装不会被识别——这是有意的：adapter 内部只在流终止时直接返回本错误，
// 不会经过额外包装层，前缀匹配足以覆盖实际调用路径。
func TestIsStreamTerminatorMissingError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"truncated openai", streamTerminatorMissingError("openai"), true},
		{"truncated anthropic", streamTerminatorMissingError("anthropic"), true},
		{"plain error", errors.New("some other failure"), false},
		{"http status error", errors.New("request failed: status=500 body=oops"), false},
		{"idle timeout error", errors.New("provider stream idle timeout after 30s"), false},
		{"string-concat wrapped (not recognized)", errors.New("outer: " + streamTerminatorMissingError("openai").Error()), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isStreamTerminatorMissingError(c.err)
			if got != c.want {
				t.Fatalf("isStreamTerminatorMissingError(%v)=%v want %v", c.err, got, c.want)
			}
		})
	}
}

// TestIsRetryableChannelErrorAcceptsTruncation 校验 F-20 截断错误被 isRetryableChannelError 视为可重试，
// 这样 Router failover 能在零事件/提前 EOF 的"空成功"上换候选重试。
func TestIsRetryableChannelErrorAcceptsTruncation(t *testing.T) {
	err := streamTerminatorMissingError("openai")
	if !isRetryableChannelError(err) {
		t.Fatalf("isRetryableChannelError should treat stream truncation as retryable; err=%v", err)
	}
	// 4xx 不可重试，作对照
	if isRetryableChannelError(errors.New("request failed: status=400 body=bad request")) {
		t.Fatalf("status=400 should not be retryable")
	}
}
