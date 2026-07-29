// package modeladapter circuit_breaker.go 移植 cc-switch 熔断器（A1）。
//
// 熔断器保护：单个 provider 连续失败或错误率过高时自动熔断，避免在一个挂掉的 endpoint
// 上反复重试浪费时间。状态机 Closed → Open → HalfOpen → Closed。
//
// 这是 A1 的第一步：独立的熔断器组件 + per-adapter 注册表。后续 B2（候选链 + failover
// 循环）接入 Router.Stream 时，调用 AllowRequest 判断是否跳过该 provider、RecordResult
// 上报结果。当前单 channel 路由也可直接用：Stream 失败时 RecordFailure，连续失败后
// 短路后续重试。
//
// 移植自 cc-switch src-tauri/src/proxy/circuit_breaker.rs，去掉 Rust async/RwLock，
// 用 sync.Mutex 等价（临界区小，读写不分开）。HalfOpen 探测名额限流（max=1）保留。
package modeladapter

import (
	"strings"
	"sync"
	"time"
)

// CircuitState 熔断器状态。
type CircuitState int

const (
	CircuitClosed CircuitState = iota // 正常工作
	CircuitOpen                       // 熔断激活，拒绝请求
	CircuitHalfOpen                   // 尝试恢复，限流放行探测请求
)

// String 用于日志/调试。
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half_open"
	}
	return "unknown"
}

// CircuitBreakerConfig 是熔断器可配参数（支持热更新，不重置状态）。
type CircuitBreakerConfig struct {
	// FailureThreshold 连续失败多少次后打开熔断器。
	FailureThreshold uint32
	// SuccessThreshold 半开状态下连续成功多少次后关闭熔断器。
	SuccessThreshold uint32
	// TimeoutSeconds 熔断器打开后多久尝试半开探测。
	TimeoutSeconds uint64
	// ErrorRateThreshold 错误率超过此值时打开熔断器（0.0-1.0）。
	ErrorRateThreshold float64
	// MinRequests 计算错误率前的最小请求数（不足时只看连续失败）。
	MinRequests uint32
}

// DefaultCircuitBreakerConfig 对齐 cc-switch 默认值。
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold:   4,
		SuccessThreshold:   2,
		TimeoutSeconds:     60,
		ErrorRateThreshold: 0.6,
		MinRequests:        10,
	}
}

// AllowResult 是 AllowRequest 的返回。
type AllowResult struct {
	// Allowed 是否放行本次请求。
	Allowed bool
	// UsedHalfOpenPermit 本次是否占用了 HalfOpen 探测名额。
	// 调用方须在请求结束后把它传回 RecordSuccess/RecordFailure 以释放名额。
	UsedHalfOpenPermit bool
}

// CircuitBreaker 是单个 provider 的熔断器实例。
//
// 状态转换：
//   - Closed + 连续失败达 FailureThreshold / 错误率达 ErrorRateThreshold（且 total>=MinRequests）→ Open
//   - Open + 距上次打开超过 TimeoutSeconds → HalfOpen（放行 1 个探测请求）
//   - HalfOpen + 探测成功达 SuccessThreshold → Closed
//   - HalfOpen + 探测失败 → Open
type CircuitBreaker struct {
	mu sync.Mutex

	state          CircuitState
	lastOpenedAt   time.Time // Open 状态开始时间，零值表示未打开过
	consecutiveFailures  uint32
	consecutiveSuccesses uint32
	totalRequests  uint32
	failedRequests uint32

	// halfOpenInFlight 当前 HalfOpen 状态已放行的探测请求数（限流，上限 1）。
	halfOpenInFlight uint32

	// lastFailureReason 最近一次 RecordFailure 记录的真实失败原因（N-39）。
	// 熔断打开后，路由层跳过该候选时用它还原"因何而熔断"，避免最终错误只剩
	// "circuit open" 而丢失真实上游失败（连接超时/5xx/鉴权失败等），便于排障。
	lastFailureReason string

	config CircuitBreakerConfig
}

// NewCircuitBreaker 创建熔断器。
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{config: config}
}

// UpdateConfig 热更新配置，不重置状态。
func (cb *CircuitBreaker) UpdateConfig(config CircuitBreakerConfig) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.config = config
}

// IsAvailable 判断 provider 是否「可被纳入候选链路」。**只读，无副作用**（N-40）：
// 不占用 HalfOpen 探测名额，也不触发 Open→HalfOpen 状态转换。仅用于路由选择阶段
// 的可用性排序；真正发起请求前仍需 AllowRequest 获取探测名额并完成状态转换。
//
// N-40：旧实现会在此处对 Open 且已超时的熔断器调 transitionToHalfOpenLocked，
// 把状态转换的副作用泄漏进"排序"这一读路径——多个候选排序或并发请求排序时会
// 提前/重复触发转换（transitionToHalfOpen 会把 halfOpenInFlight 清零），破坏
// HalfOpen 单探测限流。现在转换只发生在 AllowRequest（唯一发起点），排序纯读。
func (cb *CircuitBreaker) IsAvailable() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.isRecoverableLocked()
}

// isRecoverableLocked 只读判断熔断器是否会放行流量：Closed/HalfOpen 恒可；
// Open 仅当已过恢复超时点视为"可探测"。**不改任何状态**。调用方持锁。
func (cb *CircuitBreaker) isRecoverableLocked() bool {
	switch cb.state {
	case CircuitClosed, CircuitHalfOpen:
		return true
	case CircuitOpen:
		return cb.shouldRecoverLocked()
	}
	return false
}

// shouldRecoverLocked 判断 Open 状态是否已到超时恢复点。调用方持锁。
func (cb *CircuitBreaker) shouldRecoverLocked() bool {
	if cb.state != CircuitOpen {
		return false
	}
	if cb.lastOpenedAt.IsZero() {
		return true
	}
	return time.Since(cb.lastOpenedAt) >= time.Duration(cb.config.TimeoutSeconds)*time.Second
}

// AllowRequest 检查是否允许请求通过，必要时获取 HalfOpen 探测名额。
func (cb *CircuitBreaker) AllowRequest() AllowResult {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return AllowResult{Allowed: true, UsedHalfOpenPermit: false}
	case CircuitOpen:
		if cb.shouldRecoverLocked() {
			cb.transitionToHalfOpenLocked()
			// 转换后按当前状态决定。
			switch cb.state {
			case CircuitClosed:
				return AllowResult{Allowed: true, UsedHalfOpenPermit: false}
			case CircuitHalfOpen:
				return cb.allowHalfOpenProbeLocked()
			default:
				return AllowResult{Allowed: false, UsedHalfOpenPermit: false}
			}
		}
		return AllowResult{Allowed: false, UsedHalfOpenPermit: false}
	case CircuitHalfOpen:
		return cb.allowHalfOpenProbeLocked()
	}
	return AllowResult{Allowed: false, UsedHalfOpenPermit: false}
}

// allowHalfOpenProbeLocked 半开状态限流：只放行 1 个探测请求。调用方持锁。
func (cb *CircuitBreaker) allowHalfOpenProbeLocked() AllowResult {
	const maxHalfOpenRequests = 1
	if cb.halfOpenInFlight < maxHalfOpenRequests {
		cb.halfOpenInFlight++
		return AllowResult{Allowed: true, UsedHalfOpenPermit: true}
	}
	return AllowResult{Allowed: false, UsedHalfOpenPermit: false}
}

// RecordSuccess 上报一次成功。usedHalfOpenPermit 须来自 AllowRequest 返回值。
func (cb *CircuitBreaker) RecordSuccess(usedHalfOpenPermit bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if usedHalfOpenPermit {
		cb.releaseHalfOpenPermitLocked()
	}
	cb.consecutiveFailures = 0
	cb.totalRequests++
	// N-39：成功即清除陈旧失败原因，避免后续偶发跳过时展示过期成因。
	cb.lastFailureReason = ""

	if cb.state == CircuitHalfOpen {
		cb.consecutiveSuccesses++
		if cb.consecutiveSuccesses >= cb.config.SuccessThreshold {
			cb.transitionToClosedLocked()
		}
	}
}

// RecordFailure 上报一次失败。usedHalfOpenPermit 须来自 AllowRequest 返回值。
func (cb *CircuitBreaker) RecordFailure(usedHalfOpenPermit bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if usedHalfOpenPermit {
		cb.releaseHalfOpenPermitLocked()
	}
	cb.consecutiveFailures++
	cb.totalRequests++
	cb.failedRequests++
	cb.consecutiveSuccesses = 0

	switch cb.state {
	case CircuitHalfOpen:
		// 探测失败，立即回到 Open。
		cb.transitionToOpenLocked()
	case CircuitClosed:
		if cb.consecutiveFailures >= cb.config.FailureThreshold {
			cb.transitionToOpenLocked()
		} else if cb.totalRequests >= cb.config.MinRequests {
			errorRate := float64(cb.failedRequests) / float64(cb.totalRequests)
			if errorRate >= cb.config.ErrorRateThreshold {
				cb.transitionToOpenLocked()
			}
		}
	}
}

// NoteFailureReason 记录最近一次失败的真实原因（N-39）。与 RecordFailure 分开，
// 避免改动 channelBreaker 接口既有签名；由路由层在 RecordFailure 前后调用，
// 供后续请求跳过已熔断候选时还原真实失败原因。空字符串不覆盖既有原因。
func (cb *CircuitBreaker) NoteFailureReason(reason string) {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return
	}
	cb.mu.Lock()
	cb.lastFailureReason = trimmed
	cb.mu.Unlock()
}

// LastFailureReason 返回最近一次记录的失败原因（N-39），无则空串。
func (cb *CircuitBreaker) LastFailureReason() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.lastFailureReason
}

// releaseHalfOpenPermitLocked 释放 HalfOpen 探测名额，不影响健康统计。调用方持锁。
func (cb *CircuitBreaker) releaseHalfOpenPermitLocked() {
	if cb.halfOpenInFlight > 0 {
		cb.halfOpenInFlight--
	}
}

// transitionToOpenLocked 转入 Open：记打开时间、清零连续计数。调用方持锁。
func (cb *CircuitBreaker) transitionToOpenLocked() {
	cb.state = CircuitOpen
	cb.lastOpenedAt = time.Now()
	cb.consecutiveFailures = 0
	cb.consecutiveSuccesses = 0
}

// transitionToHalfOpenLocked 仅从 Open 转入 HalfOpen。调用方持锁。
func (cb *CircuitBreaker) transitionToHalfOpenLocked() {
	if cb.state != CircuitOpen {
		return
	}
	cb.state = CircuitHalfOpen
	cb.consecutiveSuccesses = 0
	cb.halfOpenInFlight = 0
}

// transitionToClosedLocked 转入 Closed：清零所有计数。调用方持锁。
func (cb *CircuitBreaker) transitionToClosedLocked() {
	cb.state = CircuitClosed
	cb.consecutiveFailures = 0
	cb.consecutiveSuccesses = 0
	cb.totalRequests = 0
	cb.failedRequests = 0
}

// State 返回当前状态（用于 UI/诊断）。
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// Stats 返回统计快照（用于 UI/诊断）。
type CircuitBreakerStats struct {
	State               CircuitState `json:"state"`
	ConsecutiveFailures uint32       `json:"consecutiveFailures"`
	ConsecutiveSuccesses uint32      `json:"consecutiveSuccesses"`
	TotalRequests       uint32       `json:"totalRequests"`
	FailedRequests      uint32       `json:"failedRequests"`
}

func (cb *CircuitBreaker) Stats() CircuitBreakerStats {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return CircuitBreakerStats{
		State:                cb.state,
		ConsecutiveFailures:  cb.consecutiveFailures,
		ConsecutiveSuccesses: cb.consecutiveSuccesses,
		TotalRequests:        cb.totalRequests,
		FailedRequests:       cb.failedRequests,
	}
}

// Reset 手动重置到 Closed（用于 UI 手动恢复）。
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.transitionToClosedLocked()
}

// CircuitBreakerRegistry 是 per-adapter 熔断器注册表。
// 用 adapter id 作 key（一个 adapter 一个熔断器）。并发安全。
type CircuitBreakerRegistry struct {
	mu       sync.Mutex
	breakers map[string]*CircuitBreaker
	config   CircuitBreakerConfig
}

// NewCircuitBreakerRegistry 创建注册表，所有新建熔断器共享该 config（可热更新）。
func NewCircuitBreakerRegistry(config CircuitBreakerConfig) *CircuitBreakerRegistry {
	return &CircuitBreakerRegistry{
		breakers: make(map[string]*CircuitBreaker),
		config:   config,
	}
}

// Get 取或创建指定 adapter 的熔断器。
func (r *CircuitBreakerRegistry) Get(adapterID string) *CircuitBreaker {
	key := normalizeAdapterKey(adapterID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if cb, ok := r.breakers[key]; ok {
		return cb
	}
	cb := NewCircuitBreaker(r.config)
	r.breakers[key] = cb
	return cb
}

// UpdateConfig 热更新全部熔断器的配置。
func (r *CircuitBreakerRegistry) UpdateConfig(config CircuitBreakerConfig) {
	r.mu.Lock()
	r.config = config
	breakers := make([]*CircuitBreaker, 0, len(r.breakers))
	for _, cb := range r.breakers {
		breakers = append(breakers, cb)
	}
	r.mu.Unlock()
	for _, cb := range breakers {
		cb.UpdateConfig(config)
	}
}

// Stats 返回全部熔断器的统计快照（用于 UI）。
func (r *CircuitBreakerRegistry) Stats() map[string]CircuitBreakerStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]CircuitBreakerStats, len(r.breakers))
	for id, cb := range r.breakers {
		out[id] = cb.Stats()
	}
	return out
}

func normalizeAdapterKey(adapterID string) string {
	id := []byte(adapterID)
	for i := range id {
		if id[i] >= 'A' && id[i] <= 'Z' {
			id[i] += 'a' - 'A'
		}
	}
	return string(id)
}
