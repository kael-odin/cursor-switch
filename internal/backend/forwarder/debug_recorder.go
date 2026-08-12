package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	"cursor/internal/securefile"
)

// maxDebugLogFileBytes 是单类 debug jsonl 的大小上限，达到后轮转（保留 .1/.2/.3 三份备份）。
// 配合 P0-3 的字段脱敏，避免长对话下明文日志无限增长占满磁盘。
const maxDebugLogFileBytes = 10 * 1024 * 1024

// debugLogFileCap 是轮转实际生效上限，默认取常量；测试可覆盖以小阈值验证轮转逻辑。
var debugLogFileCap = int(maxDebugLogFileBytes)

// maxDebugBackupCount 是轮转保留的备份份数。
const maxDebugBackupCount = 3

// maxDebugDataHexLength 是单条 bidi 原始请求体 hex 的上限；超限截断，防止单行超长绕过轮转预算。
const maxDebugDataHexLength = 64 * 1024

// sensitiveDebugFields 是需要脱敏的字段名（小写精确匹配，递归应用于所有 debug 事件）。
// P0-3：provider 请求/响应 payload 里的 Authorization、api key、token、password、secret 等
// 一旦落盘即明文泄露——即使文件权限 0600，配合网盘同步/日志收集仍会外泄。
var sensitiveDebugFields = map[string]struct{}{
	"authorization":       {},
	"auth":                {},
	"api_key":             {},
	"apikey":              {},
	"api-key":             {},
	"x-api-key":           {},
	"x_api_key":           {},
	"x-auth-token":        {},
	"x_auth_token":        {},
	"access_key":          {},
	"access_key_id":       {},
	"secret":              {},
	"secret_key":          {},
	"client_secret":       {},
	"client-secret":       {},
	"app_secret":          {},
	"private_key":         {},
	"password":            {},
	"passwd":              {},
	"pwd":                 {},
	"token":               {},
	"access_token":        {},
	"refresh_token":       {},
	"id_token":            {},
	"session_token":       {},
	"auth_token":          {},
	"bearer":              {},
	"cookie":              {},
	"set-cookie":          {},
	"set_cookie":          {},
	"credentials":         {},
	"proxy-authorization": {},
}

type debugLogConfig interface {
	IsObservabilityLogEnabled(context.Context) bool
}

type debugRecorder struct {
	historyRoot string
	broker      *StreamBroker
	config      debugLogConfig
	mu          sync.Mutex
}

// newDebugRecorder 创建 debug 记录器。
//
// 注意：historyRoot 不应放在网盘/云同步目录——debug 日志是诊断用途，虽经脱敏且权限 0600，
// 同步到云端仍会扩大暴露面（P0-3 文档化要求）。
func newDebugRecorder(historyRoot string, broker *StreamBroker, config debugLogConfig) *debugRecorder {
	return &debugRecorder{
		historyRoot: strings.TrimSpace(historyRoot),
		broker:      broker,
		config:      config,
	}
}

func (recorder *debugRecorder) enabled(ctx context.Context) bool {
	if recorder == nil || recorder.config == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return recorder.config.IsObservabilityLogEnabled(ctx)
}

func (recorder *debugRecorder) LogBidiRaw(ctx context.Context, requestID string, conversationID string, appendSeqno int64, dataHex string, status string, extra map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("bidi_raw", requestID, conversationID)
	event["direction"] = "client_to_backend"
	event["procedure"] = "/aiserver.v1.BidiService/BidiAppend"
	event["append_seqno"] = appendSeqno
	event["status"] = strings.TrimSpace(status)
	event["data_hex"] = truncateDebugDataHex(dataHex)
	event["data_len"] = len(dataHex)
	event["data_truncated"] = len(dataHex) > maxDebugDataHexLength
	for key, value := range extra {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, conversationID, "bidi.raw.jsonl", event)
}

func (recorder *debugRecorder) LogBidiDecoded(ctx context.Context, requestID string, conversationID string, appendSeqno int64, clientKind string, message *agentv1.AgentClientMessage, intent InboundIntent, extra map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("bidi_decoded", requestID, conversationID)
	event["schema_version"] = 2
	event["append_seqno"] = appendSeqno
	event["client_kind"] = strings.TrimSpace(clientKind)
	event["message_case"] = agentClientMessageCase(message)
	event["message"] = protoJSONDebugPayload(message)
	event["intent"] = inboundIntentDebugPayload(intent)
	if requestedModel := requestedModelDebugPayload(message); requestedModel != nil {
		event["requested_model"] = requestedModel
	}
	if actionCase := conversationActionCase(message); actionCase != "" {
		event["conversation_action"] = actionCase
	}
	for key, value := range extra {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, firstNonEmpty(conversationID, intent.ConversationID), "bidi.decoded.jsonl", event)
}

func (recorder *debugRecorder) LogRuntime(ctx context.Context, requestID string, conversationID string, eventName string, fields map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("runtime", requestID, conversationID)
	event["event"] = strings.TrimSpace(eventName)
	for key, value := range fields {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, conversationID, "runtime.jsonl", event)
}

func (recorder *debugRecorder) LogRunSSE(ctx context.Context, requestID string, conversationID string, eventName string, fields map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("runsse", requestID, conversationID)
	event["event"] = strings.TrimSpace(eventName)
	for key, value := range fields {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, conversationID, "runsse.jsonl", event)
}

func (recorder *debugRecorder) LogProvider(ctx context.Context, requestID string, conversationID string, eventName string, fields map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("provider", requestID, conversationID)
	event["event"] = strings.TrimSpace(eventName)
	for key, value := range fields {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, conversationID, "provider.jsonl", event)
}

func (recorder *debugRecorder) LogProviderArtifact(ctx context.Context, requestID string, conversationID string, modelCallID string, eventName string, payload map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	recorder.LogProvider(ctx, requestID, conversationID, eventName, map[string]any{
		"model_call_id": strings.TrimSpace(modelCallID),
		"payload":       payload,
	})
}

func (recorder *debugRecorder) baseEvent(layer string, requestID string, conversationID string) map[string]any {
	resolvedConversationID := firstNonEmpty(strings.TrimSpace(conversationID), recorder.conversationIDForRequest(requestID))
	return map[string]any{
		"schema_version":  1,
		"at":              time.Now().UTC().Format(time.RFC3339Nano),
		"layer":           strings.TrimSpace(layer),
		"request_id":      strings.TrimSpace(requestID),
		"conversation_id": resolvedConversationID,
	}
}

func (recorder *debugRecorder) appendJSONL(ctx context.Context, requestID string, conversationID string, filename string, event map[string]any) {
	if !recorder.enabled(ctx) || len(event) == 0 {
		return
	}
	dir := recorder.debugDir(requestID, conversationID)
	if strings.TrimSpace(dir) == "" {
		return
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	// P0-3：落盘前做整树脱敏——先反序列化成通用 JSON 形状（map[string]any/[]any/string），
	// 再递归把敏感字段（authorization/api_key/token/password/secret 等）替换为 ***。
	// 覆盖 data_hex 之外的 message/payload/body 所有嵌套层级，含第三方 provider 的认证头。
	var generic any
	if err := json.Unmarshal(payload, &generic); err == nil {
		if sanitized, err := json.Marshal(sanitizeDebugEvent(generic)); err == nil {
			payload = sanitized
		}
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	// F-18：debug JSONL 含完整请求体（消息/上下文/工具参数），目录 0700、文件 0600。
	if err := securefile.MkdirAll(dir); err != nil {
		return
	}
	logPath := filepath.Join(dir, filename)
	// P0-6：单类 jsonl 超过上限时轮转（.1/.2/.3，保留 3 份），防止长对话下无限增长占满磁盘。
	if info, statErr := os.Stat(logPath); statErr == nil && info.Size() >= int64(debugLogFileCap) {
		recorder.rotateLog(logPath)
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, securefile.FileMode)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(payload, '\n'))
	_ = securefile.EnsureMode(logPath, securefile.FileMode)
}

// rotateLog 把 logPath 轮转为 logPath.1，并顺延旧备份 .1→.2、.2→.3，删除 .3。
// 在 recorder.mu 持锁下调用；轮转失败只丢弃当前备份，不阻塞写入。
func (recorder *debugRecorder) rotateLog(logPath string) {
	_ = os.Remove(logPath + fmt.Sprintf(".%d", maxDebugBackupCount))
	for i := maxDebugBackupCount - 1; i >= 1; i-- {
		older := fmt.Sprintf("%s.%d", logPath, i)
		newer := fmt.Sprintf("%s.%d", logPath, i+1)
		if _, err := os.Stat(older); err == nil {
			_ = os.Rename(older, newer)
		}
	}
	_ = os.Rename(logPath, logPath+".1")
}

// truncateDebugDataHex 截断超长 bidi 原始请求体 hex，防止单行超长。
func truncateDebugDataHex(dataHex string) string {
	if len(dataHex) <= maxDebugDataHexLength {
		return dataHex
	}
	return dataHex[:maxDebugDataHexLength]
}

// sanitizeDebugEvent 递归脱敏 debug 事件中的敏感字段与敏感字符串值。
func sanitizeDebugEvent(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if _, sensitive := sensitiveDebugFields[strings.ToLower(strings.TrimSpace(key))]; sensitive {
				out[key] = "***"
				continue
			}
			out[key] = sanitizeDebugEvent(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = sanitizeDebugEvent(item)
		}
		return out
	case string:
		return redactSensitiveStringValue(typed)
	default:
		return value
	}
}

// redactSensitiveStringValue 识别字符串值形态的凭证（Bearer 头）并脱敏。
func redactSensitiveStringValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "Bearer ") && len(trimmed) > len("Bearer ") {
		return "***"
	}
	return value
}

func (recorder *debugRecorder) debugDir(requestID string, conversationID string) string {
	if recorder == nil || strings.TrimSpace(recorder.historyRoot) == "" {
		return ""
	}
	conversationID = firstNonEmpty(strings.TrimSpace(conversationID), recorder.conversationIDForRequest(requestID))
	if conversationID != "" && conversationID != "unknown" {
		return filepath.Join(recorder.historyRoot, sanitizeArtifactName(conversationID), "debug")
	}
	requestID = firstNonEmpty(strings.TrimSpace(requestID), "unknown")
	return filepath.Join(recorder.historyRoot, "_debug", "orphan", sanitizeArtifactName(requestID))
}

func (recorder *debugRecorder) conversationIDForRequest(requestID string) string {
	if recorder == nil || recorder.broker == nil {
		return ""
	}
	stream, ok := recorder.broker.Get(requestID)
	if !ok || stream == nil {
		return ""
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return strings.TrimSpace(stream.ConversationID)
}

func agentClientMessageCase(message *agentv1.AgentClientMessage) string {
	if message == nil {
		return ""
	}
	switch message.GetMessage().(type) {
	case *agentv1.AgentClientMessage_RunRequest:
		return "run_request"
	case *agentv1.AgentClientMessage_PrewarmRequest:
		return "prewarm_request"
	case *agentv1.AgentClientMessage_ConversationAction:
		return "conversation_action"
	case *agentv1.AgentClientMessage_ExecClientMessage:
		return "exec_client_message"
	case *agentv1.AgentClientMessage_ExecClientControlMessage:
		return "exec_client_control_message"
	case *agentv1.AgentClientMessage_InteractionResponse:
		return "interaction_response"
	case *agentv1.AgentClientMessage_ClientHeartbeat:
		return "client_heartbeat"
	case *agentv1.AgentClientMessage_KvClientMessage:
		return "kv_client_message"
	default:
		return fmt.Sprintf("%T", message.GetMessage())
	}
}

func agentServerMessageCase(message *agentv1.AgentServerMessage) string {
	if message == nil {
		return ""
	}
	switch message.GetMessage().(type) {
	case *agentv1.AgentServerMessage_InteractionUpdate:
		return "interaction_update"
	case *agentv1.AgentServerMessage_ExecServerMessage:
		return "exec_server_message"
	case *agentv1.AgentServerMessage_ExecServerControlMessage:
		return "exec_server_control_message"
	case *agentv1.AgentServerMessage_ConversationCheckpointUpdate:
		return "conversation_checkpoint_update"
	case *agentv1.AgentServerMessage_KvServerMessage:
		return "kv_server_message"
	case *agentv1.AgentServerMessage_InteractionQuery:
		return "interaction_query"
	default:
		return fmt.Sprintf("%T", message.GetMessage())
	}
}

func conversationActionCase(message *agentv1.AgentClientMessage) string {
	if message == nil {
		return ""
	}
	action := message.GetConversationAction()
	if action == nil && message.GetRunRequest() != nil {
		action = message.GetRunRequest().GetAction()
	}
	if action == nil {
		return ""
	}
	return conversationActionKind(action)
}

func requestedModelDebugPayload(message *agentv1.AgentClientMessage) map[string]any {
	if message == nil {
		return nil
	}
	if runRequest := message.GetRunRequest(); runRequest != nil {
		return requestedModelPayload(runRequest.GetRequestedModel())
	}
	if prewarm := message.GetPrewarmRequest(); prewarm != nil {
		return requestedModelPayload(prewarm.GetRequestedModel())
	}
	return nil
}

func requestedModelPayload(model *agentv1.RequestedModel) map[string]any {
	if model == nil {
		return nil
	}
	parameters := make([]map[string]string, 0, len(model.GetParameters()))
	for _, parameter := range model.GetParameters() {
		if parameter == nil {
			continue
		}
		parameters = append(parameters, map[string]string{
			"id":    parameter.GetId(),
			"value": parameter.GetValue(),
		})
	}
	return map[string]any{
		"model_id":                         strings.TrimSpace(model.GetModelId()),
		"max_mode":                         model.GetMaxMode(),
		"built_in_model":                   model.GetBuiltInModel(),
		"is_variant_string_representation": model.GetIsVariantStringRepresentation(),
		"parameters":                       parameters,
	}
}

func protoJSONDebugPayload(message proto.Message) any {
	if message == nil {
		return nil
	}
	payload, err := protojson.MarshalOptions{
		UseProtoNames:   true,
		EmitUnpopulated: false,
	}.Marshal(message)
	if err != nil {
		return map[string]any{"marshal_error": err.Error()}
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return string(payload)
	}
	return decoded
}

func inboundIntentDebugPayload(intent InboundIntent) map[string]any {
	payload := map[string]any{
		"kind":               strings.TrimSpace(intent.Kind),
		"request_id":         strings.TrimSpace(intent.RequestID),
		"conversation_id":    strings.TrimSpace(intent.ConversationID),
		"model_id":           strings.TrimSpace(intent.ModelID),
		"model_name":         strings.TrimSpace(intent.ModelName),
		"thinking_effort":    strings.TrimSpace(intent.ThinkingEffort),
		"mode":               intent.Mode.String(),
		"has_explicit_mode":  intent.HasExplicitMode,
		"mode_source":        string(intent.ModeSource),
		"starts_run":         intent.StartsRun,
		"subagent_type_name": strings.TrimSpace(intent.SubagentTypeName),
		"cancel_reason":      strings.TrimSpace(intent.CancelReason),
		"prewarm":            intent.Prewarm,
	}
	if len(intent.SubagentModelOverrides) > 0 {
		payload["subagent_model_overrides"] = subagentModelOverrideSummaries(intent.SubagentModelOverrides)
		payload["subagent_model_override_count"] = len(intent.SubagentModelOverrides)
	}
	if intent.ClientMessage != nil {
		payload["client_message"] = protoJSONDebugPayload(intent.ClientMessage)
	}
	if intent.ConversationState != nil {
		payload["conversation_state"] = protoJSONDebugPayload(intent.ConversationState)
	}
	if intent.UserMessage != nil {
		payload["user_message"] = protoJSONDebugPayload(intent.UserMessage)
	}
	if intent.RequestContext != nil {
		payload["request_context"] = protoJSONDebugPayload(intent.RequestContext)
	}
	if strings.TrimSpace(intent.IgnoredReason) != "" {
		payload["ignored_reason"] = strings.TrimSpace(intent.IgnoredReason)
		payload["ignored_empty_resume"] = strings.TrimSpace(intent.IgnoredReason) == "empty_resume_without_pending_continuation"
	}
	if intent.ExecClientMessage != nil {
		payload["exec_client_message"] = protoJSONDebugPayload(intent.ExecClientMessage)
	}
	if intent.ExecClientControlMessage != nil {
		payload["exec_client_control_message"] = protoJSONDebugPayload(intent.ExecClientControlMessage)
	}
	if intent.InteractionResponse != nil {
		payload["interaction_response"] = protoJSONDebugPayload(intent.InteractionResponse)
	}
	if intent.KVClientMessage != nil {
		payload["kv_client_message"] = protoJSONDebugPayload(intent.KVClientMessage)
	}
	return payload
}

func conversationActionKind(action *agentv1.ConversationAction) string {
	if action == nil {
		return ""
	}
	switch action.GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return "user_message_action"
	case *agentv1.ConversationAction_ResumeAction:
		return "resume_action"
	case *agentv1.ConversationAction_CancelAction:
		return "cancel_action"
	case *agentv1.ConversationAction_SummarizeAction:
		return "summarize_action"
	case *agentv1.ConversationAction_ShellCommandAction:
		return "shell_command_action"
	case *agentv1.ConversationAction_StartPlanAction:
		return "start_plan_action"
	case *agentv1.ConversationAction_ExecutePlanAction:
		return "execute_plan_action"
	case *agentv1.ConversationAction_AsyncAskQuestionCompletionAction:
		return "async_ask_question_completion_action"
	case *agentv1.ConversationAction_CancelSubagentAction:
		return "cancel_subagent_action"
	case *agentv1.ConversationAction_BackgroundTaskCompletionAction:
		return "background_task_completion_action"
	case *agentv1.ConversationAction_BackgroundShellAction:
		return "background_shell_action"
	case *agentv1.ConversationAction_BackgroundSubagentAction:
		return "background_subagent_action"
	default:
		return fmt.Sprintf("%T", action.GetAction())
	}
}
