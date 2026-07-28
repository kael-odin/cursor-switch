package mitm

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// newTestProxy 构造一个无 certManager 的 ProxyServer（CONNECT 走 OkConnect 直连隧道，
// 不做 MITM 解密），用于 F-14 隧道关闭测试。baseURL 需 loopback，给个不实际使用的占位。
func newTestProxy(t *testing.T) *ProxyServer {
	t.Helper()
	srv, err := NewProxyServer("127.0.0.1:0", "http://127.0.0.1:1", "", "", nil)
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})
	return srv
}

// echoTarget 是一个本地 TCP echo server，作为 CONNECT 隧道的对端。
func echoTarget(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	return ln
}

// openConnectTunnel 经代理建立一条 CONNECT 隧道到 target，返回隧道客户端侧 conn。
// 隧道建立后挂着不动（模拟 Cursor 流式长连接），返回的 conn 仍处于"已发 CONNECT、收到 200"状态。
func openConnectTunnel(t *testing.T, proxyAddr, target string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		c.Close()
		t.Fatalf("write CONNECT: %v", err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		c.Close()
		t.Fatalf("read CONNECT response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Close()
		t.Fatalf("CONNECT status=%d want 200", resp.StatusCode)
	}
	return c
}

// TestStopClosesHijackedTunnels (F-14) 验证：Stop 后仍在注册表中的 hijacked CONNECT
// 隧道被强制关闭（读写返回错误），而不是继续转发直到对端 EOF。
func TestStopClosesHijackedTunnels(t *testing.T) {
	srv := newTestProxy(t)
	target := echoTarget(t)
	defer target.Close()
	proxyAddr := srv.Snapshot().ListenAddr

	// 建两条隧道，建立后写一笔数据验证隧道活着，然后挂着。
	tunnels := make([]net.Conn, 0, 2)
	for i := 0; i < 2; i++ {
		c := openConnectTunnel(t, proxyAddr, target.Addr().String())
		tunnels = append(tunnels, c)
		// 写入触发 echo，证明隧道在工作
		if _, err := c.Write([]byte("ping")); err != nil {
			t.Fatalf("tunnel write: %v", err)
		}
		buf := make([]byte, 4)
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := c.Read(buf)
		if err != nil || n != 4 {
			c.Close()
			t.Fatalf("tunnel read before stop: n=%d err=%v", n, err)
		}
		c.SetReadDeadline(time.Time{}) // 恢复阻塞读
	}
	defer func() {
		for _, c := range tunnels {
			c.Close()
		}
	}()

	// Stop 应触发 closeAllConns，强制关闭注册表中的隧道 conn。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Stop(ctx); err != nil && err != context.DeadlineExceeded {
		// Stop 可能因 Shutdown 等待已关 conn 超时，这里隧道是被我们强关的，忽略超时。
		t.Logf("Stop returned: %v", err)
	}

	// 隧道应已被代理侧关闭——客户端 Read 应收到 EOF/重置/超时。
	for i, c := range tunnels {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 8)
		_, err := c.Read(buf)
		if err == nil {
			t.Errorf("tunnel %d: Read after Stop succeeded, expected error (tunnel not closed)", i)
		}
	}
}

// TestStopIsRunningFlipsToFalse (F-14 回归) 验证 Stop 后 IsRunning()==false。
func TestStopIsRunningFlipsToFalse(t *testing.T) {
	srv := newTestProxy(t)
	if !srv.IsRunning() {
		t.Fatalf("IsRunning should be true after Start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if srv.IsRunning() {
		t.Fatalf("IsRunning should be false after Stop")
	}
}

// TestConcurrentStartStopNoPanic (F-35) 验证并发 Start/Stop 不 panic。
// 注：lifecycleMu 在 client 层，mitm 层只有 runMu；这里测 mitm 的 runMu 串行化。
func TestConcurrentStartStopNoPanic(t *testing.T) {
	srv, err := NewProxyServer("127.0.0.1:0", "http://127.0.0.1:1", "", "", nil)
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Start()
			_ = srv.Stop(ctx)
		}()
	}
	wg.Wait()
	// 最终停掉
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Stop(ctx)
}
