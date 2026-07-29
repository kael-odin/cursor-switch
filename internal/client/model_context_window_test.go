package client

import "testing"

// TestXiaomiMiMoContextWindow 锁定小米 MiMo 上下文窗口 = 1M。
//
// 背景（2026-07-29 修）：此前内置表把 mimo-v2.5-pro 等写成 128_000，而小米 /v1/models
// 不返回 context window、models.dev 未收录小米 → 错误值无法被在线回退纠正 → Cursor UI
// 显示 "25K/128K" 的分母错误（用户记得是 1M）。实测直连 api.xiaomimimo.com 问模型自报
// v2.5 系列均为 1,000,000，此回归测试防以后又被改回错误值。
//
// 实测命令记录：
//	curl -s -X POST https://api.xiaomimimo.com/v1/chat/completions \
//	  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
//	  --data-binary '{"model":"mimo-v2.5-pro","messages":[{"role":"user","content":"What is your maximum context window in tokens? Just the number."}],"max_tokens":300,"reasoning_effort":"low"}'
//	→ content: "1,000,000"（v2.5 / v2.5-pro / v2.5-pro-ultraspeed 均一致）
//	v2-pro / v2-flash 已 "Unsupported model" 下线，保留 1M 与 v2.5 系列同口径。
func TestXiaomiMiMoContextWindow(t *testing.T) {
	want := int64(1_000_000)
	cases := []string{
		"mimo-v2.5-pro",
		"mimo-v2.5-pro-ultraspeed",
		"mimo-v2.5",
		"mimo-v2-pro",
		"mimo-v2-flash",
	}
	for _, m := range cases {
		got := lookupContextWindowTokens(m)
		if got != want {
			t.Errorf("lookupContextWindowTokens(%q) = %d, want %d (MiMo 1M 上下文，勿回退 128K 错误值)", m, got, want)
		}
	}
}

// TestXiaomiMiMoContextWindowCandidateMatch 验证带后缀/版本变体经候选匹配仍命中 1M，
// 锁住"候选剥离不会把 mimo 漏掉"的契约。
func TestXiaomiMiMoContextWindowCandidateMatch(t *testing.T) {
	cases := []string{
		"mimo-v2.5-pro",
		"mimo-v2.5-pro-ultraspeed",
		"Xiaomi/mimo-v2.5-pro", // 带命名空间前缀应剥离
	}
	want := int64(1_000_000)
	for _, m := range cases {
		got := lookupContextWindowTokens(m)
		if got != want {
			t.Errorf("lookupContextWindowTokens(%q) = %d, want %d (候选匹配应命中 MiMo 1M)", m, got, want)
		}
	}
}

// TestContextWindowNeverReturnsBogus128KForMiMo 是防回归硬断言：MiMo 系列绝不能返回
// 128_000——这是当初的 bug 值，若有人改表加回必须被此测试拦下。
func TestContextWindowNeverReturnsBogus128KForMiMo(t *testing.T) {
	for _, m := range []string{"mimo-v2.5-pro", "mimo-v2.5", "mimo-v2.5-pro-ultraspeed"} {
		got := lookupContextWindowTokens(m)
		if got == 128_000 {
			t.Errorf("lookupContextWindowTokens(%q) = 128_000，这是已确认的错误值（实际 1M），不得回退", m)
		}
	}
}
