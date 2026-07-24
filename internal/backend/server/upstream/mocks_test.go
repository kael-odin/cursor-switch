package upstream

import (
	"encoding/json"
	"testing"
)

func TestBuildBootstrapStatsigConfigJSONDisablesAlwaysLocalDecompositionGate(t *testing.T) {
	payload, err := buildBootstrapStatsigConfigJSON(12345, "test-auth-id")
	if err != nil {
		t.Fatalf("build bootstrap statsig config: %v", err)
	}

	var decoded statsigBootstrapTemplate
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode bootstrap statsig config: %v", err)
	}

	gate, ok := decoded.FeatureGates[bootstrapStatsigDecomposeAlwaysLocalExtHostGate]
	if !ok {
		t.Fatalf("missing feature gate %q", bootstrapStatsigDecomposeAlwaysLocalExtHostGate)
	}
	if value, _ := gate["value"].(bool); value {
		t.Fatalf("expected %q to be disabled", bootstrapStatsigDecomposeAlwaysLocalExtHostGate)
	}
	if ruleID, _ := gate["rule_id"].(string); ruleID != "local_disabled" {
		t.Fatalf("unexpected rule_id: %q", ruleID)
	}
}

func TestBuildBootstrapStatsigConfigJSONEnablesAgentWorkerExtensionGate(t *testing.T) {
	payload, err := buildBootstrapStatsigConfigJSON(12345, "test-auth-id")
	if err != nil {
		t.Fatalf("build bootstrap statsig config: %v", err)
	}

	var decoded statsigBootstrapTemplate
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode bootstrap statsig config: %v", err)
	}

	gate, ok := decoded.FeatureGates[bootstrapStatsigCursorAgentWorkerExtension]
	if !ok {
		t.Fatalf("missing feature gate %q", bootstrapStatsigCursorAgentWorkerExtension)
	}
	if value, _ := gate["value"].(bool); !value {
		t.Fatalf("expected %q to be enabled so the agent window marketplace/customize panel can render", bootstrapStatsigCursorAgentWorkerExtension)
	}
	if ruleID, _ := gate["rule_id"].(string); ruleID != "local_enabled" {
		t.Fatalf("unexpected rule_id: %q", ruleID)
	}
}

// 说明：TestBuildDashboardGetEffectiveUserPluginsPayloadReturnsLocalMarketplace 已移除。
// 它测的是 byok 伪造的本地 marketplace mock；该 mock 已删除，marketplace 现由真实 Cursor 账号透传。
