package modeladapter

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// blockedCustomHeaders 列出用户自定义请求头不得覆盖的敏感头。
// 这些头由适配器按 provider 协议设置真实凭据，允许用户覆盖会把鉴权重定向到攻击者端点。
var blockedCustomHeaders = map[string]struct{}{
	"authorization": {},
	"x-api-key":     {},
	"host":          {},
	"cookie":        {},
}

// blockedExtraParamKeys 列出用户 extra params 不得覆盖的协议/身份/路由字段（F-19）。
// 这些字段由适配器按 provider 协议和路由决策设置，允许用户覆盖会破坏流控、
// 绕过模型路由记账、或（与 F-20 组合）伪造空流成功。与 blockedCustomHeaders 同手法。
var blockedExtraParamKeys = map[string]struct{}{
	"stream":       {}, // 流开关；改 false 与 F-20 组合可伪造空流成功
	"model":        {}, // 绕过路由/记账
	"messages":     {}, // Chat Completions 对话主体
	"input":        {}, // Responses API 对话主体
	"tools":        {}, // 工具定义
	"tool_choice":  {}, // 强制工具调用
	"system":       {}, // Anthropic 系统提示
	"instructions": {}, // Responses API 系统提示
	"metadata":     {}, // 追踪元数据（user_id 等）
}

func cloneRequestBodyOverride(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var cloned map[string]any
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return nil
	}
	return cloned
}

func requestBodyToMap(input any) (map[string]any, error) {
	if body, ok := input.(map[string]any); ok {
		return body, nil
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, nil
}

func ApplyOpenAIExtraParams(body map[string]any, enabled bool, paramsJSON string) error {
	return applyExtraParams(body, enabled, paramsJSON, "openai extra params json")
}

func ApplyAnthropicExtraParams(body map[string]any, enabled bool, paramsJSON string) error {
	return applyExtraParams(body, enabled, paramsJSON, "anthropic extra params json")
}

func applyExtraParams(body map[string]any, enabled bool, paramsJSON string, label string) error {
	if !enabled {
		return nil
	}
	if body == nil {
		return fmt.Errorf("%s target body is nil", label)
	}
	extraParams, err := parseJSONMap(paramsJSON, label)
	if err != nil {
		return err
	}
	for key, value := range extraParams {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		// F-19：禁止 extra params 覆盖协议/身份/路由关键字段（与 blockedCustomHeaders 同手法）。
		if _, blocked := blockedExtraParamKeys[strings.ToLower(name)]; blocked {
			log.Printf("modeladapter: extra param %q blocked (would override protocol/routing field)", name)
			continue
		}
		body[name] = value
	}
	return nil
}

func ApplyCustomHeaders(httpReq *http.Request, enabled bool, headersJSON string) error {
	if !enabled {
		return nil
	}
	if httpReq == nil {
		return fmt.Errorf("custom headers target request is nil")
	}
	headers, err := parseStringJSONMap(headersJSON, "custom headers json")
	if err != nil {
		return err
	}
	for key, value := range headers {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		if _, blocked := blockedCustomHeaders[strings.ToLower(name)]; blocked {
			log.Printf("modeladapter: custom header %q blocked (would override auth/host)", name)
			continue
		}
		httpReq.Header.Set(name, value)
	}
	return nil
}

func parseJSONMap(value string, label string) (map[string]any, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil, fmt.Errorf("%s is empty", label)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf("%s must be an object: %w", label, err)
	}
	if parsed == nil {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	return parsed, nil
}

func parseStringJSONMap(value string, label string) (map[string]string, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil, fmt.Errorf("%s is empty", label)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf("%s must be an object with string values: %w", label, err)
	}
	if parsed == nil {
		return nil, fmt.Errorf("%s must be an object", label)
	}
	return parsed, nil
}
