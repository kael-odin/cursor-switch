package modeladapter

import "testing"

// TestNormalizeRuntimeThinkingEffort 锁定「思考强度」归一化表（审计 L2）。
// 此前 service.go 与 router.go 各存一份完全相同的实现，现统一到 model 包，
// forwarder 经 modeladapter 别名调用同一份。本表锁定输入→输出契约，
// 防止后续误改归一化分支（尤其 disabled/off/0 → disabled、very_high 系 → xhigh、maximum → max）。
func TestNormalizeRuntimeThinkingEffort(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		// 已是合法值：原样小写返回（含大小写/空白容差）
		{"disabled lowercase", "disabled", "disabled"},
		{"disabled upper", "DISABLED", "disabled"},
		{"disabled spaces", "  disabled  ", "disabled"},
		{"low", "low", "low"},
		{"medium", "medium", "medium"},
		{"high", "high", "high"},
		{"xhigh", "xhigh", "xhigh"},
		{"max", "max", "max"},

		// 关闭语义族 → disabled
		{"disable", "disable", "disabled"},
		{"off", "off", "disabled"},
		{"none", "none", "disabled"},
		{"false", "false", "disabled"},
		{"no", "no", "disabled"},
		{"zero", "0", "disabled"},

		// very-high 族 → xhigh
		{"very_high underscore", "very_high", "xhigh"},
		{"very-high dash", "very-high", "xhigh"},
		{"veryhigh", "veryhigh", "xhigh"},
		{"x-high", "x-high", "xhigh"},
		{"extra_high", "extra_high", "xhigh"},
		{"extra-high", "extra-high", "xhigh"},
		{"extrahigh", "extrahigh", "xhigh"},

		// maximum → max
		{"maximum", "maximum", "max"},

		// 无法识别 → ""（调用方按"未指定"处理）
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"unknown word", "turbo", ""},
		{"bogus number", "3", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 导出函数与包内别名都走同一实现，两份都验一遍。
			if got := NormalizeRuntimeThinkingEffort(tt.raw); got != tt.want {
				t.Errorf("NormalizeRuntimeThinkingEffort(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			if got := normalizeRuntimeThinkingEffort(tt.raw); got != tt.want {
				t.Errorf("normalizeRuntimeThinkingEffort(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
