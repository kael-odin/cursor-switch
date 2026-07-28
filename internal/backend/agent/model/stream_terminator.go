package modeladapter

import (
	"fmt"
	"strings"
)

// streamTerminatorMissingError 是 F-20 的"流未以合法终止事件结束"错误。
//
// 三种 provider 流（OpenAI Chat Completions / Responses / Anthropic Messages）此前
// 在正常 EOF 即返回 nil——即使没有 [DONE]/response.completed/message_stop，或
// 根本没收到任何有效事件（如普通 200 JSON 零事件）。恶意/异常 provider 可用这种
// "空成功"伪装成功，让 forwarder 误以为已得到完整回复。
//
// 修复后各 adapter 的 Stream 记录是否收到合法终止事件；未收到而 EOF 即返回本错误。
// 本错误对 isRetryableChannelError 可重试（换 provider 可能正常完成流），让 Router
// failover 到下一候选。错误形态以 "provider stream truncated: " 开头，便于判定。
func streamTerminatorMissingError(provider string) error {
	return fmt.Errorf("provider stream truncated: %s ended without a valid terminator event", provider)
}

// isStreamTerminatorMissingError 判定 err 是否为 F-20 的流截断错误。
func isStreamTerminatorMissingError(err error) bool {
	if err == nil {
		return false
	}
	return strings.HasPrefix(err.Error(), "provider stream truncated: ")
}
