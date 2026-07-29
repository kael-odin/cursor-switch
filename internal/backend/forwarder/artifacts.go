package forwarder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// artifactRecorder 只缓存当前 provider call 的请求摘要，用于本轮内 usage/model 信息补齐。
// 对话恢复事实只存在 state.json 与 context.json。
type artifactRecorder struct {
	store  *ConversationFileStore
	broker *StreamBroker
	debug  *debugRecorder

	mu       sync.RWMutex
	sessions map[string]artifactSession
}

type artifactSession struct {
	conversationID string
	turnSeq        int64
	requestPayload map[string]any
	summaryPayload map[string]any
}

type requestArtifactPrefix struct {
	Provider                string
	OpenAIEndpoint          string
	Model                   string
	PromptTokensTotal       int64
	ReplayMessageCount      int
	CanonicalBodyHash       string
	FrontierHash            string
	FrontierPath            string
	BreakpointCount         int
	ExpectedCacheRead       bool
	PreviousFrontierMatched bool
}

func newArtifactRecorder(store *ConversationFileStore, broker *StreamBroker, debug *debugRecorder) *artifactRecorder {
	recorder := &artifactRecorder{
		store:    store,
		broker:   broker,
		debug:    debug,
		sessions: make(map[string]artifactSession),
	}
	// F-36：broker 删除终态流时清掉该 request 的 session，避免 sessions map 永久增长。
	if broker != nil {
		broker.OnStreamRemoved = recorder.ClearActiveArtifactsByRequest
	}
	return recorder
}

func (recorder *artifactRecorder) RecordLLMRequest(requestID string, _ string, modelCallID string, payload map[string]any) (string, error) {
	session, err := recorder.ensureSession(requestID, modelCallID)
	if err != nil {
		return "", err
	}
	session.requestPayload = cloneStringAnyMap(payload)
	recorder.mu.Lock()
	recorder.sessions[artifactSessionKey(requestID, modelCallID)] = session
	recorder.mu.Unlock()
	if prefix, ok, err := decodeRequestPrefixPayload(session.requestPayload); err == nil && ok && prefix != nil {
		recorder.persistLatestRequestPrefix(session.conversationID, requestID, modelCallID, prefix)
	}
	recorder.debug.LogProviderArtifact(context.Background(), requestID, session.conversationID, modelCallID, "llm_request", session.requestPayload)
	return "", nil
}

func (recorder *artifactRecorder) AppendLLMResponseChunk(requestID string, runID string, modelCallID string, chunk string) (string, error) {
	// N-09：每个 SSE 行都走此路径。debug 关闭时 LogProviderArtifact 是 no-op，
	// 整段短路——不调 ensureSession、不持锁、不构造 payload map，与 LogProviderArtifact
	// 内部 enabled 短路对齐。debug 开启才付 ensureSession + 日志开销。
	if recorder == nil || recorder.debug == nil || !recorder.debug.enabled(context.Background()) {
		return "", nil
	}
	session, err := recorder.ensureSession(requestID, modelCallID)
	if err != nil {
		return "", err
	}
	recorder.debug.LogProviderArtifact(context.Background(), requestID, session.conversationID, modelCallID, "llm_response_chunk", map[string]any{
		"run_id":    strings.TrimSpace(runID),
		"raw_chunk": chunk,
		"byte_len":  len([]byte(chunk)),
	})
	return "", nil
}

func (recorder *artifactRecorder) RecordLLMSummary(requestID string, _ string, modelCallID string, payload map[string]any) (string, error) {
	session, err := recorder.ensureSession(requestID, modelCallID)
	if err != nil {
		return "", err
	}
	session.summaryPayload = cloneStringAnyMap(payload)
	recorder.mu.Lock()
	recorder.sessions[artifactSessionKey(requestID, modelCallID)] = session
	recorder.mu.Unlock()
	if prefix, ok, err := decodeRequestPrefixPayload(session.requestPayload); err == nil && ok && prefix != nil {
		if tokens := readInt64Value(session.summaryPayload["prompt_tokens_total"]); tokens > 0 {
			prefix.PromptTokensTotal = tokens
		}
		recorder.persistLatestRequestPrefix(session.conversationID, requestID, modelCallID, prefix)
	}
	recorder.debug.LogProviderArtifact(context.Background(), requestID, session.conversationID, modelCallID, "llm_summary", session.summaryPayload)
	return "", nil
}

func (recorder *artifactRecorder) persistLatestRequestPrefix(conversationID string, requestID string, modelCallID string, prefix *requestArtifactPrefix) {
	if recorder == nil || recorder.store == nil || prefix == nil || strings.TrimSpace(conversationID) == "" || strings.TrimSpace(conversationID) == "unknown" {
		return
	}
	_, _ = recorder.store.UpdateConversationMeta(conversationID, func(item *ConversationFile) error {
		if item == nil {
			return nil
		}
		item.LatestRequestPrefix = &ConversationRequestPrefix{
			RequestID:               strings.TrimSpace(requestID),
			ModelCallID:             firstNonEmpty(strings.TrimSpace(modelCallID), strings.TrimSpace(requestID)),
			Provider:                strings.TrimSpace(prefix.Provider),
			OpenAIEndpoint:          strings.TrimSpace(prefix.OpenAIEndpoint),
			Model:                   strings.TrimSpace(prefix.Model),
			PromptTokensTotal:       prefix.PromptTokensTotal,
			ReplayMessageCount:      prefix.ReplayMessageCount,
			CanonicalBodyHash:       strings.TrimSpace(prefix.CanonicalBodyHash),
			FrontierHash:            strings.TrimSpace(prefix.FrontierHash),
			FrontierPath:            strings.TrimSpace(prefix.FrontierPath),
			BreakpointCount:         prefix.BreakpointCount,
			ExpectedCacheRead:       prefix.ExpectedCacheRead,
			PreviousFrontierMatched: prefix.PreviousFrontierMatched,
			UpdatedAt:               time.Now().UTC(),
		}
		return nil
	})
}

func (recorder *artifactRecorder) ClearActiveArtifacts(requestID string, modelCallID string) {
	if recorder == nil {
		return
	}
	recorder.mu.Lock()
	delete(recorder.sessions, artifactSessionKey(requestID, modelCallID))
	recorder.mu.Unlock()
}

// ClearActiveArtifactsByRequest 清掉给定 request 下的全部 session（含 B2 failover 链各候选的
// model_call_id 产生的多条记录）。F-36：在 StreamBroker 删除活动流时由 OnStreamRemoved 回调，
// 终止"每次 provider request 永久保留请求/摘要 payload"的内存泄漏。
func (recorder *artifactRecorder) ClearActiveArtifactsByRequest(requestID string) {
	if recorder == nil {
		return
	}
	normalizedRequestID := strings.TrimSpace(requestID)
	if normalizedRequestID == "" {
		return
	}
	prefix := normalizedRequestID + eventIDSep
	recorder.mu.Lock()
	for key := range recorder.sessions {
		if strings.HasPrefix(key, prefix) {
			delete(recorder.sessions, key)
		}
	}
	recorder.mu.Unlock()
}

func (recorder *artifactRecorder) ensureSession(requestID string, modelCallID string) (artifactSession, error) {
	if recorder == nil {
		return artifactSession{}, fmt.Errorf("artifact recorder is nil")
	}
	key := artifactSessionKey(requestID, modelCallID)
	// N-09：命中（热路径，每个 SSE 行都查）走 RLock 读路径免锁竞争；
	// 未命中才升级写锁，double-check 后创建，避免并发创建重复 session。
	recorder.mu.RLock()
	if session, ok := recorder.sessions[key]; ok {
		recorder.mu.RUnlock()
		return session, nil
	}
	recorder.mu.RUnlock()
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	// double-check：持写锁后再查一次，可能已被并发创建者写入。
	if session, ok := recorder.sessions[key]; ok {
		return session, nil
	}
	conversationID, turnSeq := recorder.resolveConversationContext(requestID)
	session := artifactSession{
		conversationID: conversationID,
		turnSeq:        turnSeq,
	}
	recorder.sessions[key] = session
	return session, nil
}

func artifactSessionKey(requestID string, modelCallID string) string {
	// N-38：分隔符用 \x1f（eventIDSep）而非 "::"，与 usage EventID 一致，
	// 消除 requestID 含 "::" 时 ClearActiveArtifactsByRequest 前缀匹配跨 requestID 误清。
	return strings.TrimSpace(requestID) + eventIDSep + strings.TrimSpace(modelCallID)
}

func (recorder *artifactRecorder) resolveConversationContext(requestID string) (string, int64) {
	if recorder == nil || recorder.broker == nil {
		return "unknown", 0
	}
	stream, ok := recorder.broker.Get(requestID)
	if !ok || stream == nil {
		return "unknown", 0
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return sanitizeArtifactName(firstNonEmpty(stream.ConversationID, "unknown")), stream.TurnSeq
}

func decodeRequestPrefixPayload(payload map[string]any) (*requestArtifactPrefix, bool, error) {
	if len(payload) == 0 {
		return nil, false, nil
	}
	provider := strings.TrimSpace(readStringValue(payload["provider"]))
	model := strings.TrimSpace(readStringValue(payload["model"]))
	openAIEndpoint := strings.TrimSpace(readStringValue(payload["openai_endpoint"]))
	if provider == "" && model == "" && openAIEndpoint == "" {
		return nil, false, nil
	}
	replayMessageCount := replayMessageCountFromRequestPayload(payload)
	frontier := cacheFrontierFromRequestPayload(payload)
	return &requestArtifactPrefix{
		Provider:                provider,
		OpenAIEndpoint:          openAIEndpoint,
		Model:                   model,
		ReplayMessageCount:      replayMessageCount,
		CanonicalBodyHash:       frontier.CanonicalBodyHash,
		FrontierHash:            frontier.FrontierHash,
		FrontierPath:            frontier.FrontierPath,
		BreakpointCount:         frontier.BreakpointCount,
		ExpectedCacheRead:       frontier.ExpectedCacheRead,
		PreviousFrontierMatched: frontier.PreviousFrontierMatched,
	}, true, nil
}

type requestCacheFrontierPayload struct {
	CanonicalBodyHash       string
	FrontierHash            string
	FrontierPath            string
	BreakpointCount         int
	ExpectedCacheRead       bool
	PreviousFrontierMatched bool
}

func cacheFrontierFromRequestPayload(payload map[string]any) requestCacheFrontierPayload {
	var frontier requestCacheFrontierPayload
	knobs, ok := payload["request_knobs"].(map[string]any)
	if !ok {
		return frontier
	}
	rawFrontier, ok := knobs["cache_frontier"].(map[string]any)
	if !ok {
		return frontier
	}
	frontier.CanonicalBodyHash = strings.TrimSpace(readStringValue(rawFrontier["canonical_body_hash"]))
	frontier.FrontierHash = strings.TrimSpace(readStringValue(rawFrontier["frontier_hash"]))
	frontier.FrontierPath = strings.TrimSpace(readStringValue(rawFrontier["frontier_path"]))
	frontier.BreakpointCount = int(readInt64Value(rawFrontier["breakpoint_count"]))
	frontier.ExpectedCacheRead = readBoolValue(rawFrontier["expected_cache_read"])
	frontier.PreviousFrontierMatched = readBoolValue(rawFrontier["previous_frontier_matched"])
	return frontier
}

func replayMessageCountFromRequestPayload(payload map[string]any) int {
	switch messages := payload["messages_summary"].(type) {
	case []map[string]any:
		return replayMessageCountFromSummaryObjects(messages)
	case []any:
		items := make([]map[string]any, 0, len(messages))
		for _, item := range messages {
			message, ok := item.(map[string]any)
			if ok {
				items = append(items, message)
			}
		}
		return replayMessageCountFromSummaryObjects(items)
	default:
		return 0
	}
}

func replayMessageCountFromSummaryObjects(messages []map[string]any) int {
	count := 0
	for _, message := range messages {
		if strings.TrimSpace(readStringValue(message["role"])) == "system" {
			continue
		}
		count++
	}
	return count
}

// sanitizeArtifactName 把任意字符串压成可安全用作文件/目录名的段。
//
// 替换 `/`/`\`/`:`/空格 为 `_`，并拒绝 `"."`/`".."`（F-04 目录逃逸）——
// 这两个值不含分隔符但会令 filepath.Join 上跳到父目录，所以单独拦截。
// 空值回退为 "unknown"。
func sanitizeArtifactName(value string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	normalized := replacer.Replace(strings.TrimSpace(value))
	if normalized == "" || normalized == "." || normalized == ".." {
		return "unknown"
	}
	return normalized
}

func artifactConversationDir(historyRoot string, conversationID string) string {
	return filepath.Join(strings.TrimSpace(historyRoot), sanitizeArtifactName(conversationID))
}

func openUniqueArtifactTempFile(path string) (*os.File, string, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	for attempt := 0; attempt < 32; attempt++ {
		tempPath := filepath.Join(dir, fmt.Sprintf("%s.tmp-%d-%d", base, os.Getpid(), time.Now().UnixNano()))
		file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			return file, tempPath, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("exhausted temp file attempts")
}

func renameArtifactTempFile(tempPath string, path string) error {
	attempts := 1
	delay := 10 * time.Millisecond
	if runtime.GOOS == "windows" {
		attempts = 12
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := os.Rename(tempPath, path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if runtime.GOOS != "windows" || os.IsNotExist(lastErr) || attempt == attempts-1 {
			break
		}
		time.Sleep(delay)
		if delay < 200*time.Millisecond {
			delay *= 2
		}
	}
	return lastErr
}
