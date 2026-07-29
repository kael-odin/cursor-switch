// service.go 实现 forwarder 的主链路：Bidi 上行归一化、history 写入、provider 驱动和 RunSSE 下行。
package forwarder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/internal/appdata"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	interactionbridge "cursor/internal/backend/agent/bridge/interaction"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
	protocol "cursor/internal/backend/agent/protocol"
	serverconfig "cursor/internal/backend/server/config"
)

const (
	providerResumeDebounce         = 200 * time.Millisecond
	completedExecRetention         = 15 * time.Second
	nonStreamingExecCloseGrace     = 1500 * time.Millisecond
	defaultSummaryCompletedThought = "Chat context summarized"
	// encryptedReasoningPlaceholder 是 OpenAI Responses 模型返回加密 thinking（仅有
	// encrypted_content / ReasoningSignature、无明文 reasoning_summary_text delta）时的
	// 占位文本。BYOK 代理无法解密——加密内容只对官方 Cursor/OpenAI 后端可读（N-34）。
	// 文案诚实声明"加密、本地不可读"，不再用"Please wait a moment"误导用户等待
	// 永远不会到来的明文。有明文 summary delta 时走正常 thinking delta 路径，不触发此占位。
	encryptedReasoningPlaceholder = "Reasoning is encrypted by the provider and not available through BYOK."
	providerDefaultMaxOutputTokens = 65536
	providerOutputSafetyTokens     = 4096

	runtimeThinkingEffortParameterID = "thinking_effort"
)

type parsedSubagentModelOverrides struct {
	Overrides map[string]runtimecore.SubagentModelOverrideSelection
	Ignored   []map[string]any
	RawCount  int
}


type Service struct {
	store              *ConversationFileStore
	usageStore         *UsageFileStore
	codebaseIndexStore *CodebaseIndexStore
	docsIndexStore     *DocsIndexStore
	rules              *UserRuleStore
	projector          *HistoryProjector
	compiler           PromptCompiler
	provider           ProviderGateway
	resolver           modeladapter.ChannelResolver
	modelMemory        agentModelMemory
	broker             *StreamBroker
	recorder           *artifactRecorder
	debug              *debugRecorder
	execBridge         execbridge.ExecBridge
	interactionBridge  interactionbridge.InteractionBridge
	appendSeq          *appendSequenceTracker
}

type agentModelMemory interface {
	LastAgentModelHash() string
	SaveLastAgentModelHash(context.Context, string) error
}

// webToolsConfigProvider 由 *serverconfig.Manager 实现，返回当前 WebSearch/WebFetch 工具配置。
// forwarder 经类型断言从 ChannelResolver（host.configs）拿到它，注入给 interaction.Bridge，
// 使 WebSearch 多 provider 与 WebFetch host 白名单能实时读到用户配置（审计「行为偏离-3」）。
type webToolsConfigProvider interface {
	WebTools() serverconfig.WebToolsConfig
}

// NewService 使用默认依赖创建 forwarder 服务。
func NewService(historyRoot string, resolver modeladapter.ChannelResolver) *Service {
	projector := NewHistoryProjector()
	store := NewConversationFileStore(historyRoot)
	broker := NewStreamBroker()
	rules := NewUserRuleStore(appdata.RulesRootPath())
	var modelMemory agentModelMemory
	if candidate, ok := resolver.(agentModelMemory); ok {
		modelMemory = candidate
	}
	var debugConfig debugLogConfig
	if candidate, ok := resolver.(debugLogConfig); ok {
		debugConfig = candidate
	}
	// 审计「行为偏离-3」：若 resolver（host.configs）实现了 webToolsConfigProvider，
	// 把 WebTools 读取函数注入 interaction.Bridge，使 WebSearch 多 provider 与 WebFetch
	// host 白名单能实时读用户配置。未实现时 NewBridge(nil) 走默认降级行为。
	var interactionBridge interactionbridge.InteractionBridge = interactionbridge.NewBridge()
	if provider, ok := resolver.(webToolsConfigProvider); ok {
		reader := func() interactionbridge.WebToolsConfig {
			cfg := provider.WebTools()
			return interactionbridge.WebToolsConfig{
				WebSearchProvider:      cfg.WebSearchProvider,
				WebSearchAPIKey:        cfg.WebSearchAPIKey,
				WebFetchHostAllowlist:  cfg.WebFetchHostAllowlist,
			}
		}
		interactionBridge = interactionbridge.NewBridge(reader)
	}
	debug := newDebugRecorder(historyRoot, broker, debugConfig)
	service := &Service{
		store:              store,
		usageStore:         NewUsageFileStore(historyRoot),
		codebaseIndexStore: NewCodebaseIndexStore(appdata.CodebaseIndexRootPath()),
		docsIndexStore:     NewDocsIndexStore(appdata.DocsIndexRootPath()),
		rules:              rules,
		projector:          projector,
		compiler:           NewPromptCompiler(projector, NewToolCatalog(), NewReminderInjector(), rules),
		provider:           NewProviderGateway(resolver),
		resolver:           resolver,
		modelMemory:        modelMemory,
		broker:             broker,
		recorder:           newArtifactRecorder(store, broker, debug),
		debug:              debug,
		execBridge:         execbridge.NewBridge(),
		interactionBridge:  interactionBridge,
		appendSeq:          newAppendSequenceTracker(),
	}
	service.startHistoryMaintenance()
	return service
}

// newServiceWithDependencies 主要用于测试场景，允许注入替身依赖。
func newServiceWithDependencies(store *ConversationFileStore, projector *HistoryProjector, compiler PromptCompiler, provider ProviderGateway, broker *StreamBroker) *Service {
	historyRoot := ""
	if store != nil {
		historyRoot = store.HistoryDir()
	}
	debug := newDebugRecorder(historyRoot, broker, nil)
	return &Service{
		store:              store,
		rules:              NewUserRuleStore(appdata.RulesRootPath()),
		projector:          projector,
		compiler:           compiler,
		provider:           provider,
		broker:             broker,
		usageStore:         NewUsageFileStore(store.HistoryDir()),
		codebaseIndexStore: NewCodebaseIndexStore(appdata.CodebaseIndexRootPath()),
		docsIndexStore:     NewDocsIndexStore(appdata.DocsIndexRootPath()),
		recorder:           newArtifactRecorder(store, broker, debug),
		debug:              debug,
		execBridge:         execbridge.NewBridge(),
		interactionBridge:  interactionbridge.NewBridge(),
		appendSeq:          newAppendSequenceTracker(),
	}
}

// BidiAppend 处理 legacy Bidi 上行，把用户输入和外部结果归一化后写入 history。
func (service *Service) BidiAppend(ctx context.Context, req *connect.Request[aiserverv1.BidiAppendRequest]) (*connect.Response[aiserverv1.BidiAppendResponse], error) {
	if service == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	requestID := protocol.NormalizeRequestID(protocol.ReadAppendRequestID(req.Msg))
	if requestID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_id is required"))
	}
	appendSeqno := req.Msg.GetAppendSeqno()
	dataHex := req.Msg.GetData()
	appendTicket, staleAppend, err := service.appendSeq.Acquire(ctx, requestID, appendSeqno)
	if err != nil {
		return nil, connect.NewError(connect.CodeCanceled, err)
	}
	if staleAppend {
		log.Printf("forwarder ignored stale bidi append request_id=%s append_seqno=%d", requestID, appendSeqno)
		service.debug.LogBidiRaw(ctx, requestID, "", appendSeqno, dataHex, "stale", nil)
		return connect.NewResponse(&aiserverv1.BidiAppendResponse{}), nil
	}
	defer appendTicket.Release()
	message, clientKind, err := protocol.DecodeAgentClientMessage(dataHex)
	if err != nil {
		service.debug.LogBidiRaw(ctx, requestID, "", appendSeqno, dataHex, "decode_error", map[string]any{
			"error": err.Error(),
		})
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	intent, err := service.decodeInboundIntent(requestID, message, clientKind)
	if err != nil {
		service.debug.LogBidiRaw(ctx, requestID, "", appendSeqno, dataHex, "intent_error", map[string]any{
			"client_kind": strings.TrimSpace(clientKind),
			"error":       err.Error(),
		})
		service.debug.LogBidiDecoded(ctx, requestID, "", appendSeqno, clientKind, message, InboundIntent{RequestID: requestID}, map[string]any{
			"error": err.Error(),
		})
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	service.debug.LogBidiRaw(ctx, requestID, intent.ConversationID, appendSeqno, dataHex, "accepted", map[string]any{
		"client_kind": strings.TrimSpace(clientKind),
	})
	service.debug.LogBidiDecoded(ctx, requestID, intent.ConversationID, appendSeqno, clientKind, message, intent, nil)
	if err := service.dispatchInboundIntent(intent); err != nil {
		if shouldAcknowledgeInterruptedInboundIntent(intent, err) {
			service.debug.LogRuntime(ctx, requestID, intent.ConversationID, "dispatch_interrupted_ignored", map[string]any{
				"kind":  strings.TrimSpace(intent.Kind),
				"error": err.Error(),
			})
			return connect.NewResponse(&aiserverv1.BidiAppendResponse{}), nil
		}
		service.debug.LogRuntime(ctx, requestID, intent.ConversationID, "dispatch_error", map[string]any{
			"kind":  strings.TrimSpace(intent.Kind),
			"error": err.Error(),
		})
		code := connect.CodeInvalidArgument
		if strings.TrimSpace(intent.Kind) == "run" {
			code = connect.CodeInternal
		}
		return nil, connect.NewError(code, err)
	}
	service.debug.LogRuntime(ctx, requestID, intent.ConversationID, "inbound_intent_dispatched", map[string]any{
		"kind":            strings.TrimSpace(intent.Kind),
		"thinking_effort": strings.TrimSpace(intent.ThinkingEffort),
		"prewarm":         intent.Prewarm,
		"ignored_reason":  strings.TrimSpace(intent.IgnoredReason),
	})

	return connect.NewResponse(&aiserverv1.BidiAppendResponse{}), nil
}

func shouldAcknowledgeInterruptedInboundIntent(intent InboundIntent, err error) bool {
	if !errors.Is(err, errProviderLoopInterrupted) {
		return false
	}
	switch strings.TrimSpace(intent.Kind) {
	case "metadata", "kv_result", "exec_result", "exec_control", "interaction_result", "cancel":
		return true
	default:
		return false
	}
}

// RunSSE 订阅指定 request 的活动流，优先回放 backlog，在 backlog 清空期间按 5 秒周期发送心跳。
func (service *Service) RunSSE(ctx context.Context, req *connect.Request[aiserverv1.BidiRequestId], stream *connect.ServerStream[agentv1.AgentServerMessage]) error {
	if service == nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	requestID := protocol.NormalizeRequestID(protocol.ReadBidiRequestID(req.Msg))
	if requestID == "" {
		return buildRunSSECustomError(connect.CodeInvalidArgument, "请求参数无效", fmt.Errorf("request_id is required"))
	}
	subscriberID, signal, err := service.broker.Subscribe(requestID)
	if err != nil {
		return buildRunSSECustomError(connect.CodeInvalidArgument, "请求参数无效", err)
	}
	service.debug.LogRunSSE(ctx, requestID, "", "subscribe", map[string]any{
		"subscriber_id": subscriberID,
	})
	defer func() {
		remaining := service.broker.Unsubscribe(requestID, subscriberID)
		service.debug.LogRunSSE(context.Background(), requestID, "", "unsubscribe", map[string]any{
			"subscriber_id":         subscriberID,
			"remaining_subscribers": remaining,
		})
		if remaining == 0 {
			// RunSSE 连接短暂抖动时，给活跃 provider 一段重连宽限期，
			// 避免把本来还能正常收口的请求直接打成 context canceled。
			if !service.scheduleOrphanCancelActor(requestID, "[canceled] RunSSE client disconnected") {
				service.broker.RemoveIfIdle(requestID)
			}
		}
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	cursor := 0
	for {
		backlog, err := service.broker.ReadFromCursor(requestID, cursor)
		if err != nil {
			service.debug.LogRunSSE(ctx, requestID, "", "read_error", map[string]any{
				"cursor": cursor,
				"error":  err.Error(),
			})
			return nil
		}
		if len(backlog) > 0 {
			for _, event := range backlog {
				if event.Message != nil {
					if err := stream.Send(event.Message); err != nil {
						service.debug.LogRunSSE(ctx, requestID, "", "send_error", map[string]any{
							"cursor":       cursor,
							"message_case": agentServerMessageCase(event.Message),
							"message":      protoJSONDebugPayload(event.Message),
							"error":        err.Error(),
						})
						return err
					}
					service.debug.LogRunSSE(ctx, requestID, "", "send_message", map[string]any{
						"cursor":       cursor,
						"message_case": agentServerMessageCase(event.Message),
						"message":      protoJSONDebugPayload(event.Message),
					})
				}
				cursor++
				if event.End {
					service.debug.LogRunSSE(ctx, requestID, "", "terminal", map[string]any{
						"cursor":                 cursor,
						"terminal_error_code":    strings.TrimSpace(event.TerminalErrorCode),
						"terminal_error_message": strings.TrimSpace(event.TerminalErrorMessage),
					})
					return buildTerminalStreamError(event)
				}
			}
			continue
		}
		select {
		case <-ctx.Done():
			service.debug.LogRunSSE(ctx, requestID, "", "client_context_done", map[string]any{
				"cursor": cursor,
				"error":  ctx.Err().Error(),
			})
			if backlog, err := service.broker.ReadFromCursor(requestID, cursor); err == nil {
				for _, event := range backlog {
					cursor++
					if event.End {
						service.debug.LogRunSSE(context.Background(), requestID, "", "terminal_after_context_done", map[string]any{
							"cursor":                 cursor,
							"terminal_error_code":    strings.TrimSpace(event.TerminalErrorCode),
							"terminal_error_message": strings.TrimSpace(event.TerminalErrorMessage),
						})
						return buildTerminalStreamError(event)
					}
				}
			}
			return nil
		case <-signal:
			continue
		case <-ticker.C:
		}
		if backlog, err := service.broker.ReadFromCursor(requestID, cursor); err != nil {
			service.debug.LogRunSSE(ctx, requestID, "", "read_error", map[string]any{
				"cursor": cursor,
				"error":  err.Error(),
			})
			return nil
		} else if len(backlog) > 0 {
			continue
		}
		heartbeat := buildHeartbeatMessage()
		if err := stream.Send(heartbeat); err != nil {
			service.debug.LogRunSSE(ctx, requestID, "", "heartbeat_error", map[string]any{
				"cursor":       cursor,
				"message_case": agentServerMessageCase(heartbeat),
				"message":      protoJSONDebugPayload(heartbeat),
				"error":        err.Error(),
			})
			return err
		}
		service.debug.LogRunSSE(ctx, requestID, "", "heartbeat", map[string]any{
			"cursor":       cursor,
			"message_case": agentServerMessageCase(heartbeat),
			"message":      protoJSONDebugPayload(heartbeat),
		})
	}
}

// decodeInboundIntent 把 legacy AgentClientMessage 映射为 forwarder 内部 intent。
func (service *Service) decodeInboundIntent(requestID string, message *agentv1.AgentClientMessage, clientKind string) (InboundIntent, error) {
	intent := InboundIntent{
		RequestID:     strings.TrimSpace(requestID),
		ClientMessage: message,
	}
	var err error
	switch strings.TrimSpace(clientKind) {
	case "run_request":
		runRequest := message.GetRunRequest()
		if runRequest == nil {
			return InboundIntent{}, fmt.Errorf("run_request payload is required")
		}
		conversationID := strings.TrimSpace(runRequest.GetConversationId())
		if conversationID == "" {
			return InboundIntent{}, fmt.Errorf("conversation_id is required in run_request")
		}
		intent.ConversationID = conversationID
		intent.ConversationState = runRequest.GetConversationState()
		intent.UserMessage = extractUserMessage(message)
		intent.RequestContext = extractRequestContext(message)
		if service.shouldIgnoreEmptyResumeRunRequest(requestID, runRequest, intent.UserMessage, intent.RequestContext) {
			intent.Kind = "metadata"
			intent.StartsRun = false
			intent.HasExplicitMode = false
			intent.ModeSource = ModeSourceUnknown
			intent.IgnoredReason = "empty_resume_without_pending_continuation"
			return intent, nil
		}
		intent.Kind = "run"
		intent.StartsRun = true
		intent.Mode, intent.ModeSource, intent.HasExplicitMode, err = extractRunMode(message)
		if err != nil {
			return InboundIntent{}, err
		}
		intent.ModelID = extractRequestedModelID(message)
		intent.ThinkingEffort = extractRuntimeThinkingEffort(message)
		intent.SubagentTypeName = strings.TrimSpace(runRequest.GetSubagentTypeName())
		parsedOverrides := parseSubagentModelOverrides(runRequest.GetSubagentModelOverrides())
		intent.SubagentModelOverrides = parsedOverrides.Overrides
		service.debug.LogRuntime(context.Background(), intent.RequestID, intent.ConversationID, "subagent_model_overrides_parsed", map[string]any{
			"override_count": parsedOverrides.RawCount,
			"valid_count":    len(parsedOverrides.Overrides),
			"ignored_count":  len(parsedOverrides.Ignored),
			"overrides":      subagentModelOverrideSummaries(parsedOverrides.Overrides),
			"ignored":        parsedOverrides.Ignored,
		})
		if intent.ModelID == "" {
			intent.ModelID = "default"
		}
		intent.ModelName = service.resolveRequestedModelName(message, intent.ModelID)
	case "prewarm_request":
		prewarmRequest := message.GetPrewarmRequest()
		if prewarmRequest == nil {
			return InboundIntent{}, fmt.Errorf("prewarm_request payload is required")
		}
		conversationID := strings.TrimSpace(prewarmRequest.GetConversationId())
		if conversationID == "" {
			return InboundIntent{}, fmt.Errorf("conversation_id is required in prewarm_request")
		}
		intent.Kind = "run"
		intent.Prewarm = true
		intent.StartsRun = true
		intent.ConversationID = conversationID
		intent.SubagentTypeName = strings.TrimSpace(prewarmRequest.GetSubagentTypeName())
		intent.ConversationState = prewarmRequest.GetConversationState()
		intent.Mode, intent.ModeSource, intent.HasExplicitMode, err = extractPrewarmMode(prewarmRequest)
		if err != nil {
			return InboundIntent{}, err
		}
		intent.ModelID = firstNonEmpty(extractRequestedModelID(message), "default")
		intent.ThinkingEffort = extractRuntimeThinkingEffort(message)
		intent.ModelName = service.resolveRequestedModelName(message, intent.ModelID)
	case "conversation_action":
		action := message.GetConversationAction()
		if action == nil {
			return InboundIntent{}, fmt.Errorf("conversation_action payload is required")
		}
		intent.UserMessage = extractConversationActionUserMessage(action)
		intent.RequestContext = extractConversationActionRequestContext(action)
		intent.StartsRun = conversationActionStartsRun(action)
		intent.Mode, intent.ModeSource, intent.HasExplicitMode, err = extractConversationActionMode(action)
		if err != nil {
			return InboundIntent{}, err
		}
		switch item := action.GetAction().(type) {
		case *agentv1.ConversationAction_CancelAction:
			intent.Kind = "cancel"
			intent.CancelReason = strings.TrimSpace(item.CancelAction.GetReason())
		default:
			if intent.StartsRun || intent.HasExplicitMode {
				if stream, ok := service.broker.Get(intent.RequestID); ok && stream != nil {
					stream.mu.Lock()
					intent.ConversationID = strings.TrimSpace(stream.ConversationID)
					intent.ModelID = strings.TrimSpace(stream.ModelID)
					intent.ModelName = strings.TrimSpace(stream.ModelName)
					intent.ThinkingEffort = strings.TrimSpace(stream.ThinkingEffort)
					if !intent.HasExplicitMode && stream.Mode != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
						intent.Mode = stream.Mode
					}
					if stream.CheckpointConversation != nil {
						intent.SubagentTypeName = strings.TrimSpace(stream.CheckpointConversation.SubagentTypeName)
					}
					stream.mu.Unlock()
				}
				if strings.TrimSpace(intent.ConversationID) == "" {
					return InboundIntent{}, fmt.Errorf("conversation_action requires active request context")
				}
			}
			if intent.StartsRun {
				intent.Kind = "run"
				intent.StartsRun = true
				if intent.ModelID == "" {
					intent.ModelID = "default"
				}
			} else {
				intent.Kind = "metadata"
			}
		}
	case "exec_client_message":
		intent.Kind = "exec_result"
		intent.ExecClientMessage = message.GetExecClientMessage()
	case "exec_client_control_message":
		intent.Kind = "exec_control"
		intent.ExecClientControlMessage = message.GetExecClientControlMessage()
	case "interaction_response":
		intent.Kind = "interaction_result"
		intent.InteractionResponse = message.GetInteractionResponse()
	case "kv_client_message":
		intent.Kind = "kv_result"
		intent.KVClientMessage = message.GetKvClientMessage()
	case "client_heartbeat":
		intent.Kind = "metadata"
	default:
		return InboundIntent{}, fmt.Errorf("unsupported client message kind: %s", clientKind)
	}
	return intent, nil
}

// handleRunIntent 处理 run/prewarm 类 intent，负责建会话、写 turn 和拉起 provider。
func (service *Service) handleRunIntent(intent InboundIntent) error {
	intent.UserMessage = normalizeUserMessageForStorage(intent.UserMessage)
	if !intent.Prewarm {
		service.cancelOtherConversationActors(
			intent.ConversationID,
			intent.RequestID,
			"[canceled] Superseded by newer request",
		)
	}
	conversation, effectiveMode, turnSeq, initialEntries, err := service.bootstrapRuntimeConversation(intent)
	if err != nil {
		return err
	}
	rewindDecision := service.decideRunRewind(intent, conversation)
	if rewindDecision.Evaluated && !rewindDecision.Apply {
		service.logRunRewindDecision(intent.RequestID, intent.ConversationID, "rewind_skipped", rewindDecision)
	}
	if rewindDecision.Apply {
		service.logRunRewindDecision(intent.RequestID, intent.ConversationID, "rewind_detected", rewindDecision)
		turnSeq = rewindDecision.TargetTurnSeq
		initialEntries, err = buildRunEntries(intent, effectiveMode, turnSeq)
		if err != nil {
			return err
		}
	}
	if service.store != nil {
		if rewindDecision.Apply {
			persisted, err := service.store.ReplaceEntries(
				intent.ConversationID,
				appendReplacementRunEntries(rewindDecision.PrefixEntries, initialEntries),
				func(item *ConversationFile) error {
					applyRunRewindMetadata(item, conversation, intent, turnSeq)
					return nil
				},
			)
			if err != nil {
				return err
			}
			if persisted != nil {
				conversation = persisted
			}
			service.logRunRewindDecision(intent.RequestID, intent.ConversationID, "rewind_applied", rewindDecision)
		} else {
			persisted, err := service.store.SaveConversationWithEntries(intent.ConversationID, conversation, initialEntries)
			if err != nil {
				return err
			}
			if persisted != nil {
				conversation = persisted
			}
		}
	} else if rewindDecision.Apply {
		service.applyRunRewindToConversation(conversation, rewindDecision, initialEntries, intent, turnSeq)
		service.logRunRewindDecision(intent.RequestID, intent.ConversationID, "rewind_applied", rewindDecision)
	} else if len(initialEntries) > 0 {
		appendEntriesInPlace(conversation, initialEntries)
		deriveConversationLoopState(conversation)
	}

	stream, err := service.broker.OpenStream(intent.RequestID, intent.ConversationID, turnSeq, intent.ModelID, intent.ModelName, effectiveMode, userMessageText(intent.UserMessage))
	if err != nil {
		return err
	}
	if stream == nil {
		return fmt.Errorf("open stream failed")
	}
	if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
		return err
	}
	updateStreamRequestContextData(stream, intent.RequestContext)
	service.updateStreamMCPToolServers(stream, intent.RequestContext)
	clearPendingProviderCompletion(stream)
	stream.mu.Lock()
	stream.ThinkingEffort = strings.TrimSpace(intent.ThinkingEffort)
	stream.SubagentModelOverrides = cloneSubagentModelOverrides(intent.SubagentModelOverrides)
	stream.PendingProviderAction = providerActionNone
	stream.PendingCompaction = nil
	stream.PendingExecs = make(map[string]runtimecore.PendingExec)
	stream.PendingInteractions = make(map[string]runtimecore.PendingInteraction)
	stream.RecentCompletedExecs = make(map[uint32]time.Time)
	stream.BackgroundShells = make(map[string]*BackgroundShellState)
	stream.BackgroundShellsByMessageID = make(map[uint32]string)
	stream.BackgroundShellsByExecID = make(map[string]string)
	stream.TimerTokens = make(map[string]uint64)
	stream.CurrentProviderToken = 0
	stream.CurrentCompactionToken = 0
	stream.ProviderAccumulatedText.Reset()
	stream.ProviderAccumulatedReasoning.Reset()
	stream.ProviderAccumulatedReasoningSignature = ""
	stream.ProviderAccumulatedReasoningSignatureSource = ""
	stream.ProviderAccumulatedReasoningItemID = ""
	stream.ProviderAccumulatedReasoningStatus = ""
	stream.ProviderAccumulatedReasoningSummary = nil
	stream.ProviderSyntheticThinkingStartedAt = time.Time{}
	stream.ProviderSyntheticThinkingPublished = false
	stream.ProviderFinishReason = ""
	stream.ProviderUsage = turnUsageSnapshot{}
	stream.ToolInvocationCount = 0
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	service.setTurnPhase(stream, TurnPhaseIdle)
	service.debug.LogRuntime(context.Background(), intent.RequestID, intent.ConversationID, "stream_state_updated", map[string]any{
		"turn_seq":                      turnSeq,
		"model_id":                      strings.TrimSpace(intent.ModelID),
		"model_name":                    strings.TrimSpace(intent.ModelName),
		"thinking_effort":               strings.TrimSpace(intent.ThinkingEffort),
		"mode":                          effectiveMode.String(),
		"prewarm":                       intent.Prewarm,
		"subagent_type":                 strings.TrimSpace(intent.SubagentTypeName),
		"subagent_model_override_count": len(intent.SubagentModelOverrides),
		"subagent_model_overrides":      subagentModelOverrideSummaries(intent.SubagentModelOverrides),
		"latest_user_text":              userMessageText(intent.UserMessage),
	})
	if err := service.publishCheckpoint(intent.RequestID, intent.ConversationID); err != nil {
		return err
	}
	if intent.Prewarm {
		return nil
	}
	return service.requestProviderAction(stream, providerActionStart)
}

func (service *Service) loadPreviousSummaryReplay(conversationID string) ([][]byte, bool, error) {
	if service == nil || strings.TrimSpace(conversationID) == "" {
		return nil, false, nil
	}
	return service.loadLatestCarryForwardReplay(conversationID)
}

func (service *Service) snapshotVisibleTurns(conversation *ConversationFile) ([][]byte, error) {
	if service == nil || service.projector == nil || conversation == nil {
		return nil, nil
	}
	state, err := service.projector.ProjectLegacyCheckpoint(conversation)
	if err != nil {
		return nil, err
	}
	return cloneByteSlices(state.GetTurns()), nil
}

// handleCancelIntent 处理取消请求，并向客户端发送执行桥 abort。
func (service *Service) handleCancelIntent(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", intent.RequestID)
	}
	hasCheckpoint := checkpointConversationInitialized(stream)
	if hasCheckpoint {
		cancelReason := firstNonEmpty(intent.CancelReason, "user aborted")
		_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			newMetadataEntry(stream.TurnSeq, intent.RequestID, "control", map[string]any{
				"status":        "canceled",
				"reason":        cancelReason,
				"replay_policy": cancelReplayPolicyForReason(cancelReason),
			}),
		})
		if err != nil {
			return err
		}
	}
	stream.mu.Lock()
	pendingExecs := make([]runtimecore.PendingExec, 0, len(stream.PendingExecs))
	for _, pending := range stream.PendingExecs {
		pendingExecs = append(pendingExecs, pending)
	}
	stream.mu.Unlock()
	for _, pending := range pendingExecs {
		_ = service.broker.Publish(intent.RequestID, StreamEvent{
			Message: buildExecAbortMessage(pending),
		})
	}
	if hasCheckpoint {
		if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
			return err
		}
	}
	clearPendingProviderCompletion(stream)
	stream.mu.Lock()
	stream.PendingProviderAction = providerActionNone
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	service.setTurnPhase(stream, TurnPhaseCanceled)
	return service.broker.Cancel(intent.RequestID, firstNonEmpty(intent.CancelReason, "[canceled] User aborted request"))
}


// handleMetadataIntent 处理当前不驱动 provider 的轻量元数据上行。
func (service *Service) handleMetadataIntent(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		if intent.HasExplicitMode || intent.StartsRun {
			return fmt.Errorf("metadata intent requires active request context: %s", intent.RequestID)
		}
		return nil
	}
	backgroundShellToolCallID, backgroundShellActionWasNew := observeBackgroundShellAction(stream, intent.ClientMessage)
	observeBackgroundTaskCompletionAction(stream, intent.ClientMessage)
	if !checkpointConversationInitialized(stream) {
		if intent.HasExplicitMode {
			stream.mu.Lock()
			stream.Mode = intent.Mode
			stream.UpdatedAt = time.Now().UTC()
			stream.mu.Unlock()
		}
		return nil
	}
	entries := []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "metadata", map[string]any{
			"kind":       intent.Kind,
			"starts_run": intent.StartsRun,
		}),
	}
	if backgroundShellToolCallID != "" && backgroundShellActionWasNew {
		entries = append(entries, newBackgroundShellActionMetadataEntry(stream.TurnSeq, stream.RequestID, backgroundShellToolCallID, backgroundShellActionSourceClient))
	}
	entries = append(entries, backgroundTaskCompletionMetadataEntries(stream.TurnSeq, stream.RequestID, intent.ClientMessage)...)
	if intent.HasExplicitMode {
		modeEntry, err := newModeMetadataEntry(stream.TurnSeq, stream.RequestID, intent.Mode, true, intent.ModeSource)
		if err != nil {
			return err
		}
		modeAliasValue, err := modeAlias(intent.Mode)
		if err != nil {
			return err
		}
		entries = append(entries, modeEntry, newModeChangePromptContextEntry(stream.TurnSeq, stream.RequestID, intent.Mode))
		stream.mu.Lock()
		stream.Mode = intent.Mode
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		if _, err := service.updateConversationMetaAndCheckpoint(stream, stream.ConversationID, func(item *ConversationFile) error {
			if item == nil {
				return nil
			}
			item.Mode = modeAliasValue
			return nil
		}); err != nil {
			return err
		}
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, entries); err != nil {
		return err
	}
	if intent.HasExplicitMode {
		stream.mu.Lock()
		modelCallID := strings.TrimSpace(stream.CurrentModelCallID)
		stream.mu.Unlock()
		if modelCallID != "" {
			if err := service.syncSummaryCarryForward(stream.ConversationID, intent.RequestID, modelCallID); err != nil {
				return err
			}
		}
		if err := service.publishCheckpoint(intent.RequestID, stream.ConversationID); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) scheduleProviderResume(stream *ActiveStream, _ int) error {
	return service.requestProviderAction(stream, providerActionResume)
}

func shouldResumeAfterToolResults(finishReason string) bool {
	switch strings.TrimSpace(finishReason) {
	case "tool_use", "tool_calls", "function_call":
		return true
	default:
		return false
	}
}

func (service *Service) cancelScheduledProviderResume(stream *ActiveStream) {
	if stream == nil {
		return
	}
	clearStreamTimer(stream, providerTimerKey(streamTimerProviderResume, ""))
}

// driveProvider 由 actor 触发一次 provider pass，并把真实流包装成 provider_event 回投 mailbox。
func (service *Service) driveProvider(stream *ActiveStream) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	if stream.ProviderActive || stream.Status == StreamStatusCanceled || stream.Status == StreamStatusCompleted || stream.Status == StreamStatusFailed {
		stream.mu.Unlock()
		return nil
	}
	stream.ProviderPassCount++
	currentPass := stream.ProviderPassCount
	stream.Status = StreamStatusStreaming
	stream.PendingProviderAction = providerActionNone
	stream.CurrentModelCallID = uuid.NewString()
	stream.CurrentProviderToken++
	currentToken := stream.CurrentProviderToken
	stream.ProviderAccumulatedText.Reset()
	stream.ProviderAccumulatedReasoning.Reset()
	stream.ProviderAccumulatedReasoningSignature = ""
	stream.ProviderAccumulatedReasoningSignatureSource = ""
	stream.ProviderAccumulatedReasoningItemID = ""
	stream.ProviderAccumulatedReasoningStatus = ""
	stream.ProviderAccumulatedReasoningSummary = nil
	if stream.ProviderSyntheticThinkingStartedAt.IsZero() {
		stream.ProviderSyntheticThinkingStartedAt = time.Now().UTC()
	}
	stream.ProviderFinishReason = ""
	stream.ProviderUsage = turnUsageSnapshot{}
	stream.ToolInvocationCount = 0
	modelCallID := stream.CurrentModelCallID
	conversationID := stream.ConversationID
	requestID := stream.RequestID
	modelID := stream.ModelID
	modelName := stream.ModelName
	thinkingEffort := stream.ThinkingEffort
	mode := stream.Mode
	latestUserText := stream.LatestUserText
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	log.Printf("forwarder provider pass started request_id=%s model_call_id=%s provider_pass=%d", strings.TrimSpace(requestID), strings.TrimSpace(modelCallID), currentPass)

	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	conversation, err = service.syncConversationContextWindowTokens(stream, conversationID, conversation)
	if err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	conversation, err = service.persistDerivedPromptContexts(stream, conversationID, requestID, conversation, mode, latestUserText)
	if err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	compiled, err := service.compiler.Compile(conversation, mode, latestUserText, modelName)
	if err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	compiled = guardCompiledConversationForProvider(compiled)
	if compacted, compactErr := service.maybeCompactBeforeProvider(stream, conversation, compiled); compactErr != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", compactErr)
	} else if compacted {
		stream.mu.Lock()
		stream.ProviderActive = false
		stream.ProviderCancel = nil
		stream.UpdatedAt = time.Now().UTC()
		hasPendingCompaction := stream.PendingCompaction != nil
		status := stream.Status
		stream.mu.Unlock()
		switch {
		case isTerminalStreamStatus(status):
			switch status {
			case StreamStatusCompleted:
				service.setTurnPhase(stream, TurnPhaseCompleted)
			case StreamStatusCanceled:
				service.setTurnPhase(stream, TurnPhaseCanceled)
			default:
				service.setTurnPhase(stream, TurnPhaseFailed)
			}
		case hasPendingCompaction:
			service.setTurnPhase(stream, TurnPhaseCompacting)
		default:
			service.setTurnPhase(stream, TurnPhaseIdle)
		}
		return nil
	}
	if err := service.syncSummarySnapshot(stream, conversation, requestID, modelCallID); err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	maxTokens, requestKnobs := service.resolveProviderOutputBudget(modelID, conversation, compiled)
	service.maybeSaveLastAgentModelHash(conversation, modelID, mode, currentPass)
	ctx, cancel := context.WithCancel(context.Background())
	stream.mu.Lock()
	stream.ProviderActive = true
	stream.ProviderCancel = cancel
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	service.setTurnPhase(stream, TurnPhaseProviderRunning)

	providerRequest := ProviderRequest{
		RequestID:          requestID,
		ConversationID:     conversationID,
		RunID:              requestID,
		ModelCallID:        modelCallID,
		ModelID:            modelID,
		Mode:               compiled.Mode,
		ThinkingEffort:     compiled.Mode.String(),
		Messages:           compiled.Messages,
		StableMessageCount: compiled.StableMessageCount,
		Tools:              compiled.Tools,
		MaxTokens:          maxTokens,
		RequestKnobs:       requestKnobs,
		CompileSummary:     compiled.CompileSummary,
		Observer:           service.recorder,
		ArtifactPaths:      &modeladapter.LLMArtifactPaths{},
	}
	providerRequest.ThinkingEffort = thinkingEffort
	service.debug.LogProvider(context.Background(), requestID, conversationID, "provider_request_prepared", map[string]any{
		"model_call_id":          strings.TrimSpace(modelCallID),
		"provider_pass":          currentPass,
		"model_id":               strings.TrimSpace(modelID),
		"model_name":             strings.TrimSpace(modelName),
		"mode":                   compiled.Mode.String(),
		"thinking_effort":        strings.TrimSpace(thinkingEffort),
		"max_tokens":             maxTokens,
		"request_knobs":          requestKnobs,
		"message_count":          len(compiled.Messages),
		"tool_count":             len(compiled.Tools),
		"compile_summary_length": len(compiled.CompileSummary),
	})
	go service.runProviderStream(stream, currentToken, ctx, providerRequest)
	return nil
}

func (service *Service) resolveProviderOutputBudget(modelID string, conversation *ConversationFile, compiled CompiledConversation) (int, map[string]any) {
	configuredMaxTokens := service.resolveConfiguredProviderMaxOutputTokens(modelID)
	contextWindowTokens := compactionContextWindowSize(conversation)
	estimatedPromptTokens := estimateCompiledPromptTokens(compiled)
	if conversation != nil && int64(conversation.TokenDetailsUsedTokens) > estimatedPromptTokens {
		estimatedPromptTokens = int64(conversation.TokenDetailsUsedTokens)
	}
	remainingTokens := int64(0)
	requestMaxTokens := int64(configuredMaxTokens)
	if requestMaxTokens <= 0 {
		requestMaxTokens = providerDefaultMaxOutputTokens
	}
	if contextWindowTokens > 0 && estimatedPromptTokens > 0 {
		remainingTokens = contextWindowTokens - estimatedPromptTokens
		allowedTokens := remainingTokens - providerOutputSafetyTokens
		if allowedTokens < 1 {
			allowedTokens = 1
		}
		if allowedTokens < requestMaxTokens {
			requestMaxTokens = allowedTokens
		}
	}
	maxTokens := int(requestMaxTokens)
	if maxTokens <= 0 {
		maxTokens = 1
	}
	requestKnobs := map[string]any{
		"configured_max_tokens":             configuredMaxTokens,
		"dynamic_max_tokens":                maxTokens,
		"compiled_prompt_tokens_estimate":   estimatedPromptTokens,
		"context_window_tokens":             contextWindowTokens,
		"remaining_context_tokens_estimate": remainingTokens,
		"provider_output_safety_tokens":     providerOutputSafetyTokens,
	}
	return maxTokens, withPreviousCacheFrontierHint(requestKnobs, conversation)
}

func withPreviousCacheFrontierHint(requestKnobs map[string]any, conversation *ConversationFile) map[string]any {
	if len(requestKnobs) == 0 {
		requestKnobs = map[string]any{}
	}
	if conversation == nil || conversation.LatestRequestPrefix == nil {
		return requestKnobs
	}
	prefix := conversation.LatestRequestPrefix
	frontierHash := strings.TrimSpace(prefix.FrontierHash)
	if frontierHash == "" {
		return requestKnobs
	}
	requestKnobs["previous_cache_frontier_hash"] = frontierHash
	requestKnobs["previous_cache_frontier"] = map[string]any{
		"canonical_body_hash": prefix.CanonicalBodyHash,
		"frontier_hash":       frontierHash,
		"frontier_path":       prefix.FrontierPath,
		"breakpoint_count":    prefix.BreakpointCount,
		"request_id":          strings.TrimSpace(prefix.RequestID),
		"model_call_id":       strings.TrimSpace(prefix.ModelCallID),
	}
	return requestKnobs
}

func (service *Service) resolveConfiguredProviderMaxOutputTokens(modelID string) int {
	if service == nil || service.resolver == nil {
		return providerDefaultMaxOutputTokens
	}
	channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
	if err != nil || channel == nil {
		return providerDefaultMaxOutputTokens
	}
	maxTokens := configuredProviderMaxOutputTokens(channel.Provider, channel.MaxTokens, channel.AnthropicMaxTokens)
	if maxTokens <= 0 {
		return providerDefaultMaxOutputTokens
	}
	return maxTokens
}

func configuredProviderMaxOutputTokens(provider string, maxTokens int, anthropicMaxTokens int) int {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		if anthropicMaxTokens > 0 {
			return anthropicMaxTokens
		}
		if maxTokens > 0 {
			return maxTokens
		}
	case "openai":
		if maxTokens > 0 {
			return maxTokens
		}
		if anthropicMaxTokens > 0 {
			return anthropicMaxTokens
		}
	default:
		if maxTokens > 0 && anthropicMaxTokens > 0 {
			if anthropicMaxTokens > maxTokens {
				return anthropicMaxTokens
			}
			return maxTokens
		}
		if maxTokens > 0 {
			return maxTokens
		}
		if anthropicMaxTokens > 0 {
			return anthropicMaxTokens
		}
	}
	return providerDefaultMaxOutputTokens
}

func (service *Service) maybeSaveLastAgentModelHash(conversation *ConversationFile, modelID string, mode agentv1.AgentMode, providerPass int) {
	if service == nil || service.modelMemory == nil || service.resolver == nil {
		return
	}
	if providerPass != 1 || !isSupportedActiveMode(mode) {
		return
	}
	if conversation != nil && strings.TrimSpace(conversation.SubagentTypeName) != "" {
		return
	}
	channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
	if err != nil || channel == nil || strings.TrimSpace(channel.ID) == "" {
		if err != nil {
			log.Printf("forwarder skipped last agent model hash update model_id=%s error=%v", strings.TrimSpace(modelID), err)
		}
		return
	}
	if err := service.modelMemory.SaveLastAgentModelHash(context.Background(), strings.TrimSpace(channel.ID)); err != nil {
		log.Printf("forwarder failed to save last agent model hash channel_id=%s error=%v", strings.TrimSpace(channel.ID), err)
	}
}

func (service *Service) persistDerivedPromptContexts(stream *ActiveStream, conversationID string, requestID string, conversation *ConversationFile, mode agentv1.AgentMode, latestUserText string) (*ConversationFile, error) {
	if stream == nil {
		return nil, fmt.Errorf("active stream is required")
	}
	if service == nil || service.compiler == nil {
		return conversation, nil
	}
	contexts, err := service.compiler.DerivePromptContexts(conversation, mode, latestUserText)
	if err != nil {
		return nil, err
	}
	if len(contexts) == 0 {
		return conversation, nil
	}
	stream.mu.Lock()
	turnSeq := stream.TurnSeq
	stream.mu.Unlock()
	if turnSeq <= 0 {
		return conversation, nil
	}
	entries := make([]HistoryEntry, 0, len(contexts))
	for _, context := range contexts {
		context = normalizePromptContextMessage(context)
		if !isReplayablePromptContext(context) {
			continue
		}
		entries = append(entries, newPromptContextEntry(turnSeq, requestID, context))
	}
	if len(entries) == 0 {
		return conversation, nil
	}
	if _, err := service.appendConversationEntries(stream, conversationID, entries); err != nil {
		return nil, err
	}
	conversation, _, _, err = service.snapshotCheckpointConversation(stream)
	return conversation, err
}

func (service *Service) runProviderStream(stream *ActiveStream, token uint64, ctx context.Context, request ProviderRequest) {
	err := service.provider.StartStream(ctx, request, func(event modeladapter.ModelEvent) error {
		return service.postStreamCommandWait(stream, streamCommand{
			Kind: streamCommandProviderEvent,
			Provider: &streamProviderEvent{
				Token: token,
				Event: event,
			},
		})
	})
	if postErr := service.postStreamCommandWait(stream, streamCommand{
		Kind: streamCommandProviderEvent,
		Provider: &streamProviderEvent{
			Token: token,
			Done:  true,
			Err:   err,
		},
	}); postErr != nil && !errors.Is(postErr, errProviderLoopInterrupted) {
		service.debug.LogProvider(context.Background(), request.RequestID, request.ConversationID, "provider_completion_post_error", map[string]any{
			"model_call_id":  strings.TrimSpace(request.ModelCallID),
			"provider_token": token,
			"error":          postErr.Error(),
		})
		log.Printf(
			"forwarder provider completion post failed request_id=%s model_call_id=%s provider_token=%d err=%v",
			strings.TrimSpace(request.RequestID),
			strings.TrimSpace(request.ModelCallID),
			token,
			postErr,
		)
		_ = service.failStreamIfNonTerminal(stream, "unknown", postErr)
	}
	if err != nil {
		service.debug.LogProvider(context.Background(), request.RequestID, request.ConversationID, "provider_stream_finished", map[string]any{
			"model_call_id":  strings.TrimSpace(request.ModelCallID),
			"provider_token": token,
			"error":          err.Error(),
		})
		return
	}
	service.debug.LogProvider(context.Background(), request.RequestID, request.ConversationID, "provider_stream_finished", map[string]any{
		"model_call_id":  strings.TrimSpace(request.ModelCallID),
		"provider_token": token,
	})
}


// extractUserMessage 从 legacy run_request 中提取用户消息。
func extractUserMessage(message *agentv1.AgentClientMessage) *agentv1.UserMessage {
	if message == nil || message.GetRunRequest() == nil || message.GetRunRequest().GetAction() == nil {
		return nil
	}
	switch item := message.GetRunRequest().GetAction().GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return item.UserMessageAction.GetUserMessage()
	case *agentv1.ConversationAction_StartPlanAction:
		return item.StartPlanAction.GetUserMessage()
	default:
		return nil
	}
}

// extractRequestContext 从 legacy 请求中提取 request_context。
func extractRequestContext(message *agentv1.AgentClientMessage) *agentv1.RequestContext {
	if message == nil || message.GetRunRequest() == nil || message.GetRunRequest().GetAction() == nil {
		return nil
	}
	switch item := message.GetRunRequest().GetAction().GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return item.UserMessageAction.GetRequestContext()
	case *agentv1.ConversationAction_ResumeAction:
		return item.ResumeAction.GetRequestContext()
	case *agentv1.ConversationAction_StartPlanAction:
		return item.StartPlanAction.GetRequestContext()
	case *agentv1.ConversationAction_ExecutePlanAction:
		return item.ExecutePlanAction.GetRequestContext()
	default:
		return nil
	}
}

func (service *Service) shouldIgnoreEmptyResumeRunRequest(requestID string, runRequest *agentv1.AgentRunRequest, userMessage *agentv1.UserMessage, requestContext *agentv1.RequestContext) bool {
	if runRequest == nil || !conversationActionIsResume(runRequest.GetAction()) {
		return false
	}
	if userMessage != nil || requestContextHasPayload(requestContext) {
		return false
	}
	state := runRequest.GetConversationState()
	if state != nil && len(state.GetPendingToolCalls()) > 0 {
		return false
	}
	conversationID := strings.TrimSpace(runRequest.GetConversationId())
	if conversationID == "" || service.hasActiveConversationStream(conversationID, requestID) {
		return false
	}
	conversation, err := service.loadConversationForResumeGuard(conversationID)
	if err != nil || conversation == nil {
		return false
	}
	return emptyResumeCanBeIgnoredForConversation(conversation)
}

func requestContextHasPayload(requestContext *agentv1.RequestContext) bool {
	return requestContext != nil && proto.Size(requestContext) > 0
}

func (service *Service) loadConversationForResumeGuard(conversationID string) (*ConversationFile, error) {
	if service == nil || service.store == nil {
		return nil, nil
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, nil
	}
	return service.store.LoadConversation(conversationID)
}

func (service *Service) hasActiveConversationStream(conversationID string, requestID string) bool {
	conversationID = strings.TrimSpace(conversationID)
	if service == nil || service.broker == nil || conversationID == "" {
		return false
	}
	if len(service.broker.OtherConversationRequestIDs(conversationID, requestID)) > 0 {
		return true
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if strings.TrimSpace(stream.ConversationID) != conversationID {
		return false
	}
	if isTerminalStreamStatus(stream.Status) {
		return false
	}
	switch stream.Phase {
	case TurnPhaseCanceled, TurnPhaseCompleted, TurnPhaseFailed:
		return false
	default:
		return true
	}
}

func emptyResumeCanBeIgnoredForConversation(conversation *ConversationFile) bool {
	if conversation == nil {
		return false
	}
	status := strings.TrimSpace(conversation.CurrentLoopStatus)
	currentRequestID := strings.TrimSpace(conversation.CurrentRequestID)
	if status == "" {
		return currentRequestID == ""
	}
	switch status {
	case "completed", "idle":
		return true
	default:
		return false
	}
}

func extractConversationActionUserMessage(action *agentv1.ConversationAction) *agentv1.UserMessage {
	if action == nil {
		return nil
	}
	switch item := action.GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return item.UserMessageAction.GetUserMessage()
	case *agentv1.ConversationAction_StartPlanAction:
		return item.StartPlanAction.GetUserMessage()
	default:
		return nil
	}
}

func extractConversationActionRequestContext(action *agentv1.ConversationAction) *agentv1.RequestContext {
	if action == nil {
		return nil
	}
	switch item := action.GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return item.UserMessageAction.GetRequestContext()
	case *agentv1.ConversationAction_ResumeAction:
		return item.ResumeAction.GetRequestContext()
	case *agentv1.ConversationAction_StartPlanAction:
		return item.StartPlanAction.GetRequestContext()
	case *agentv1.ConversationAction_ExecutePlanAction:
		return item.ExecutePlanAction.GetRequestContext()
	default:
		return nil
	}
}

func conversationActionIsResume(action *agentv1.ConversationAction) bool {
	if action == nil {
		return false
	}
	_, ok := action.GetAction().(*agentv1.ConversationAction_ResumeAction)
	return ok
}

func conversationActionStartsRun(action *agentv1.ConversationAction) bool {
	if action == nil {
		return false
	}
	switch action.GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction,
		*agentv1.ConversationAction_ResumeAction,
		*agentv1.ConversationAction_StartPlanAction,
		*agentv1.ConversationAction_ExecutePlanAction:
		return true
	default:
		return false
	}
}

// extractRunMode 推导本轮应使用的 mode。
func extractRunMode(message *agentv1.AgentClientMessage) (agentv1.AgentMode, ModeSource, bool, error) {
	if userMessage := extractUserMessage(message); userMessage != nil && userMessage.GetMode() != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
		return resolveExplicitMode(userMessage.GetMode(), ModeSourceUserMessage)
	}
	if message != nil && message.GetRunRequest() != nil && message.GetRunRequest().GetAction() != nil {
		if item, ok := message.GetRunRequest().GetAction().GetAction().(*agentv1.ConversationAction_ExecutePlanAction); ok && item.ExecutePlanAction != nil {
			if mode := item.ExecutePlanAction.GetExecutionMode(); mode != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
				return resolveExplicitMode(mode, ModeSourceExecutePlanAction)
			}
		}
	}
	if message != nil && message.GetRunRequest() != nil && message.GetRunRequest().GetConversationState() != nil {
		if mode := message.GetRunRequest().GetConversationState().GetMode(); mode != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
			return resolveExplicitMode(mode, ModeSourceConversationState)
		}
	}
	return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
}

func extractPrewarmMode(request *agentv1.PrewarmRequest) (agentv1.AgentMode, ModeSource, bool, error) {
	if request == nil || request.GetConversationState() == nil {
		return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
	}
	mode := request.GetConversationState().GetMode()
	if mode == agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
		return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
	}
	return resolveExplicitMode(mode, ModeSourceConversationState)
}

func extractConversationActionMode(action *agentv1.ConversationAction) (agentv1.AgentMode, ModeSource, bool, error) {
	if userMessage := extractConversationActionUserMessage(action); userMessage != nil && userMessage.GetMode() != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
		return resolveExplicitMode(userMessage.GetMode(), ModeSourceUserMessage)
	}
	if action == nil {
		return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
	}
	switch item := action.GetAction().(type) {
	case *agentv1.ConversationAction_ExecutePlanAction:
		if item.ExecutePlanAction != nil && item.ExecutePlanAction.GetExecutionMode() != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
			return resolveExplicitMode(item.ExecutePlanAction.GetExecutionMode(), ModeSourceExecutePlanAction)
		}
	}
	return agentv1.AgentMode_AGENT_MODE_AGENT, ModeSourceUnknown, false, nil
}

// extractRequestedModelID 提取本轮显式请求的模型 ID。
func extractRequestedModelID(message *agentv1.AgentClientMessage) string {
	if message == nil {
		return ""
	}
	if runRequest := message.GetRunRequest(); runRequest != nil {
		return firstNonEmpty(extractRequestedModelIDFromRequestedModel(runRequest.GetRequestedModel()), runRequest.GetModelDetails().GetModelId())
	}
	if prewarm := message.GetPrewarmRequest(); prewarm != nil {
		return firstNonEmpty(extractRequestedModelIDFromRequestedModel(prewarm.GetRequestedModel()), prewarm.GetModelDetails().GetModelId())
	}
	return ""
}

func extractRequestedModelIDFromRequestedModel(model *agentv1.RequestedModel) string {
	if model == nil {
		return ""
	}
	if model.GetIsVariantStringRepresentation() {
		modelID, _ := splitRuntimeThinkingEffortVariantString(model.GetModelId())
		return modelID
	}
	return strings.TrimSpace(model.GetModelId())
}

func extractRuntimeThinkingEffort(message *agentv1.AgentClientMessage) string {
	if message == nil {
		return ""
	}
	if runRequest := message.GetRunRequest(); runRequest != nil {
		return extractRuntimeThinkingEffortFromRequestedModel(runRequest.GetRequestedModel())
	}
	if prewarm := message.GetPrewarmRequest(); prewarm != nil {
		return extractRuntimeThinkingEffortFromRequestedModel(prewarm.GetRequestedModel())
	}
	return ""
}

func extractRuntimeThinkingEffortFromRequestedModel(model *agentv1.RequestedModel) string {
	if model == nil {
		return ""
	}
	for _, parameter := range model.GetParameters() {
		if parameter == nil || !isRuntimeThinkingEffortParameterID(parameter.GetId()) {
			continue
		}
		if effort := modeladapter.NormalizeRuntimeThinkingEffort(parameter.GetValue()); effort != "" {
			return effort
		}
	}
	if model.GetIsVariantStringRepresentation() {
		if _, effort := splitRuntimeThinkingEffortVariantString(model.GetModelId()); effort != "" {
			return effort
		}
		return modeladapter.NormalizeRuntimeThinkingEffort(model.GetModelId())
	}
	return ""
}

func isRuntimeThinkingEffortParameterID(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case runtimeThinkingEffortParameterID,
		"reasoning",
		"reasoning_effort",
		"thinking_intensity",
		"anthropic_thinking_effort",
		"openai_reasoning_effort":
		return true
	default:
		return false
	}
}

func splitRuntimeThinkingEffortVariantString(raw string) (string, string) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", ""
	}
	if effort := modeladapter.NormalizeRuntimeThinkingEffort(text); effort != "" {
		return "", effort
	}
	index := strings.LastIndex(text, ":")
	if index <= 0 || index >= len(text)-1 {
		return "", ""
	}
	modelID := strings.TrimSpace(text[:index])
	effort := modeladapter.NormalizeRuntimeThinkingEffort(text[index+1:])
	if modelID == "" || effort == "" {
		return "", ""
	}
	return modelID, effort
}


// userMessageText 返回用户消息中的纯文本。
func userMessageText(message *agentv1.UserMessage) string {
	if message == nil {
		return ""
	}
	return strings.TrimSpace(message.GetText())
}

func currentProviderPass(stream *ActiveStream) int {
	if stream == nil {
		return 0
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.ProviderPassCount
}

func currentStreamMode(stream *ActiveStream) agentv1.AgentMode {
	if stream == nil {
		return agentv1.AgentMode_AGENT_MODE_AGENT
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if normalized, err := validateSupportedActiveMode(stream.Mode); err == nil {
		return normalized
	}
	return stream.Mode
}

// selectPendingExec 按 exec_id 或 message_id 在当前流里查找挂起执行桥。
func selectPendingExec(execID string, messageID uint32, stream *ActiveStream) (runtimecore.PendingExec, bool) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if item, ok := stream.PendingExecs[strings.TrimSpace(execID)]; ok {
		return item, true
	}
	if messageID != 0 {
		for _, item := range stream.PendingExecs {
			if item.MessageID == messageID {
				return item, true
			}
		}
	}
	return runtimecore.PendingExec{}, false
}

func selectPendingInteraction(message *agentv1.InteractionResponse, stream *ActiveStream) (runtimecore.PendingInteraction, bool) {
	if stream == nil || message == nil {
		return runtimecore.PendingInteraction{}, false
	}
	interactionID := fmt.Sprintf("%d", message.GetId())
	stream.mu.Lock()
	defer stream.mu.Unlock()
	item, ok := stream.PendingInteractions[interactionID]
	return item, ok
}

// selectPendingExecByControl 根据控制消息的桥消息 ID 查找挂起执行桥。
func selectPendingExecByControl(message *agentv1.ExecClientControlMessage, stream *ActiveStream) (runtimecore.PendingExec, bool) {
	messageID, ok := execControlMessageID(message)
	if !ok {
		return runtimecore.PendingExec{}, false
	}
	return selectPendingExec("", messageID, stream)
}

func execControlMessageID(message *agentv1.ExecClientControlMessage) (uint32, bool) {
	if message == nil {
		return 0, false
	}
	switch item := message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_StreamClose:
		return item.StreamClose.GetId(), true
	case *agentv1.ExecClientControlMessage_Throw:
		return item.Throw.GetId(), true
	case *agentv1.ExecClientControlMessage_Heartbeat:
		return item.Heartbeat.GetId(), true
	default:
		return 0, false
	}
}

func shouldIgnoreMissingExecResult(message *agentv1.ExecClientMessage, stream *ActiveStream) bool {
	if message == nil {
		return false
	}
	return recentlyCompletedExecExists(stream, message.GetId())
}

func shouldIgnoreMissingExecControl(message *agentv1.ExecClientControlMessage, stream *ActiveStream, requestID string) bool {
	if message == nil {
		return false
	}
	// 审计 L7：Heartbeat/StreamClose 在"stream 存在但 pending exec 找不到"时，
	// 原实现经 shouldIgnoreStaleExecControl 一律静默吞。重连客户端的迟到控制消息
	// 确实是传输级噪声（合理忽略），但若 pending exec 被错误清除也表现为 missing，
	// 无条件吞会掩盖真实协议错误。这里区分两种情况：
	//   - recentlyCompletedExecExists：该 exec 最近刚完成（已被处理），忽略合理；
	//   - 从未存在：可能是协议错误，仍忽略（绝不杀流——返回 error 会经 failStream
	//     误杀整个流，比静默吞更糟），但记 WARN 让真实协议异常可被诊断。
	if isStaleTransportExecControl(message) {
		messageID, ok := execControlMessageID(message)
		if ok && recentlyCompletedExecExists(stream, messageID) {
			return true
		}
		log.Printf(
			"forwarder stale exec control has no pending exec request_id=%s message_id=%d (never existed; ignored, may indicate protocol drift)",
			strings.TrimSpace(requestID),
			messageID,
		)
		return true
	}
	messageID, ok := execControlMessageID(message)
	if !ok {
		return false
	}
	return recentlyCompletedExecExists(stream, messageID)
}

// isStaleTransportExecControl 判定是否为重连客户端可能迟发的传输级控制消息
// （Heartbeat / StreamClose）。这类消息即便对应 pending exec 已不存在也不应杀流。
func isStaleTransportExecControl(message *agentv1.ExecClientControlMessage) bool {
	if message == nil {
		return false
	}
	switch message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_Heartbeat, *agentv1.ExecClientControlMessage_StreamClose:
		return true
	default:
		return false
	}
}

func shouldIgnoreStaleExecControl(message *agentv1.ExecClientControlMessage) bool {
	if message == nil {
		return false
	}
	// 此处 stream 已不 active（request 结束/崩溃/换代会话）：重连客户端迟发的
	// Heartbeat/StreamClose 是纯传输级噪声，静默吞合理，也无 RecentCompletedExecs 可查。
	return isStaleTransportExecControl(message)
}

type pendingAssistantMessage struct {
	ID      string                    `json:"id,omitempty"`
	Role    string                    `json:"role,omitempty"`
	Content []pendingAssistantContent `json:"content,omitempty"`
}

type pendingAssistantContent struct {
	Type       string          `json:"type,omitempty"`
	Text       string          `json:"text,omitempty"`
	Signature  string          `json:"signature,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
}

type pendingToolCallReplay struct {
	OpenedAt time.Time
	SortKey  string
	Raw      string
}

func buildPendingToolCalls(pendingExecs []runtimecore.PendingExec, pendingInteractions []runtimecore.PendingInteraction) []string {
	if len(pendingExecs) == 0 && len(pendingInteractions) == 0 {
		return nil
	}

	items := make([]pendingToolCallReplay, 0, len(pendingExecs)+len(pendingInteractions))
	for _, pending := range pendingExecs {
		raw, ok := encodePendingExecAsAssistantOutput(pending)
		if !ok {
			continue
		}
		items = append(items, pendingToolCallReplay{
			OpenedAt: pending.OpenedAt,
			SortKey:  fmt.Sprintf("exec-%020d", pending.MessageID),
			Raw:      raw,
		})
	}
	for _, pending := range pendingInteractions {
		raw, ok := encodePendingInteractionAsAssistantOutput(pending)
		if !ok {
			continue
		}
		items = append(items, pendingToolCallReplay{
			OpenedAt: pending.OpenedAt,
			SortKey:  "interaction-" + strings.TrimSpace(pending.InteractionID),
			Raw:      raw,
		})
	}
	if len(items) == 0 {
		return nil
	}

	sort.SliceStable(items, func(i, j int) bool {
		left := items[i]
		right := items[j]
		switch {
		case left.OpenedAt.Equal(right.OpenedAt):
			return left.SortKey < right.SortKey
		case left.OpenedAt.IsZero():
			return false
		case right.OpenedAt.IsZero():
			return true
		default:
			return left.OpenedAt.Before(right.OpenedAt)
		}
	})

	encoded := make([]string, 0, len(items))
	for _, item := range items {
		encoded = append(encoded, item.Raw)
	}
	return encoded
}

func encodePendingExecAsAssistantOutput(pending runtimecore.PendingExec) (string, bool) {
	toolCallID := strings.TrimSpace(pending.ToolCallID)
	toolName, argsJSON, ok := pendingAssistantToolShape(pending)
	if toolCallID == "" || !ok || strings.TrimSpace(toolName) == "" {
		return "", false
	}

	payload, err := json.Marshal(pendingAssistantMessage{
		ID:      "1",
		Role:    "assistant",
		Content: buildPendingAssistantContents(pending.ReasoningContent, pending.ReasoningSignature, toolCallID, toolName, argsJSON),
	})
	if err != nil {
		return "", false
	}
	return string(payload), true
}

func encodePendingInteractionAsAssistantOutput(pending runtimecore.PendingInteraction) (string, bool) {
	toolCallID := strings.TrimSpace(pending.ToolCallID)
	toolName := strings.TrimSpace(deriveToolNameFromPendingInteraction(pending))
	if toolCallID == "" || toolName == "" {
		return "", false
	}
	payload, err := json.Marshal(pendingAssistantMessage{
		ID:      "1",
		Role:    "assistant",
		Content: buildPendingAssistantContents(pending.ReasoningContent, pending.ReasoningSignature, toolCallID, toolName, pending.ArgsJSON),
	})
	if err != nil {
		return "", false
	}
	return string(payload), true
}

func buildPendingAssistantContents(reasoningContent string, reasoningSignature string, toolCallID string, toolName string, argsJSON []byte) []pendingAssistantContent {
	items := make([]pendingAssistantContent, 0, 2)
	if strings.TrimSpace(reasoningContent) != "" {
		items = append(items, pendingAssistantContent{
			Type:      "reasoning",
			Text:      reasoningContent,
			Signature: strings.TrimSpace(reasoningSignature),
		})
	}
	items = append(items, pendingAssistantContent{
		Type:       "tool-call",
		ToolCallID: toolCallID,
		ToolName:   strings.TrimSpace(toolName),
		Args:       append(json.RawMessage(nil), argsJSON...),
	})
	return items
}

func pendingAssistantToolShape(pending runtimecore.PendingExec) (string, []byte, bool) {
	switch strings.TrimSpace(pending.ExecKind) {
	case patchEditReadExecKindName, patchEditWriteExecKindName, patchEditPostReadExecKindName:
		payload, err := decodePendingPatchEditPayload(pending.ArgsJSON)
		if err != nil {
			return "", nil, false
		}
		argsJSON, err := patchEditPayloadArgsJSON(payload)
		if err != nil {
			return "", nil, false
		}
		return firstNonEmpty(strings.TrimSpace(payload.ToolName), patchEditToolName), argsJSON, true
	case writeReadExecKind, writeWriteExecKind, writePostReadExecKind:
		payload, err := decodePendingWritePayload(pending.ArgsJSON)
		if err != nil {
			return "", nil, false
		}
		argsJSON, err := payload.VisibleArgs.MarshalJSON()
		if err != nil {
			return "", nil, false
		}
		return "Write", argsJSON, true
	default:
		toolName := strings.TrimSpace(deriveToolNameFromPendingExec(pending))
		if toolName == "" {
			return "", nil, false
		}
		return toolName, append([]byte(nil), pending.ArgsJSON...), true
	}
}

// markExecCompleted 保留一个短时 tombstone，避免迟到的 transport-level control 被误判为协议错误。
func markExecCompleted(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil {
		return
	}
	now := time.Now().UTC()
	cutoff := now.Add(-completedExecRetention)

	stream.mu.Lock()
	delete(stream.PendingExecs, pending.ExecID)
	if pending.MessageID != 0 {
		if stream.RecentCompletedExecs == nil {
			stream.RecentCompletedExecs = make(map[uint32]time.Time)
		}
		for messageID, completedAt := range stream.RecentCompletedExecs {
			if completedAt.Before(cutoff) {
				delete(stream.RecentCompletedExecs, messageID)
			}
		}
		stream.RecentCompletedExecs[pending.MessageID] = now
	}
	stream.UpdatedAt = now
	stream.mu.Unlock()
}

func recentlyCompletedExecExists(stream *ActiveStream, messageID uint32) bool {
	if stream == nil || messageID == 0 {
		return false
	}
	now := time.Now().UTC()
	cutoff := now.Add(-completedExecRetention)

	stream.mu.Lock()
	defer stream.mu.Unlock()
	if len(stream.RecentCompletedExecs) == 0 {
		return false
	}
	completedAt, ok := stream.RecentCompletedExecs[messageID]
	for id, ts := range stream.RecentCompletedExecs {
		if ts.Before(cutoff) {
			delete(stream.RecentCompletedExecs, id)
		}
	}
	if !ok {
		return false
	}
	if completedAt.Before(cutoff) {
		delete(stream.RecentCompletedExecs, messageID)
		return false
	}
	return true
}


func lookupMCPToolServer(stream *ActiveStream, toolName string) string {
	trimmedToolName := strings.TrimSpace(toolName)
	if trimmedToolName == "" {
		return ""
	}
	if stream != nil {
		stream.mu.Lock()
		serverIdentifier := strings.TrimSpace(stream.MCPToolServers[trimmedToolName])
		stream.mu.Unlock()
		if serverIdentifier != "" {
			return serverIdentifier
		}
	}
	return ""
}

func readStringAny(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func readMapAny(value any) map[string]any {
	switch item := value.(type) {
	case map[string]any:
		return item
	case nil:
		return map[string]any{}
	default:
		return map[string]any{}
	}
}

// inferToolName 从完整 ToolCall proto 中反推出 canonical 工具名。
func inferToolName(toolCall *agentv1.ToolCall) string {
	if toolCall == nil || toolCall.GetTool() == nil {
		return ""
	}
	switch toolCall.GetTool().(type) {
	case *agentv1.ToolCall_ReadToolCall:
		return "Read"
	case *agentv1.ToolCall_UpdateTodosToolCall:
		return "TodoWrite"
	case *agentv1.ToolCall_ReadTodosToolCall:
		return "ReadTodos"
	case *agentv1.ToolCall_DeleteToolCall:
		return "Delete"
	case *agentv1.ToolCall_GrepToolCall:
		return "Grep"
	case *agentv1.ToolCall_GlobToolCall:
		return "Glob"
	case *agentv1.ToolCall_ShellToolCall:
		return "Shell"
	case *agentv1.ToolCall_AwaitToolCall:
		return "AwaitShell"
	case *agentv1.ToolCall_WriteShellStdinToolCall:
		return "WriteShellStdin"
	case *agentv1.ToolCall_EditToolCall:
		return inferEditToolNameFromToolCall(toolCall.GetEditToolCall())
	case *agentv1.ToolCall_LsToolCall:
		return "Ls"
	case *agentv1.ToolCall_McpToolCall:
		return "CallMcpTool"
	case *agentv1.ToolCall_ListMcpResourcesToolCall:
		return "ListMcpResources"
	case *agentv1.ToolCall_ReadMcpResourceToolCall:
		return "FetchMcpResource"
	case *agentv1.ToolCall_CreatePlanToolCall:
		return "CreatePlan"
	case *agentv1.ToolCall_AskQuestionToolCall:
		return "AskQuestion"
	case *agentv1.ToolCall_WebSearchToolCall:
		return "WebSearch"
	case *agentv1.ToolCall_WebFetchToolCall:
		return "WebFetch"
	case *agentv1.ToolCall_SwitchModeToolCall:
		return "SwitchMode"
	case *agentv1.ToolCall_GenerateImageToolCall:
		return "GenerateImage"
	case *agentv1.ToolCall_TaskToolCall:
		return "Task"
	default:
		return ""
	}
}

// deriveToolNameFromPendingExec 根据执行桥种类反推出 canonical 工具名。
func deriveToolNameFromPendingExec(pending runtimecore.PendingExec) string {
	switch strings.TrimSpace(pending.ExecKind) {
	case "read":
		return "Read"
	case "write":
		return "Write"
	case "delete":
		return "Delete"
	case "glob":
		return "Glob"
	case "grep":
		return "Grep"
	case "diagnostics":
		return "ReadLints"
	case "ls":
		return "Ls"
	case "mcp":
		return "CallMcpTool"
	case "list_mcp_resources":
		return "ListMcpResources"
	case "read_mcp_resource":
		return "FetchMcpResource"
	case "shell":
		return "Shell"
	case "await_shell":
		return "AwaitShell"
	case "write_shell_stdin":
		return "WriteShellStdin"
	case "force_background_shell":
		return "ForceBackgroundShell"
	case "subagent":
		return "Task"
	default:
		return ""
	}
}

func execKindFromToolName(name string) (string, bool) {
	switch strings.TrimSpace(name) {
	case "Read":
		return "read", true
	case "Write":
		return "write", true
	case "PatchEdit":
		return "patch_edit", true
	case "Delete":
		return "delete", true
	case "Glob":
		return "glob", true
	case "Grep":
		return "grep", true
	case "Ls":
		return "ls", true
	case "ReadLints":
		return "diagnostics", true
	case "CallMcpTool":
		return "mcp", true
	case "FetchMcpResource":
		return "read_mcp_resource", true
	case "Shell":
		return "shell", true
	case "AwaitShell":
		return "await_shell", true
	case "WriteShellStdin":
		return "write_shell_stdin", true
	case "ForceBackgroundShell":
		return "force_background_shell", true
	case "Task":
		return "subagent", true
	default:
		return "", false
	}
}

func isExecTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "Read", "Write", "PatchEdit", "Delete", "Shell", "WriteShellStdin", "ForceBackgroundShell", "Grep", "Glob", "Ls", "ReadLints", "CallMcpTool", "FetchMcpResource", "Task":
		return true
	default:
		return false
	}
}

func inferEditToolName(args *agentv1.EditArgs) string {
	if args != nil && args.StreamContent != nil {
		return "Write"
	}
	return "Edit"
}

func inferEditToolNameFromToolCall(toolCall *agentv1.EditToolCall) string {
	if toolCall == nil {
		return ""
	}
	if editResultLooksLikeStructuredEdit(toolCall.GetResult()) {
		return "Edit"
	}
	return inferEditToolName(toolCall.GetArgs())
}

func editResultLooksLikeStructuredEdit(result *agentv1.EditResult) bool {
	success := result.GetSuccess()
	if success == nil {
		return false
	}
	return success.BeforeFullFileContent != nil || success.DiffString != nil
}

// buildTerminalStreamError 把 broker 终态事件转换成 Connect endstream 错误。
func buildTerminalStreamError(event StreamEvent) error {
	if !event.End {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(event.TerminalErrorCode)) {
	case "":
		return nil
	case "canceled":
		return connect.NewError(connect.CodeCanceled, errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	case "invalid_argument":
		return connect.NewError(connect.CodeInvalidArgument, errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	case "failed_precondition":
		return connect.NewError(connect.CodeFailedPrecondition, errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	case compactionOverflowTerminalCode:
		return buildRunSSECustomError(connect.CodeInvalidArgument, "Context Too Large After Compaction", errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	case "provider_error":
		return buildRunSSEProviderError(errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	default:
		return connect.NewError(connect.CodeUnknown, errors.New(strings.TrimSpace(event.TerminalErrorMessage)))
	}
}

// buildRunSSEProviderError 构造 provider 专用的 RunSSE 错误包。
func buildRunSSEProviderError(cause error) error {
	return buildRunSSEStructuredErrorWithDetail(
		connect.CodeUnavailable,
		"Server Error",
		"",
		cause,
		aiserverv1.ErrorDetails_ERROR_PROVIDER_ERROR,
		false,
	)
}

// buildRunSSECustomError 构造带有 CustomErrorDetails 的 RunSSE 结构化错误。
func buildRunSSECustomError(code connect.Code, title string, cause error) error {
	return buildRunSSEStructuredErrorWithDetail(code, title, "", cause, aiserverv1.ErrorDetails_ERROR_CUSTOM_MESSAGE, false)
}

// buildRunSSEStructuredError 统一构造带有 ErrorDetails 的 Connect endstream 错误。
func buildRunSSEStructuredErrorWithDetail(code connect.Code, title string, detailText string, cause error, errorKind aiserverv1.ErrorDetails_Error, expected bool) error {
	if cause == nil {
		cause = fmt.Errorf("unknown RunSSE error")
	}
	trimmedDetail := strings.TrimSpace(detailText)
	if trimmedDetail == "" {
		trimmedDetail = cause.Error()
	}
	isRetryable := true
	allowUnsafeCommandLinks := true
	showRequestID := true
	shouldShowImmediateError := false
	isExpected := expected
	payload := &aiserverv1.ErrorDetails{
		Error: errorKind,
		Details: &aiserverv1.CustomErrorDetails{
			Title:       strings.TrimSpace(title),
			Detail:      trimmedDetail,
			IsRetryable: &isRetryable,
			AllowCommandLinksPotentiallyUnsafePleaseOnlyUseForHandwrittenTrustedMarkdown: &allowUnsafeCommandLinks,
			ShowRequestId:            &showRequestID,
			ShouldShowImmediateError: &shouldShowImmediateError,
		},
		IsExpected: &isExpected,
	}
	result := connect.NewError(code, cause)
	detail, detailErr := connect.NewErrorDetail(payload)
	if detailErr == nil {
		result.AddDetail(detail)
	}
	return result
}
