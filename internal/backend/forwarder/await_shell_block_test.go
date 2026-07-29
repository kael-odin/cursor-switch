package forwarder

import (
	"strings"
	"testing"
	"time"
)

// TestAwaitShellBlocksUntilPatternMatched 覆盖 N-04：
// 修复前 awaitShellSnapshot 同步返回，block_until_ms 仅算 timedOut 布尔从不阻塞，
// 模型几乎总是拿到 {matched:false, timed_out:true} + 调用瞬间的部分输出。
// 修复后当 block_until_ms>0 且未匹配/未终态时进入阻塞轮询循环，直到匹配/终态/超时。
func TestAwaitShellBlocksUntilPatternMatched(t *testing.T) {
	// 场景 3：阻塞期间 shell 写入匹配内容 → 在超时前返回 matched=true。
	stream := &ActiveStream{BackgroundShells: map[string]*BackgroundShellState{
		"shell-delayed": {ShellID: "shell-delayed", Status: backgroundShellStatusRunning},
	}}
	svc := &Service{}

	// 300ms 后写入匹配 pattern 的输出，block_until_ms 给 5000ms（远大于延迟）。
	go func() {
		time.Sleep(300 * time.Millisecond)
		stream.mu.Lock()
		stream.BackgroundShells["shell-delayed"].StdoutBuffer = "BUILD SUCCESS\n"
		stream.mu.Unlock()
	}()

	start := time.Now()
	svc.awaitShellBlockUntilMatched(stream, awaitShellArgs{ShellID: "shell-delayed", Pattern: "BUILD SUCCESS"}, 5000)
	elapsed := time.Since(start)

	if elapsed >= 4000*time.Millisecond {
		t.Fatalf("did not return early on match: elapsed=%v (should be ~300ms)", elapsed)
	}
	// 阻塞循环返回后，snapshot 应读到 matched=true。
	result := svc.awaitShellSnapshot(stream, awaitShellArgs{ShellID: "shell-delayed", Pattern: "BUILD SUCCESS"})
	if !result.Matched {
		t.Errorf("expected matched=true after blocking wait, got false. status=%s", result.Status)
	}
}

// TestAwaitShellBlocksUntilTimeoutWhenNoMatch 覆盖 N-04：
// 未匹配且 shell 一直 running → 阻塞到超时，返回 timed_out=true。
func TestAwaitShellBlocksUntilTimeoutWhenNoMatch(t *testing.T) {
	stream := &ActiveStream{BackgroundShells: map[string]*BackgroundShellState{
		"shell-running": {ShellID: "shell-running", Status: backgroundShellStatusRunning, StdoutBuffer: "partial output"},
	}}
	svc := &Service{}

	// block_until_ms=200ms，未匹配未终态 → 应阻塞约 200ms 后返回。
	start := time.Now()
	svc.awaitShellBlockUntilMatched(stream, awaitShellArgs{ShellID: "shell-running", Pattern: "WILL_NEVER_MATCH"}, 200)
	elapsed := time.Since(start)

	if elapsed < 150*time.Millisecond {
		t.Fatalf("returned too early, did not block: elapsed=%v (should be ~200ms)", elapsed)
	}
	result := svc.awaitShellSnapshot(stream, awaitShellArgs{ShellID: "shell-running", Pattern: "WILL_NEVER_MATCH"})
	if !result.TimedOut {
		t.Errorf("expected timed_out=true on no match + running, got false. status=%s matched=%v", result.Status, result.Matched)
	}
}

// TestAwaitShellNoBlockWhenAlreadyMatched 覆盖 N-04：
// 已匹配时不阻塞（立即返回）。
func TestAwaitShellNoBlockWhenAlreadyMatched(t *testing.T) {
	stream := &ActiveStream{BackgroundShells: map[string]*BackgroundShellState{
		"shell-matched": {ShellID: "shell-matched", Status: backgroundShellStatusRunning, StdoutBuffer: "DONE\n"},
	}}
	svc := &Service{}

	start := time.Now()
	svc.awaitShellBlockUntilMatched(stream, awaitShellArgs{ShellID: "shell-matched", Pattern: "DONE"}, 5000)
	elapsed := time.Since(start)

	if elapsed >= 1000*time.Millisecond {
		t.Fatalf("blocked despite already-matched: elapsed=%v (should be ~0)", elapsed)
	}
}

// TestAwaitShellNoBlockWhenTerminal 覆盖 N-04：
// shell 已终态时不阻塞（立即返回）。
func TestAwaitShellNoBlockWhenTerminal(t *testing.T) {
	stream := &ActiveStream{BackgroundShells: map[string]*BackgroundShellState{
		"shell-done": {ShellID: "shell-done", Status: backgroundShellStatusCompleted},
	}}
	svc := &Service{}

	start := time.Now()
	svc.awaitShellBlockUntilMatched(stream, awaitShellArgs{ShellID: "shell-done", Pattern: strings.Repeat("NOPE", 1)}, 5000)
	elapsed := time.Since(start)

	if elapsed >= 1000*time.Millisecond {
		t.Fatalf("blocked despite terminal status: elapsed=%v (should be ~0)", elapsed)
	}
}
