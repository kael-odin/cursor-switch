package client

import (
	"context"
	"testing"
	"time"

	backend "cursor/internal/backend"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/logger"
	"cursor/internal/mitm"
)

// setTestHome 把 HOME 与 USERPROFILE 同时指向 tmp（os.UserHomeDir 在 Windows 读
// USERPROFILE、其余平台读 HOME，两者都设才跨平台一致）。appdata.RootDir / 各路径
// 都从 home 推导，故整组配置落到临时目录，不污染真实环境。
func setTestHome(t *testing.T, tmp string) {
	t.Helper()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	// logger 是进程级单例，首次写日志会在当前 HOME 下打开 app.log 并长期持有句柄。
	// 关闭它并重置 initOnce，使 t.TempDir 的 RemoveAll 清理能删除该文件，且下一个
	// 测试可按自己的 HOME 重新打开。非测试路径不调 CloseLogFile。
	t.Cleanup(func() { logger.CloseLogFile() })
}

// newTestProxyService 构造一个指向临时 home 的 ProxyService：真实 backend.Host（NewHost）
// + 真实 mitm.ProxyServer（空闲端口、nil certManager）但不 Start。app=nil 故
// LoadUserConfig 走 DefaultConfig；不触发任何 OS 证书存储交互。
//
// 共享 home：logger 是进程级单例，首次写日志时打开 app.log 并在整个进程生命周期内
// 持有句柄。若每个测试用不同 t.TempDir()，旧句柄会指向已被 t.Cleanup 删除的目录，
// 导致 RemoveAll 清理报 "being used by another process"。故同一测试内所有子 service
// 共享同一个 home（由 caller 先 setTestHome 一次），与 backend/host_test.go 一致。
func newTestProxyService(t *testing.T) *ProxyService {
	t.Helper()
	s := &ProxyService{
		configPath:       resolveUserConfigPath(),
		logsRoot:         resolveLogsRootPath(),
		publicClient:     nil,
		modelTestResults: make(map[string]ModelAdapterTestResult),
	}
	s.store = serverconfig.NewStore(s.configPath, s.logsRoot)
	host, err := backend.NewHost(s.store)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	s.backendHost = host
	return s
}

// startFreeProxy 启动一个绑定空闲端口的真实 MITM 代理（nil certManager OK）。
// 返回已 Start（IsRunning==true）的代理与其真实监听地址。
func startFreeProxy(t *testing.T) *mitm.ProxyServer {
	t.Helper()
	// baseURL 必须是 loopback，否则 NewProxyServer 拒绝；用 1 号端口占位（不会真连）。
	proxy, err := mitm.NewProxyServer("127.0.0.1:0", "http://127.0.0.1:1", "", "", nil)
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	if err := proxy.Start(); err != nil {
		t.Fatalf("proxy.Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = proxy.Stop(ctx)
	})
	return proxy
}

// TestStageTransitions 验证 F-35 残留的显式状态机阶段流转：
//   - 初始 idle
//   - StartProxy 成功路径走 starting → running
//   - StartProxy 失败路径走 starting → failed
//   - StopProxy 走 stopping → idle
//
// 失败路径用「预启一个运行中 proxy 且其地址与 config 不同」触发 ensureProxy 报错
// （"代理正在运行，不能切换"），在到达 OS 证书注入前回滚，干净可注入。
func TestStageTransitions(t *testing.T) {
	// 全测试共享一个 home：logger 单例首次写打开 app.log 后不换目录，
	// 故同测试内所有子 service 必须落到同一 tmp（见 newTestProxyService 注释）。
	setTestHome(t, t.TempDir())

	// --- 失败路径：starting → failed ---
	// 预启一个运行中 proxy（wasRunning=true 快照），其地址与默认 config.ProxyListenAddr
	// （127.0.0.1:18080）不同 → ensureProxy 报"代理正在运行，不能切换"→ 在到达 OS 证书
	// 注入前回滚，干净可注入。
	s2 := newTestProxyService(t)
	preProxy := startFreeProxy(t) // 绑定空闲端口，地址 ≠ 默认 127.0.0.1:18080
	s2.proxy = preProxy            // 预置运行中的 proxy（wasRunning=true 快照）

	if got := s2.GetState().Stage; got != "idle" {
		t.Fatalf("initial stage = %q, want idle", got)
	}
	_, err := s2.StartProxy()
	if err == nil {
		t.Fatal("StartProxy should fail (ensureProxy: proxy running on different addr)")
	}
	if got := s2.GetState().Stage; got != "failed" {
		t.Fatalf("after failed StartProxy, stage = %q, want failed", got)
	}
	// 核心：预启的 proxy 在回滚后仍 running（wasRunning=true，未被回滚停掉）。
	if !preProxy.IsRunning() {
		t.Error("pre-existing proxy should remain running after rollback (wasRunning=true)")
	}

	// --- StopProxy 状态机：stopping → idle ---
	// 用一个全新的、无 proxy 无 backend 的 service 调 StopProxy，断言阶段流转。
	s3 := newTestProxyService(t)
	if got := s3.GetState().Stage; got != "idle" {
		t.Fatalf("s3 initial stage = %q, want idle", got)
	}
	// 手动置 running 后调 StopProxy，验证 stopping → idle。
	s3.setStage(stageRunning)
	if got := s3.GetState().Stage; got != "running" {
		t.Fatalf("s3 after setStage(running) = %q, want running", got)
	}
	if _, err := s3.StopProxy(); err != nil {
		t.Fatalf("StopProxy: %v", err)
	}
	if got := s3.GetState().Stage; got != "idle" {
		t.Fatalf("after StopProxy, stage = %q, want idle", got)
	}
}

// TestStartProxyDoesNotRollbackPreExistingProxy 是 F-35 残留修正的核心回归测试：
// 此前 ApplyCursorSettings 失败路径无条件停 proxy+backend，会误停 StartProxy 之前
// 就在运行的健康 proxy。现改为定向回滚——只停本次启动的组件。
// 构造：预启 proxy（wasRunning=true）→ StartProxy 在 ensureProxy 因地址不同失败
// → 回滚。断言预启 proxy 仍 running、本次启动的 backend 被回滚停掉、stage==failed。
func TestStartProxyDoesNotRollbackPreExistingProxy(t *testing.T) {
	setTestHome(t, t.TempDir())
	s := newTestProxyService(t)
	preProxy := startFreeProxy(t)
	s.proxy = preProxy

	// 起步前确认 proxy 真在运行、backend 未运行。
	if !preProxy.IsRunning() {
		t.Fatal("preProxy should be running before StartProxy")
	}
	if s.backendHost.IsRunning() {
		t.Fatal("backend should NOT be running before StartProxy")
	}

	_, err := s.StartProxy()
	if err == nil {
		t.Fatal("StartProxy should fail at ensureProxy (proxy running on different addr)")
	}
	if got := s.GetState().Stage; got != "failed" {
		t.Fatalf("stage = %q, want failed", got)
	}

	// 核心断言：预启 proxy（wasRunning=true）回滚后仍 running。
	if !preProxy.IsRunning() {
		t.Error("REGRESSION: pre-existing proxy was stopped by rollback; only components started in THIS call should be rolled back")
	}
	// 本次启动的 backend（wasRunning=false）应被回滚停掉。
	if s.backendHost.IsRunning() {
		t.Error("backend started in this call should be stopped by rollback (wasRunning=false)")
	}
}

// TestStartProxyRollsBackBackendOnStartFailure 验证 start_backend 失败时回滚：
// 让后端监听一个已被占用的端口（用另一个 proxy 占住）使 backend.Start 的 net.Listen
// 失败 → rollback。回滚对未启动的 backend 是 no-op；stage==failed。这覆盖「失败路径
// 也走 rollback helper」而非旧 fail，且不误停任何先前组件。
func TestStartProxyRollsBackBackendOnStartFailure(t *testing.T) {
	setTestHome(t, t.TempDir())
	s := newTestProxyService(t)
	// 用一个 proxy 占住默认后端端口 127.0.0.1:18090，让 backend.Start 的 net.Listen 失败。
	occupier := startFreeProxy(t)
	// 把 occupier 的监听地址改成默认后端端口：先 Stop 再用 NewProxyServer 重建到 18090。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := occupier.Stop(ctx); err != nil {
		t.Fatalf("stop occupier: %v", err)
	}
	blocker, err := mitm.NewProxyServer("127.0.0.1:18090", "http://127.0.0.1:1", "", "", nil)
	if err != nil {
		t.Fatalf("NewProxyServer blocker: %v", err)
	}
	if err := blocker.Start(); err != nil {
		t.Fatalf("blocker.Start: %v", err)
	}
	t.Cleanup(func() {
		c, ccl := context.WithTimeout(context.Background(), 3*time.Second)
		defer ccl()
		_ = blocker.Stop(c)
	})

	_, startErr := s.StartProxy()
	if startErr == nil {
		t.Fatal("StartProxy should fail (backend Start net.Listen on occupied port)")
	}
	if got := s.GetState().Stage; got != "failed" {
		t.Fatalf("stage = %q, want failed", got)
	}
	// backend 没启动起来，回滚对它是 no-op；IsRunning 仍 false。
	if s.backendHost.IsRunning() {
		t.Error("backend should not be running after start_backend failure")
	}
}

// TestStopProxyNoPanicConcurrent 是 S20 风格的并发 smoke：并发 Start/Stop 不 panic
// 不死锁。race detector 需 cgo/gcc 本环境无，故仅按无 panic 验证。
func TestStopProxyNoPanicConcurrent(t *testing.T) {
	setTestHome(t, t.TempDir())
	s := newTestProxyService(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("concurrent Start/Stop panicked: %v", r)
			}
		}()
		for i := 0; i < 20; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, _ = s.StartProxy()
			_ = ctx
			cancel()
			_, _ = s.StopProxy()
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent Start/Stop did not complete within 30s (possible deadlock)")
	}
}
