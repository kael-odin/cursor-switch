package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	legacyruntime "cursor/internal/runtime"
)

func (service *Service) handleInteractionToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	if service == nil || stream == nil {
		return fmt.Errorf("active stream is required")
	}
	serverMessage, pendingInteraction, err := service.interactionBridge.OpenQuery(invocation)
	if err != nil {
		return newRecoverableToolInvocationError(err)
	}
	pendingInteraction.ModelCallID = invocation.ModelCallID
	pendingInteraction.ReasoningContent = invocation.ReasoningContent
	pendingInteraction.ReasoningSignature = invocation.ReasoningSignature
	pendingInteraction.ReasoningSignatureSource = invocation.ReasoningSignatureSource
	if pendingInteraction.OpenedAt.IsZero() {
		pendingInteraction.OpenedAt = time.Now().UTC()
	}

	stream.mu.Lock()
	if stream.PendingInteractions == nil {
		stream.PendingInteractions = make(map[string]runtimecore.PendingInteraction)
	}
	pendingInteraction.ProviderPass = stream.ProviderPassCount
	stream.PendingInteractions[pendingInteraction.InteractionID] = pendingInteraction
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	removePending := func() {
		stream.mu.Lock()
		delete(stream.PendingInteractions, pendingInteraction.InteractionID)
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		removePending()
		return err
	}
	if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage}); err != nil {
		removePending()
		return err
	}
	return nil
}

func (service *Service) handleInteractionResult(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", intent.RequestID)
	}
	if intent.InteractionResponse == nil {
		return fmt.Errorf("interaction response is required")
	}
	pending, found := selectPendingInteraction(intent.InteractionResponse, stream)
	if !found {
		return fmt.Errorf("pending interaction not found")
	}
	result, err := service.interactionBridge.ApplyInteractionResponse(intent.InteractionResponse, pending)
	if err != nil {
		return err
	}
	markInteractionCompleted(stream, pending)
	toolName := strings.TrimSpace(deriveToolNameFromPendingInteraction(pending))
	if result.ToolCall != nil {
		applySwitchModeMetadata(stream, result.ToolCall)
		if err := service.appendToolResult(stream, result.ToolCallID, toolName, pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, result.ToolCall); err != nil {
			return err
		}
	} else if strings.TrimSpace(result.ToolResultPayload) != "" {
		if err := service.appendToolResult(stream, pending.ToolCallID, toolName, pending.ArgsJSON, result.ToolResultPayload, pending.ReasoningContent, nil); err != nil {
			return err
		}
	}
	if err := service.publishToolCallCompleted(intent.RequestID, result.ToolCallID, pending.ModelCallID, result.ToolCall); err != nil {
		return err
	}
	if err := service.applyApprovedSwitchMode(stream, stream.ConversationID, result.ToolCall); err != nil {
		return err
	}
	if switchToolCall := result.ToolCall.GetSwitchModeToolCall(); switchToolCall != nil && switchToolCall.GetResult().GetSuccess() != nil {
		targetMode, err := switchModeTarget(switchToolCall.GetArgs())
		if err != nil {
			return err
		}
		modeEntry, err := newModeMetadataEntry(stream.TurnSeq, intent.RequestID, targetMode, true, ModeSourceSwitchModeTool)
		if err != nil {
			return err
		}
		if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			modeEntry,
			newModeChangePromptContextEntry(stream.TurnSeq, intent.RequestID, targetMode),
		}); err != nil {
			return err
		}
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, intent.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishCheckpoint(intent.RequestID, stream.ConversationID); err != nil {
		return err
	}
	if !shouldAutoResumeAfterInteraction(pending) {
		rememberPendingProviderCompletion(stream, pendingTurnCompletion{
			ConversationID: stream.ConversationID,
			RequestID:      stream.RequestID,
			TurnSeq:        stream.TurnSeq,
			ModelCallID:    pending.ModelCallID,
			ProviderPass:   pending.ProviderPass,
			Disposition:    completionDispositionCompleteAfterExternal,
		})
	}
	return service.reconcileStream(stream)
}

func (service *Service) applyApprovedSwitchMode(stream *ActiveStream, conversationID string, toolCall *agentv1.ToolCall) error {
	switchToolCall := toolCall.GetSwitchModeToolCall()
	if switchToolCall == nil || switchToolCall.GetResult().GetSuccess() == nil {
		return nil
	}
	targetMode, err := switchModeTarget(switchToolCall.GetArgs())
	if err != nil {
		return err
	}
	targetAlias, err := modeAlias(targetMode)
	if err != nil {
		return err
	}
	stream.mu.Lock()
	stream.Mode = targetMode
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	_, err = service.updateConversationMetaAndCheckpoint(stream, conversationID, func(item *ConversationFile) error {
		if item == nil {
			return nil
		}
		item.Mode = targetAlias
		return nil
	})
	return err
}

func applySwitchModeMetadata(stream *ActiveStream, toolCall *agentv1.ToolCall) {
	switchToolCall := toolCall.GetSwitchModeToolCall()
	if switchToolCall == nil {
		return
	}
	success := switchToolCall.GetResult().GetSuccess()
	if success == nil {
		return
	}
	if fromAlias, err := modeAlias(currentStreamMode(stream)); err == nil {
		success.FromModeId = fromAlias
	}
}

func switchModeTarget(args *agentv1.SwitchModeArgs) (agentv1.AgentMode, error) {
	if args == nil {
		return agentv1.AgentMode_AGENT_MODE_UNSPECIFIED, fmt.Errorf("switch mode args are required")
	}
	return parseTargetModeID(args.GetTargetModeId())
}

func markInteractionCompleted(stream *ActiveStream, pending runtimecore.PendingInteraction) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	delete(stream.PendingInteractions, pending.InteractionID)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func deriveToolNameFromPendingInteraction(pending runtimecore.PendingInteraction) string {
	switch strings.TrimSpace(pending.InteractionKind) {
	case "ask_question":
		return "AskQuestion"
	case "create_plan":
		return "CreatePlan"
	case "web_search":
		return "WebSearch"
	case "web_fetch":
		return "WebFetch"
	case "switch_mode":
		return "SwitchMode"
	default:
		return ""
	}
}

func isInteractionTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "AskQuestion", "CreatePlan", "WebSearch", "WebFetch", "SwitchMode":
		return true
	default:
		return false
	}
}

func shouldAutoResumeAfterInteraction(pending runtimecore.PendingInteraction) bool {
	switch strings.TrimSpace(pending.InteractionKind) {
	case "create_plan":
		return false
	default:
		return true
	}
}

type generateImageToolCarrier struct {
	Description         string   `json:"description,omitempty"`
	FilePath            string   `json:"file_path,omitempty"`
	ReferenceImagePaths []string `json:"reference_image_paths,omitempty"`
	ImageData           string   `json:"image_data,omitempty"`
}

func isImmediateNativeTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "GenerateImage", "AwaitShell":
		return true
	default:
		return false
	}
}

func (service *Service) handleImmediateNativeToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	switch strings.TrimSpace(invocation.ToolName) {
	case "GenerateImage":
		return service.handleGenerateImageToolInvocation(stream, invocation)
	case "AwaitShell":
		return service.handleAwaitShellToolInvocation(stream, invocation)
	default:
		return fmt.Errorf("unsupported immediate native tool: %s", invocation.ToolName)
	}
}

func (service *Service) handleGenerateImageToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	carrier, decodeErr := decodeGenerateImageToolCarrier(invocation.ArgsJSON)
	args := buildGenerateImageArgsFromCarrier(carrier)
	sanitizedInvocation := invocation
	sanitizedInvocation.ArgsJSON = encodeGenerateImageArgsForHistory(args)
	requestID := ""
	if stream != nil {
		stream.mu.Lock()
		requestID = strings.TrimSpace(stream.RequestID)
		stream.mu.Unlock()
	}
	log.Printf("forwarder generate image tool invoked request_id=%s description_bytes=%d reference_count=%d decode_err=%v", requestID, len(args.GetDescription()), len(args.GetReferenceImagePaths()), decodeErr)
	if decodeErr != nil {
		result, payload := buildGenerateImageErrorResult(decodeErr.Error())
		return service.completeImmediateToolResult(stream, sanitizedInvocation, payload, buildGenerateImageToolCall(args, result))
	}
	description := strings.TrimSpace(args.GetDescription())
	if description == "" {
		result, payload := buildGenerateImageErrorResult("GenerateImage description is required")
		return service.completeImmediateToolResult(stream, sanitizedInvocation, payload, buildGenerateImageToolCall(args, result))
	}
	// 解析当前命中的 chat adapter：复用其 baseURL/apiKey，model 用 ImageModelID（空回退 Model）。
	// 这样同一 adapter 既能 chat（ModelID）又能生图（ImageModelID），零额外凭据。
	modelID := ""
	providerPass := 0
	if stream != nil {
		stream.mu.Lock()
		modelID = strings.TrimSpace(stream.ModelID)
		providerPass = stream.ProviderPassCount
		stream.mu.Unlock()
	}
	channel, err := service.resolveImageChannel(modelID)
	if err != nil {
		result, payload := buildGenerateImageErrorResult(err.Error())
		return service.completeImmediateToolResult(stream, sanitizedInvocation, payload, buildGenerateImageToolCall(args, result))
	}
	// reference_image_paths 非空 → 图生图（/v1/images/edits multipart，读工作区文件）；
	// 否则若本轮有用户上传图 inline data，直取它走图生图（守 F-30 不落盘，不依赖模型填路径）；
	// 都没有 → 文生图（/v1/images/generations）。
	// 参考图在 actor 上同步读完（工作区文件 IO / 内存快照，快），按值捕获进 goroutine。
	referencePaths := compactTrimmedStrings(args.GetReferenceImagePaths())
	var refs []imageReference
	if len(referencePaths) > 0 {
		refs, err = loadReferenceImages(stream, referencePaths)
		if err != nil {
			result, payload := buildGenerateImageErrorResult(err.Error())
			return service.completeImmediateToolResult(stream, sanitizedInvocation, payload, buildGenerateImageToolCall(args, result))
		}
	} else {
		refs = snapshotCurrentTurnSelectedImages(stream)
	}
	mode := "generate"
	if len(refs) > 0 {
		mode = "edit"
	}
	log.Printf("forwarder generate image refs resolved request_id=%s mode=%s reference_image_paths=%d inline_refs=%d", requestID, mode, len(referencePaths), len(refs))
	// 生图是慢操作（实测 ~93s）。若在 stream actor goroutine 上同步调用 generateImage/editImage，
	// 会阻塞 actor mailbox 整整一轮——期间 Cursor 的并发 BidiAppend RPC 全部堆积取不到响应，
	// 触发 mitm 60s ResponseHeaderTimeout → 499 → Cursor 取消 → provider loop interrupted。
	// 故此处只登记 pending + 派发 goroutine + 立即返回；pendingBridgeCount 计入 PendingImages
	// 让 provider 流进 TurnPhaseWaitingExternal 暂停等结果；goroutine 跑完通过 dispatchInboundIntent
	// 把结果回投到 mailbox，由 actor 串行处理（handleImageResult），无竞争。
	imageID := strings.TrimSpace(invocation.CallID)
	if imageID == "" {
		imageID = fmt.Sprintf("img-%s-%d", requestID, stream.TurnSeq)
	}
	pending := pendingImage{
		ImageID:                  imageID,
		ToolCallID:               invocation.CallID,
		ToolName:                 strings.TrimSpace(invocation.ToolName),
		ModelCallID:              invocation.ModelCallID,
		ArgsJSON:                 sanitizedInvocation.ArgsJSON,
		ReasoningContent:         invocation.ReasoningContent,
		ReasoningSignature:       invocation.ReasoningSignature,
		ReasoningSignatureSource: invocation.ReasoningSignatureSource,
		FilePath:                 args.GetFilePath(),
		ProviderPass:             providerPass,
		OpenedAt:                 time.Now().UTC(),
	}
	stream.mu.Lock()
	if stream.PendingImages == nil {
		stream.PendingImages = make(map[string]pendingImage)
	}
	stream.PendingImages[imageID] = pending
	providerCtx := stream.ProviderContext // #1:持锁快照，供 goroutine 派生可取消 ctx；ctx 是不可变接口，快照后读安全
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	// channel/refs/requestID/description/providerCtx 均为值类型捕获，goroutine 不再触碰 stream 共享态。
	go service.runImageGeneration(requestID, pending, channel.BaseURL, channel.APIKey, channel.ImageModelID, description, refs, providerCtx)
	return nil
}

// runImageGeneration 在独立 goroutine 中执行慢生图调用，完成后把结果回投到 stream actor mailbox。
//
// 关键约束：本函数运行在自由 goroutine 上，绝不直接触碰 stream 的历史/phase/pending 共享态——
// 只调 generateImage/editImage（纯 HTTP），再通过 dispatchInboundIntent 把结果作为 image_result
// intent 投递。dispatchInboundIntent 会 postStreamCommandWait 进 actor mailbox，由 actor 串行处理
// （handleImageResult），从而无竞争地写历史 + 推进 turn。stream 若已取消/终结，streamForIntent
// 返回 no-op，回投自动丢弃，pending 随 stream 回收，无泄漏。
func (service *Service) runImageGeneration(requestID string, pending pendingImage, baseURL, apiKey, model, prompt string, refs []imageReference, providerCtx context.Context) {
	// 生图慢（部分上游正常 120s+）。整体超时用 ctx 看门狗兜底，绝不靠 http.Client.Timeout 砍正常慢请求。
	// 15 分钟：覆盖最慢的图生图（含上传 + 渲染），同时真死 hung 会被砍掉释放 goroutine，不泄漏。
	// #1:parent 取自 stream.ProviderContext——客户端 cancel（broker.Cancel→ProviderCancel()）会级联取消本 ctx，
	// 即时中断在途生图/图生图 HTTP，不再空跑 15min 白耗上游额度。ProviderContext 可能为 nil（生图在 provider
	// 尚未启动时触发的极端场景），回退 context.Background() 保旧行为（仅 15min 看门狗）。
	parent := providerCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()
	var imageData string
	var imgErr error
	if len(refs) > 0 {
		imageData, imgErr = editImage(ctx, baseURL, apiKey, model, prompt, refs)
	} else {
		imageData, imgErr = generateImage(ctx, baseURL, apiKey, model, prompt)
	}
	payload := imageResultPayload{
		ImageID:    pending.ImageID,
		ToolCallID: pending.ToolCallID,
		FilePath:   pending.FilePath,
	}
	if imgErr != nil {
		payload.Err = imgErr.Error()
	} else {
		payload.ImageData = imageData
	}
	intent := InboundIntent{Kind: "image_result", RequestID: requestID, ImageResult: &payload}
	// dispatchInboundIntent 内部 postStreamCommandWait 会阻塞至 actor 处理完；goroutine 阻塞无妨
	// （它本就不在 actor 路径上）。错误仅记日志——stream 已不在则属预期（用户中途取消）。
	if err := service.dispatchInboundIntent(intent); err != nil {
		log.Printf("forwarder image result dispatch failed request_id=%s image_id=%s err=%v", requestID, pending.ImageID, err)
	}
}

// handleImageResult 在 stream actor goroutine 上处理 image_result 回投（与 handleInteractionResult 同构）。
//
// 流程：从 PendingImages 取出 pending（并删除）→ 据 payload.Err 重建 success/error tool_call →
// 走 5 步尾（appendToolResult/publishToolCallCompleted/syncSummaryCarryForward/publishCheckpoint/
// reconcileStream）→ 记 PendingProviderCompletion 为 complete_after_external（terminal：不再 resume
// 模型，等价同步版的 markProviderTerminalToolInvocation）。reconcileStream 取出该 completion 后完成 turn。
func (service *Service) handleImageResult(intent InboundIntent) error {
	stream, ok := service.broker.Get(intent.RequestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", intent.RequestID)
	}
	if intent.ImageResult == nil {
		return fmt.Errorf("image result is required")
	}
	payload := intent.ImageResult
	stream.mu.Lock()
	pending, found := stream.PendingImages[payload.ImageID]
	delete(stream.PendingImages, payload.ImageID)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if !found {
		return fmt.Errorf("pending image not found: %s", payload.ImageID)
	}

	// 从 pending.ArgsJSON 反解 args 喂给现有 builder（零新代码）。
	carrier, _ := decodeGenerateImageToolCarrier(pending.ArgsJSON)
	args := buildGenerateImageArgsFromCarrier(carrier)
	var toolCall *agentv1.ToolCall
	var resultPayload string
	if strings.TrimSpace(payload.Err) != "" {
		result, p := buildGenerateImageErrorResult(payload.Err)
		resultPayload, toolCall = p, buildGenerateImageToolCall(args, result)
	} else {
		result, p := buildGenerateImageSuccessResult(payload.FilePath, payload.ImageData)
		resultPayload, toolCall = p, buildGenerateImageToolCall(args, result)
	}

	if err := service.appendToolResult(stream, pending.ToolCallID, pending.ToolName, pending.ArgsJSON, resultPayload, pending.ReasoningContent, toolCall); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(intent.RequestID, pending.ToolCallID, pending.ModelCallID, toolCall); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, intent.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishCheckpoint(intent.RequestID, stream.ConversationID); err != nil {
		return err
	}
	// terminal-after-external：等价同步版的 markProviderTerminalToolInvocation——
	// 不在派发时 set（结果未到会让 turn 提前完成），而在回投时由 completion 表达。
	rememberPendingProviderCompletion(stream, pendingTurnCompletion{
		ConversationID: stream.ConversationID,
		RequestID:      stream.RequestID,
		TurnSeq:        stream.TurnSeq,
		ModelCallID:    pending.ModelCallID,
		ProviderPass:   pending.ProviderPass,
		Disposition:    completionDispositionCompleteAfterExternal,
	})
	return service.reconcileStream(stream)
}

// resolveImageChannel 解析生图所需的 baseURL/apiKey/model，两段兜底：
//
//  1. 先按 stream.ModelID 找 chat adapter（现状路径）。命中且该 adapter 挂了 ImageModelID，
//     或 Role==both/image（自带生图能力）→ 复用其 baseURL/apiKey/imageModelID。
//  2. 否则取全局 image adapter（Role==image/both，按 Priority 升序取首个 enabled）——
//     让纯 image adapter 可独立配置，不依赖 chat adapter 的 ModelID 命中。
//
// model 优先用 adapter 的 ImageModelID（如 gpt-image-2），空则回退 Model（chat 模型名）——
// 后者只在 adapter 配的就是 image 模型时成立，否则 images API 会返回 model not found（错误会友好回传）。
// 返回的 ResolvedChannel.ImageModelID 已填好最终 model，BaseURL/APIKey 直接复用。
func (service *Service) resolveImageChannel(modelID string) (*legacyruntime.ResolvedChannel, error) {
	if service == nil || service.resolver == nil {
		return nil, fmt.Errorf("image channel resolver unavailable")
	}
	ctx := context.Background()
	resolveFrom := func(channel *legacyruntime.ResolvedChannel) (*legacyruntime.ResolvedChannel, error) {
		if channel == nil {
			return nil, fmt.Errorf("no enabled adapter for model %s", modelID)
		}
		imageModel := strings.TrimSpace(channel.ImageModelID)
		if imageModel == "" {
			imageModel = strings.TrimSpace(channel.Model)
		}
		if imageModel == "" {
			return nil, fmt.Errorf("image model is empty (set imageModelID on the adapter)")
		}
		if strings.TrimSpace(channel.BaseURL) == "" || strings.TrimSpace(channel.APIKey) == "" {
			return nil, fmt.Errorf("image adapter missing baseURL or apiKey")
		}
		channel.ImageModelID = imageModel
		return channel, nil
	}
	// 第一段：chat adapter 命中且具备生图能力（ImageModelID 非空，或 Role==both/image）。
	if strings.TrimSpace(modelID) != "" {
		channel, err := service.resolver.SelectChannelForModel(ctx, modelID)
		if err == nil && channel != nil {
			role := strings.TrimSpace(channel.Role)
			if strings.TrimSpace(channel.ImageModelID) != "" || role == "both" || role == "image" {
				return resolveFrom(channel)
			}
		}
	}
	// 第二段：全局 image adapter 兜底（Role==image/both）。
	channel, err := service.resolver.SelectChannelForImage(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve image channel: %w (configure an adapter with role=image/both and imageModelID)", err)
	}
	return resolveFrom(channel)
}

// loadReferenceImages 把图生图的参考图路径解析为已读到内存的字节切片。
//
// 路径解析复用 resolveWorkspacePath（requireExisting=true）：相对路径按当前 stream 的工作区根
// 逐个 join，绝对路径直接用；找不到的路径友好报错（哪条、为什么）。这是 forwarder 里第一个
// 服务端读工作区文件的场景（Read 工具走 exec bridge 由客户端读），故在此内联，不抽公共 helper。
//
// 文件名取 filepath.Base，multipart 用它作文件名（OpenAI 据扩展名判 Content-Type）。
func loadReferenceImages(stream *ActiveStream, paths []string) ([]imageReference, error) {
	pathContext := snapshotStreamPathContext(stream)
	refs := make([]imageReference, 0, len(paths))
	for _, raw := range paths {
		resolved, ok := resolveWorkspacePath(raw, pathContext.workspacePaths, true)
		if !ok {
			return nil, fmt.Errorf("reference image not found in workspace: %s", raw)
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read reference image %s: %w", raw, err)
		}
		refs = append(refs, imageReference{
			filename: filepath.Base(resolved),
			data:     data,
		})
	}
	return refs, nil
}

// snapshotCurrentTurnSelectedImages 返回 stream.CurrentTurnSelectedImages 的副本（nil 安全）。
// 由 handleGenerateImageToolInvocation 在 reference_image_paths 为空时调用，直取本轮上传图 inline data
// 走 /v1/images/edits，不落盘、不依赖模型填路径。
func snapshotCurrentTurnSelectedImages(stream *ActiveStream) []imageReference {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if len(stream.CurrentTurnSelectedImages) == 0 {
		return nil
	}
	// 深拷贝 data 切片——调用方（goroutine）会持有 refs 一段时间，不能与 stream 共享底层数组。
	refs := make([]imageReference, len(stream.CurrentTurnSelectedImages))
	for i, ref := range stream.CurrentTurnSelectedImages {
		refs[i] = imageReference{
			filename: ref.filename,
			mimeType: ref.mimeType,
			data:     append([]byte(nil), ref.data...),
		}
	}
	return refs
}

// extractCurrentTurnSelectedImages 从 UserMessage 的 SelectedContext.SelectedImages 提取 inline data，
// 构造 []imageReference 供图生图直取。在 normalizeUserMessageForStorage 之前调用——此时 inline data
// 仍在内存。复用 guardSelectedImages 的 data 获取与 F-30 上限（单图 10MB / 总 32MB / 最多 6 张，
// 对齐 Claude 官方 Vision 约束），超限走缩放重编码（长边≤2000px）而非裸截断，
// 避免落盘外发任意本地文件。filename 由 mime 推扩展名（缺失回退 .png），供 multipart 文件名用。
func extractCurrentTurnSelectedImages(userMessage *agentv1.UserMessage) []imageReference {
	if userMessage == nil {
		return nil
	}
	images := userMessage.GetSelectedContext().GetSelectedImages()
	if len(images) == 0 {
		return nil
	}
	// 诊断：图生图关键调查——上传图到底以什么形态到达 forwarder。
	// Cursor 可能只传 blob_id（非 inline data），F-30 会丢弃、模型也看不到 → 图生图走文生图。
	// 打印每张图的形态，便于定位"图生图没参考图"根因。
	for i, image := range images {
		if image == nil {
			log.Printf("forwarder selected_image[%d] form=nil", i)
			continue
		}
		form := "empty"
		switch image.GetDataOrBlobId().(type) {
		case *agentv1.SelectedImage_Data:
			form = "data"
		case *agentv1.SelectedImage_BlobId:
			form = "blob_id"
		case *agentv1.SelectedImage_BlobIdWithData_:
			form = "blob_id_with_data"
		}
		log.Printf("forwarder selected_image[%d] form=%s path_bytes=%d data_bytes=%d blob_id_with_data_bytes=%d mime=%q", i, form, len(strings.TrimSpace(image.GetPath())), len(image.GetData()), len(image.GetBlobIdWithData().GetData()), strings.TrimSpace(image.GetMimeType()))
	}
	refs := make([]imageReference, 0, minInt(len(images), promptGuardSelectedImageMaxCount))
	remaining := promptGuardSelectedImagesTotalBytes
	for _, image := range images {
		if image == nil || len(refs) >= promptGuardSelectedImageMaxCount {
			continue
		}
		data := image.GetData()
		if len(data) == 0 {
			data = image.GetBlobIdWithData().GetData()
		}
		if len(data) == 0 {
			continue // 仅含 path 无 inline data——F-30 不读文件系统，跳过
		}
		mimeType := image.GetMimeType()
		if len(data) > promptGuardSelectedImageMaxBytes {
			// 超单图字节上限：缩放重编码（长边≤2000px）而非裸字节截断——裸截断会损坏二进制完整性。
			// 仅在字节超限时缩放，避免对范围内参考图无谓降采样。
			rescaled, newMIME, _, err := rescaleImageIfNeeded(data, mimeType, promptGuardSelectedImageMaxBytes, imageRescaleMaxEdge)
			if err == nil && rescaled != nil {
				data = rescaled
				if newMIME != "" {
					mimeType = newMIME // 缩放可能 webp/gif→png/jpeg，同步更新 MIME 供下游嗅探
				}
			} else {
				log.Printf("forwarder selected_image rescale failed bytes=%d err=%v — dropping image", len(data), err)
				continue // 缩放/解码失败整张丢弃，绝不裸截断或外发损坏数据
			}
		}
		if remaining <= 0 {
			break
		}
		if len(data) > remaining {
			// 超总量预算：再缩放一次（按剩余字节为上限）。仍超则丢弃该张 + log。
			rescaled, newMIME, _, err := rescaleImageIfNeeded(data, mimeType, remaining, imageRescaleMaxEdge)
			if err == nil && rescaled != nil && len(rescaled) <= remaining {
				data = rescaled
				if newMIME != "" {
					mimeType = newMIME
				}
			} else {
				log.Printf("forwarder selected_image over total budget bytes=%d remaining=%d err=%v — dropping image", len(data), remaining, err)
				continue
			}
		}
		remaining -= len(data)
		refs = append(refs, imageReference{
			filename: imageFilenameForMIME(mimeType, data),
			mimeType: mimeType,
			data:     append([]byte(nil), data...),
		})
	}
	if len(refs) == 0 {
		return nil
	}
	return refs
}

// imageFilenameForMIME 据 mime / 字节嗅探推一个 multipart 文件名（扩展名决定 Content-Type）。
// 复用 http.DetectContentType 嗅探，mime 显式声明优先；缺失回退 .png（OpenAI images 默认接受 png）。
func imageFilenameForMIME(mimeType string, payload []byte) string {
	normalized := strings.TrimSpace(strings.ToLower(mimeType))
	if normalized == "" && len(payload) > 0 {
		if detected := strings.TrimSpace(strings.ToLower(http.DetectContentType(payload))); strings.HasPrefix(detected, "image/") {
			normalized = detected
		}
	}
	ext := ".png"
	switch normalized {
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	return "reference" + ext
}

func markProviderTerminalToolInvocation(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.ProviderTerminalToolInvocation = true
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
}

func decodeGenerateImageToolCarrier(raw []byte) (generateImageToolCarrier, error) {
	var carrier generateImageToolCarrier
	if len(raw) == 0 {
		return carrier, nil
	}
	if err := json.Unmarshal(raw, &carrier); err != nil {
		return carrier, fmt.Errorf("decode GenerateImage args failed: %w", err)
	}
	carrier.Description = strings.TrimSpace(carrier.Description)
	carrier.FilePath = strings.TrimSpace(carrier.FilePath)
	carrier.ImageData = strings.TrimSpace(carrier.ImageData)
	carrier.ReferenceImagePaths = compactTrimmedStrings(carrier.ReferenceImagePaths)
	return carrier, nil
}

func compactTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	items := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

func buildGenerateImageArgsFromCarrier(carrier generateImageToolCarrier) *agentv1.GenerateImageArgs {
	args := &agentv1.GenerateImageArgs{
		Description:         strings.TrimSpace(carrier.Description),
		ReferenceImagePaths: append([]string(nil), carrier.ReferenceImagePaths...),
	}
	if filePath := strings.TrimSpace(carrier.FilePath); filePath != "" {
		args.FilePath = &filePath
	}
	return args
}

func encodeGenerateImageArgsForHistory(args *agentv1.GenerateImageArgs) []byte {
	payload := map[string]any{}
	if args != nil {
		if description := strings.TrimSpace(args.GetDescription()); description != "" {
			payload["description"] = description
		}
		if filePath := strings.TrimSpace(args.GetFilePath()); filePath != "" {
			payload["file_path"] = filePath
		}
		if referenceImagePaths := compactTrimmedStrings(args.GetReferenceImagePaths()); len(referenceImagePaths) > 0 {
			payload["reference_image_paths"] = referenceImagePaths
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

func buildGenerateImageToolCall(args *agentv1.GenerateImageArgs, result *agentv1.GenerateImageResult) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_GenerateImageToolCall{
			GenerateImageToolCall: &agentv1.GenerateImageToolCall{
				Args:   args,
				Result: result,
			},
		},
	}
}

func buildGenerateImageSuccessResult(filePath string, imageData string) (*agentv1.GenerateImageResult, string) {
	trimmedFilePath := strings.TrimSpace(filePath)
	payload := fmt.Sprintf("generate image success image_data_bytes=%d", len(imageData))
	if trimmedFilePath != "" {
		payload = fmt.Sprintf("generate image success file_path=%s image_data_bytes=%d", trimmedFilePath, len(imageData))
	}
	return &agentv1.GenerateImageResult{
		Result: &agentv1.GenerateImageResult_Success{
			Success: &agentv1.GenerateImageSuccess{
				FilePath:  trimmedFilePath,
				ImageData: imageData,
			},
		},
	}, payload
}

func buildGenerateImageErrorResult(message string) (*agentv1.GenerateImageResult, string) {
	errorText := strings.TrimSpace(message)
	if errorText == "" {
		errorText = "image generation failed"
	}
	return &agentv1.GenerateImageResult{
		Result: &agentv1.GenerateImageResult_Error{
			Error: &agentv1.GenerateImageError{Error: errorText},
		},
	}, errorText
}

func isLocalStateTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "TodoWrite":
		return true
	default:
		return false
	}
}

func (service *Service) handleLocalStateToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	switch strings.TrimSpace(invocation.ToolName) {
	case "TodoWrite":
		return service.handleTodoWriteToolInvocation(stream, invocation)
	default:
		return fmt.Errorf("unsupported local state tool: %s", invocation.ToolName)
	}
}

func (service *Service) handleTodoWriteToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	decodedArgs, err := decodeUpdateTodosArgsJSONWithPresence(invocation.ArgsJSON)
	if err != nil {
		return service.completeImmediateToolResult(stream, invocation, summarizeUpdateTodosResult(&agentv1.UpdateTodosResult{
			Result: &agentv1.UpdateTodosResult_Error{
				Error: &agentv1.UpdateTodosError{Error: err.Error()},
			},
		}), buildUpdateTodosToolCall(&agentv1.UpdateTodosArgs{}, &agentv1.UpdateTodosResult{
			Result: &agentv1.UpdateTodosResult_Error{
				Error: &agentv1.UpdateTodosError{Error: err.Error()},
			},
		}))
	}
	args := decodedArgs.Args
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return err
	}
	structuredState, err := projectConversationStructuredState(conversation)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	applyMerge := shouldMergeTodoUpdate(args, decodedArgs.MergeSet, structuredState.Todos)
	args.Merge = applyMerge
	var nextTodos []*agentv1.TodoItem
	if applyMerge {
		nextTodos, err = mergeTodoItems(structuredState.Todos, args.GetTodos(), now)
	} else {
		nextTodos, err = normalizeTodoItems(args.GetTodos(), now, true)
	}
	if err != nil {
		result := buildUpdateTodosErrorResult(args, err.Error())
		return service.completeImmediateToolResult(stream, invocation, summarizeUpdateTodosResult(result), buildUpdateTodosToolCall(args, result))
	}
	if !applyMerge {
		if missingIDs := missingActiveTodoReplacementIDs(structuredState.Todos, nextTodos); len(missingIDs) > 0 {
			result := buildUpdateTodosErrorResult(args, unsafeTodoReplaceError(missingIDs))
			return service.completeImmediateToolResult(stream, invocation, summarizeUpdateTodosResult(result), buildUpdateTodosToolCall(args, result))
		}
	}
	result := &agentv1.UpdateTodosResult{
		Result: &agentv1.UpdateTodosResult_Success{
			Success: &agentv1.UpdateTodosSuccess{
				Todos:      cloneTodoItems(nextTodos),
				TotalCount: int32(len(nextTodos)),
				WasMerge:   applyMerge,
			},
		},
	}
	return service.completeImmediateToolResult(stream, invocation, summarizeUpdateTodosResult(result), buildUpdateTodosToolCall(args, result))
}

func shouldMergeTodoUpdate(args *agentv1.UpdateTodosArgs, mergeSet bool, existing []*agentv1.TodoItem) bool {
	if args.GetMerge() {
		return true
	}
	if mergeSet || len(existing) == 0 {
		return false
	}
	if len(missingActiveTodoReplacementIDs(existing, args.GetTodos())) > 0 {
		return true
	}
	for _, item := range args.GetTodos() {
		if item == nil {
			continue
		}
		if strings.TrimSpace(item.GetContent()) == "" {
			return true
		}
	}
	return false
}

func (service *Service) handleReadTodosToolInvocation(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	args, err := decodeReadTodosArgsJSON(invocation.ArgsJSON)
	if err != nil {
		return service.completeImmediateToolResult(stream, invocation, summarizeReadTodosResult(&agentv1.ReadTodosResult{
			Result: &agentv1.ReadTodosResult_Error{
				Error: &agentv1.ReadTodosError{Error: err.Error()},
			},
		}), buildReadTodosToolCall(&agentv1.ReadTodosArgs{}, &agentv1.ReadTodosResult{
			Result: &agentv1.ReadTodosResult_Error{
				Error: &agentv1.ReadTodosError{Error: err.Error()},
			},
		}))
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return err
	}
	structuredState, err := projectConversationStructuredState(conversation)
	if err != nil {
		return err
	}
	filtered := filterTodoItems(structuredState.Todos, args.GetStatusFilter(), args.GetIdFilter())
	result := &agentv1.ReadTodosResult{
		Result: &agentv1.ReadTodosResult_Success{
			Success: &agentv1.ReadTodosSuccess{
				Todos:      filtered,
				TotalCount: int32(len(filtered)),
			},
		},
	}
	return service.completeImmediateToolResult(stream, invocation, summarizeReadTodosResult(result), buildReadTodosToolCall(args, result))
}

func (service *Service) completeImmediateToolResult(stream *ActiveStream, invocation runtimecore.ToolInvocation, resultText string, toolCall *agentv1.ToolCall) error {
	if err := service.appendToolResult(stream, invocation.CallID, strings.TrimSpace(invocation.ToolName), invocation.ArgsJSON, resultText, invocation.ReasoningContent, toolCall); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(stream.RequestID, invocation.CallID, invocation.ModelCallID, toolCall); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, invocation.ModelCallID); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}
