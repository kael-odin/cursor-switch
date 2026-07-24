package runtime

// 本文件曾承载 byok 的假身份常量（InjectAccountEmail / InjectAuthToken / LocalRelayToken）
// 与进程级 loopback 鉴权 token（LoopbackToken / LoopbackAuthorization）。
//
// 现在这些机制均已移除：
// - 假身份已被真实 Cursor 账号透传取代（middleware 捕获凭证 + CredentialOriginalCursor 策略恢复）。
// - loopback 鉴权已被 relayauth 包的私有 proof 头取代（独立于 Authorization）。
//
// 保留此文件作为占位符，避免外部 import "cursor/internal/runtime" 的代码构建失败。
// 未来若需新的进程级常量，在此添加。

