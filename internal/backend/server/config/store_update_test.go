package config

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestStoreUpdateAtomicConcurrent 验证 F-03：并发 Update 改不同字段互不覆盖。
//
// 此前 Load 和 Save 各自加锁，完整读改写不在同一临界区——并发改不同字段时
// 后写者基于陈旧基线覆盖先写者。Update 在同一 store.mu 临界区内 Load-Modify-Save，
// 两个并发事务各改一个字段，最终两个字段都应保留。
func TestStoreUpdateAtomicConcurrent(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()

	// 初始写入基线。
	if _, err := store.Save(ctx, DefaultConfig()); err != nil {
		t.Fatal(err)
	}

	const goroutines = 2
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)

	// 事务 A：设 Log=true
	go func() {
		defer wg.Done()
		_, err := store.Update(ctx, func(cfg *Config) error {
			cfg.Log = true
			return nil
		})
		errs <- err
	}()
	// 事务 B：设 LastAgentModelHash="hash-B"
	go func() {
		defer wg.Done()
		_, err := store.Update(ctx, func(cfg *Config) error {
			cfg.LastAgentModelHash = "hash-B"
			return nil
		})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Update returned error: %v", err)
		}
	}

	final, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !final.Log {
		t.Error("F-03 FAIL: Log was overwritten by concurrent Update (expected true)")
	}
	if final.LastAgentModelHash != "hash-B" {
		t.Errorf("F-03 FAIL: LastAgentModelHash lost (got %q, want hash-B)", final.LastAgentModelHash)
	}
}

// TestStoreUpdateMutatorErrorRollsBack 验证 mutator 返回 error 时不写回。
func TestStoreUpdateMutatorErrorRollsBack(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()
	if _, err := store.Save(ctx, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	sentinelErr := fmt.Errorf("intentional abort")
	// mutator 改了字段但返回 error——必须回滚，磁盘值不变。
	_, err := store.Update(ctx, func(cfg *Config) error {
		cfg.Log = true
		return sentinelErr
	})
	if err != sentinelErr {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	final, _ := store.Load(ctx)
	if final.Log {
		t.Error("mutator error should have rolled back, but Log was persisted")
	}
}

// TestStoreUpdatePreservesUnrelatedFields 验证 Update 以磁盘最新为基线，
// 只改 mutator 动的字段——这正是 F-02 后端 merge 的基础。
func TestStoreUpdatePreservesUnrelatedFields(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()

	// 先用 Save 写入 Log=true, hash="pre-existing"
	cfg := DefaultConfig()
	cfg.Log = true
	cfg.LastAgentModelHash = "pre-existing"
	if _, err := store.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	// Update 只改 Log，不应动 LastAgentModelHash（模拟前端整包保存改走后端 merge）。
	if _, err := store.Update(ctx, func(cfg *Config) error {
		cfg.Log = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	final, _ := store.Load(ctx)
	if final.Log {
		t.Error("expected Log=false after Update")
	}
	if final.LastAgentModelHash != "pre-existing" {
		t.Errorf("F-02 merge base: unrelated field lost (got %q, want pre-existing)",
			final.LastAgentModelHash)
	}
}

// TestManagerSaveLastAgentModelHashConcurrentWithUpdate 验证改用 Update 后，
// 两个 Update 路径并发不互覆盖（F-03 的承诺）。
//
// 注意：manager.Save（前端整包保存）仍是非原子的整包替换语义，与 Update 并发时
// 仍可能覆盖——那是 F-02（前端整包丢字段）的范畴，需前端改走 patch 或后端 merge，
// 不在本测试承诺内。本测试只验证两个 Update 事务之间的原子性。
func TestManagerSaveLastAgentModelHashConcurrentWithUpdate(t *testing.T) {
	store := NewStore(t.TempDir()+"/config.yaml", "")
	ctx := context.Background()
	if _, err := store.Save(ctx, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ctx, store)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	// A：Update 路径保存 hash
	go func() {
		defer wg.Done()
		_ = manager.SaveLastAgentModelHash(ctx, "hash-from-A")
	}()
	// B：另一 Update 路径改 Log
	go func() {
		defer wg.Done()
		_, _ = manager.Update(ctx, func(cfg *Config) error {
			cfg.Log = true
			return nil
		})
	}()
	wg.Wait()

	final := manager.Current()
	if final.LastAgentModelHash != "hash-from-A" {
		t.Errorf("F-03 FAIL: hash lost under concurrent Update (got %q)", final.LastAgentModelHash)
	}
	if !final.Log {
		t.Errorf("F-03 FAIL: Log lost under concurrent Update")
	}
}
