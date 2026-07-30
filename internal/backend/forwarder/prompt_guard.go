package forwarder

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

const (
	promptGuardUserTextChars            = 32000
	promptGuardUserRichTextChars        = 32000
	promptGuardSubagentReminderChars    = 12000
	promptGuardSelectedFileChars        = 16000
	promptGuardSelectedFilesTotalChars  = 64000
	promptGuardSelectedFilesMaxCount    = 12
	promptGuardRequestFileChars         = 16000
	promptGuardRequestFilesTotalChars   = 64000
	promptGuardRequestFilesMaxCount     = 12
	promptGuardRuleChars                = 6000
	promptGuardRulesTotalChars          = 24000
	promptGuardRulesMaxCount            = 40
	promptGuardSkillDescriptionChars    = 800
	promptGuardSkillDescriptionsTotal   = 16000
	promptGuardSkillDescriptorsMaxCount = 32
	promptGuardAgentSkillContentChars   = 6000
	promptGuardAgentSkillsMaxCount      = 16
	promptGuardRealtimeTextChars        = 12000
	promptGuardCompiledMessageChars     = 120000
	// F-30：SelectedImage 只接受内联 Data，服务端绝不读 Path。
	// 单图 10MB、请求总量 32MB——对齐 Claude 官方 Vision 约束（单图 10MB、请求总 32MB）。
	// 见 https://platform.claude.com/docs/en/docs/build-with-claude/vision （Request limits）。
	// OpenAI /v1/images/edits 单图 <50MB，10MB 亦满足。
	// 超限走「解码→缩放→重编码」（长边≤2000px），而非裸字节截断——二进制中间切片会损坏
	// PNG/JPEG 完整性致上游解码失败。缩放失败或解码失败则整张丢弃，避免任意本地文件被读后外发。
	// 最多 6 张（Claude 20 图内无更严限制，6 张保守合理）。
	promptGuardSelectedImageMaxCount    = 6
	promptGuardSelectedImageMaxBytes    = 10 * 1024 * 1024
	promptGuardSelectedImagesTotalBytes = 32 * 1024 * 1024
)

func normalizeUserMessageForStorage(userMessage *agentv1.UserMessage) *agentv1.UserMessage {
	if userMessage == nil {
		return nil
	}
	cloned, ok := proto.Clone(userMessage).(*agentv1.UserMessage)
	if !ok || cloned == nil {
		return userMessage
	}
	cloned.Text = truncatePromptGuardText("user_message.text", cloned.GetText(), promptGuardUserTextChars)
	if richText := strings.TrimSpace(cloned.GetRichText()); richText != "" {
		cloned.RichText = stringPtr(truncatePromptGuardText("user_message.rich_text", richText, promptGuardUserRichTextChars))
	}
	if reminder := strings.TrimSpace(cloned.GetSubagentSystemReminder()); reminder != "" {
		cloned.SubagentSystemReminder = stringPtr(truncatePromptGuardText("user_message.subagent_system_reminder", reminder, promptGuardSubagentReminderChars))
	}
	cloned.SelectedContext = guardSelectedContext(cloned.GetSelectedContext())
	return cloned
}

func guardRequestContextForStorage(requestContext *agentv1.RequestContext) {
	if requestContext == nil {
		return
	}
	requestContext.Rules = guardCursorRules(requestContext.GetRules())
	requestContext.FileContents = guardStringMap(
		requestContext.GetFileContents(),
		"request_context.file_contents",
		promptGuardRequestFileChars,
		promptGuardRequestFilesTotalChars,
		promptGuardRequestFilesMaxCount,
	)
	if summary := strings.TrimSpace(requestContext.GetUserIntentSummary()); summary != "" {
		requestContext.UserIntentSummary = stringPtr(truncatePromptGuardText("request_context.user_intent_summary", summary, promptGuardRealtimeTextChars))
	}
	if hooks := strings.TrimSpace(requestContext.GetHooksAdditionalContext()); hooks != "" {
		requestContext.HooksAdditionalContext = stringPtr(truncatePromptGuardText("request_context.hooks_additional_context", hooks, promptGuardRealtimeTextChars))
	}
	if commit := strings.TrimSpace(requestContext.GetCommitAttributionMessage()); commit != "" {
		requestContext.CommitAttributionMessage = stringPtr(truncatePromptGuardText("request_context.commit_attribution_message", commit, promptGuardRealtimeTextChars))
	}
	if pr := strings.TrimSpace(requestContext.GetPrAttributionMessage()); pr != "" {
		requestContext.PrAttributionMessage = stringPtr(truncatePromptGuardText("request_context.pr_attribution_message", pr, promptGuardRealtimeTextChars))
	}
}

func guardCompiledConversationForProvider(compiled CompiledConversation) CompiledConversation {
	for index := range compiled.Messages {
		message := &compiled.Messages[index]
		if strings.TrimSpace(message.Role) == "system" {
			continue
		}
		if strings.TrimSpace(message.Content) != "" {
			message.Content = truncatePromptGuardText("compiled."+firstNonEmpty(strings.TrimSpace(message.Role), "message"), message.Content, promptGuardCompiledMessageChars)
		}
		for partIndex := range message.ContentParts {
			if strings.TrimSpace(message.ContentParts[partIndex].Text) == "" {
				continue
			}
			message.ContentParts[partIndex].Text = truncatePromptGuardText("compiled.content_part", message.ContentParts[partIndex].Text, promptGuardCompiledMessageChars)
		}
	}
	return compiled
}

func guardSelectedContext(selectedContext *agentv1.SelectedContext) *agentv1.SelectedContext {
	if selectedContext == nil {
		return nil
	}
	cloned, ok := proto.Clone(selectedContext).(*agentv1.SelectedContext)
	if !ok || cloned == nil {
		return selectedContext
	}
	cloned.Files = guardSelectedFiles(cloned.GetFiles())
	cloned.SelectedSkills = guardAgentSkills(cloned.GetSelectedSkills())
	cloned.SelectedImages = guardSelectedImages(cloned.GetSelectedImages())
	cloned.ExtraContext = guardStringSlice(cloned.GetExtraContext(), "selected_context.extra_context", promptGuardRealtimeTextChars, promptGuardRealtimeTextChars, promptGuardAgentSkillsMaxCount)
	return cloned
}

// guardSelectedImages 实施 F-30 防护：SelectedImage 只接受内联 Data，绝不读 Path。
//
// 服务端读取客户端提供的 Path 会把应用权限范围内的任意本地文件（含 symlink 目标）
// Base64 后外发给 provider。这里在入站处：
//   - 清空 Path 字段（即便下游仍保留读路径逻辑也无法命中）
//   - 仅保留携带非空内联 Data 的条目
//   - 限单图字节数与总字节数，超限截断或丢弃
//   - 限数量
//
// 注意：BlobIdWithData.data 同样视为内联数据，一并纳入上限。
func guardSelectedImages(images []*agentv1.SelectedImage) []*agentv1.SelectedImage {
	if len(images) == 0 {
		return nil
	}
	result := make([]*agentv1.SelectedImage, 0, minInt(len(images), promptGuardSelectedImageMaxCount))
	remaining := promptGuardSelectedImagesTotalBytes
	for _, image := range images {
		if image == nil || len(result) >= promptGuardSelectedImageMaxCount {
			continue
		}
		data := image.GetData()
		if len(data) == 0 {
			data = image.GetBlobIdWithData().GetData()
		}
		if len(data) == 0 {
			// 仅含 path 无内联 data——服务端不读文件系统，直接丢弃（F-30）。
			continue
		}
		if len(data) > promptGuardSelectedImageMaxBytes {
			// 超单图字节上限：缩放重编码（长边≤2000px）而非裸字节截断——裸截断会损坏二进制完整性。
			// 仅在字节超限时缩放，避免对范围内视觉上下文图无谓降采样（保画质）。
			rescaled, _, _, err := rescaleImageIfNeeded(data, "", promptGuardSelectedImageMaxBytes, imageRescaleMaxEdge)
			if err == nil && rescaled != nil {
				data = rescaled
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
			rescaled, _, _, err := rescaleImageIfNeeded(data, "", remaining, imageRescaleMaxEdge)
			if err == nil && rescaled != nil && len(rescaled) <= remaining {
				data = rescaled
			} else {
				log.Printf("forwarder selected_image over total budget bytes=%d remaining=%d err=%v — dropping image", len(data), remaining, err)
				continue
			}
		}
		remaining -= len(data)
		cloned, ok := proto.Clone(image).(*agentv1.SelectedImage)
		if !ok || cloned == nil {
			continue
		}
		cloned.Path = "" // F-30：服务端永不信任 Path
		// 重设 oneof 为内联 Data，丢弃可能残留的 blob_id / blob_id_with_data 形态，
		// 统一为已截断的 data，下游 resolveImageContent 只走内联分支。
		cloned.DataOrBlobId = &agentv1.SelectedImage_Data{Data: data}
		result = append(result, cloned)
	}
	return result
}

func guardSelectedFiles(files []*agentv1.SelectedFile) []*agentv1.SelectedFile {
	if len(files) == 0 {
		return nil
	}
	result := make([]*agentv1.SelectedFile, 0, minInt(len(files), promptGuardSelectedFilesMaxCount))
	remaining := promptGuardSelectedFilesTotalChars
	for _, file := range files {
		if file == nil || len(result) >= promptGuardSelectedFilesMaxCount {
			continue
		}
		content := strings.TrimSpace(file.GetContent())
		if content == "" {
			continue
		}
		limit := minInt(promptGuardSelectedFileChars, remaining)
		if limit <= 0 {
			break
		}
		cloned, ok := proto.Clone(file).(*agentv1.SelectedFile)
		if !ok || cloned == nil {
			continue
		}
		cloned.Content = truncatePromptGuardText("selected_context.files.content", content, limit)
		remaining -= promptGuardRuneCount(cloned.GetContent())
		result = append(result, cloned)
	}
	return result
}

func guardCursorRules(rules []*agentv1.CursorRule) []*agentv1.CursorRule {
	if len(rules) == 0 {
		return nil
	}
	result := make([]*agentv1.CursorRule, 0, minInt(len(rules), promptGuardRulesMaxCount))
	remaining := promptGuardRulesTotalChars
	for _, rule := range rules {
		if rule == nil || len(result) >= promptGuardRulesMaxCount {
			continue
		}
		content := strings.TrimSpace(rule.GetContent())
		if content == "" {
			continue
		}
		limit := minInt(promptGuardRuleChars, remaining)
		if limit <= 0 {
			break
		}
		cloned, ok := proto.Clone(rule).(*agentv1.CursorRule)
		if !ok || cloned == nil {
			continue
		}
		cloned.Content = truncatePromptGuardText("request_context.rules.content", content, limit)
		remaining -= promptGuardRuneCount(cloned.GetContent())
		result = append(result, cloned)
	}
	return result
}

func guardSkillDescriptors(descriptors []*agentv1.SkillDescriptor) []*agentv1.SkillDescriptor {
	if len(descriptors) == 0 {
		return nil
	}
	result := make([]*agentv1.SkillDescriptor, 0, minInt(len(descriptors), promptGuardSkillDescriptorsMaxCount))
	remaining := promptGuardSkillDescriptionsTotal
	for _, descriptor := range descriptors {
		if descriptor == nil || len(result) >= promptGuardSkillDescriptorsMaxCount {
			continue
		}
		description := strings.TrimSpace(descriptor.GetDescription())
		if description == "" {
			continue
		}
		limit := minInt(promptGuardSkillDescriptionChars, remaining)
		if limit <= 0 {
			break
		}
		cloned, ok := proto.Clone(descriptor).(*agentv1.SkillDescriptor)
		if !ok || cloned == nil {
			continue
		}
		cloned.Description = truncatePromptGuardText("skill.description", description, limit)
		remaining -= promptGuardRuneCount(cloned.GetDescription())
		result = append(result, cloned)
	}
	return result
}

func guardAgentSkills(skills []*agentv1.AgentSkill) []*agentv1.AgentSkill {
	if len(skills) == 0 {
		return nil
	}
	result := make([]*agentv1.AgentSkill, 0, minInt(len(skills), promptGuardAgentSkillsMaxCount))
	for _, skill := range skills {
		if skill == nil || len(result) >= promptGuardAgentSkillsMaxCount {
			continue
		}
		cloned, ok := proto.Clone(skill).(*agentv1.AgentSkill)
		if !ok || cloned == nil {
			continue
		}
		if content := strings.TrimSpace(cloned.GetContent()); content != "" {
			cloned.Content = truncatePromptGuardText("agent_skill.content", content, promptGuardAgentSkillContentChars)
		}
		if description := strings.TrimSpace(cloned.GetDescription()); description != "" {
			cloned.Description = truncatePromptGuardText("agent_skill.description", description, promptGuardSkillDescriptionChars)
		}
		result = append(result, cloned)
	}
	return result
}

func guardStringMap(input map[string]string, label string, itemLimit int, totalLimit int, maxItems int) map[string]string {
	if len(input) == 0 {
		return nil
	}
	keys := make([]string, 0, len(input))
	values := make(map[string]string, len(input))
	for key, value := range input {
		trimmedKey := strings.TrimSpace(key)
		trimmedValue := strings.TrimSpace(value)
		if trimmedKey == "" || trimmedValue == "" {
			continue
		}
		if _, exists := values[trimmedKey]; exists {
			continue
		}
		keys = append(keys, trimmedKey)
		values[trimmedKey] = trimmedValue
	}
	sort.Strings(keys)
	result := make(map[string]string, minInt(len(keys), maxItems))
	remaining := totalLimit
	for _, key := range keys {
		if len(result) >= maxItems {
			break
		}
		content := values[key]
		if content == "" {
			continue
		}
		limit := minInt(itemLimit, remaining)
		if limit <= 0 {
			break
		}
		result[key] = truncatePromptGuardText(label, content, limit)
		remaining -= promptGuardRuneCount(result[key])
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func guardStringSlice(input []string, label string, itemLimit int, totalLimit int, maxItems int) []string {
	if len(input) == 0 {
		return nil
	}
	result := make([]string, 0, minInt(len(input), maxItems))
	remaining := totalLimit
	for _, item := range input {
		if len(result) >= maxItems {
			break
		}
		content := strings.TrimSpace(item)
		if content == "" {
			continue
		}
		limit := minInt(itemLimit, remaining)
		if limit <= 0 {
			break
		}
		truncated := truncatePromptGuardText(label, content, limit)
		remaining -= promptGuardRuneCount(truncated)
		result = append(result, truncated)
	}
	return result
}

func truncatePromptGuardText(label string, text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if promptGuardRuneCount(text) <= limit {
		return text
	}
	runes := []rune(text)
	notice := fmt.Sprintf("\n\n[truncated: %s exceeded %d chars; kept head and tail from %d chars]\n\n", strings.TrimSpace(label), limit, len(runes))
	noticeRunes := []rune(notice)
	keep := limit - len(noticeRunes)
	if keep <= 0 {
		return string(runes[:limit])
	}
	head := keep * 2 / 3
	tail := keep - head
	if tail <= 0 {
		return string(runes[:head]) + notice
	}
	return string(runes[:head]) + notice + string(runes[len(runes)-tail:])
}

func promptGuardRuneCount(text string) int {
	return utf8.RuneCountInString(text)
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
