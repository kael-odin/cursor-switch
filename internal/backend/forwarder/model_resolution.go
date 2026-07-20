// model_resolution.go 实现模型名解析与上下文窗口 token 同步。从 service.go 拆出。
package forwarder

import (
	"context"
	"strings"

	"cursor/gen/agentv1"
)

func (service *Service) resolveRequestedModelName(message *agentv1.AgentClientMessage, modelID string) string {
	if message != nil {
		if runRequest := message.GetRunRequest(); runRequest != nil {
			if name := firstNonEmpty(
				runRequest.GetModelDetails().GetDisplayName(),
				runRequest.GetModelDetails().GetDisplayModelId(),
			); name != "" {
				return name
			}
		}
		if prewarm := message.GetPrewarmRequest(); prewarm != nil {
			if name := firstNonEmpty(
				prewarm.GetModelDetails().GetDisplayName(),
				prewarm.GetModelDetails().GetDisplayModelId(),
			); name != "" {
				return name
			}
		}
	}
	if service != nil && service.resolver != nil {
		channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
		if err == nil && channel != nil {
			if name := firstNonEmpty(channel.Name, channel.Model); name != "" {
				return name
			}
		}
	}
	return strings.TrimSpace(modelID)
}

func (service *Service) resolveContextWindowTokens(modelID string) uint32 {
	if service == nil || service.resolver == nil {
		return projectedConversationMaxTokens
	}
	channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
	if err != nil || channel == nil || channel.ContextWindowTokens <= 0 {
		return projectedConversationMaxTokens
	}
	return clampInt64ToUint32(int64(channel.ContextWindowTokens))
}

func (service *Service) syncConversationContextWindowTokens(stream *ActiveStream, conversationID string, conversation *ConversationFile) (*ConversationFile, error) {
	if stream == nil || conversation == nil {
		return conversation, nil
	}
	stream.mu.Lock()
	modelID := stream.ModelID
	stream.mu.Unlock()
	target := service.resolveContextWindowTokens(modelID)
	if target == 0 || conversation.TokenDetailsMaxTokens == target {
		return conversation, nil
	}
	return service.updateConversationMetaAndCheckpoint(stream, conversationID, func(item *ConversationFile) error {
		if item == nil {
			return nil
		}
		item.TokenDetailsMaxTokens = target
		return nil
	})
}
