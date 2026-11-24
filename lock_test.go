package main

// lock_test.go - unit tests for file locks. Covers EX exclusion, SH sharing, lock_busy
// timeout, reacquire after release, and target/browser lock isolation.

import (
	"fmt"
	"testing"
	"time"
)

func TestFileLockModesAndBusy(t *testing.T) {
	id := fmt.Sprintf("LOCKTEST-%d", time.Now().UnixNano())

	ex, err := acquireTargetLock(id, lockExclusive, 0)
	if err != nil {
		t.Fatalf("首次获取 EX 锁失败: %v", err)
	}
	_, err = acquireTargetLock(id, lockExclusive, 0)
	busy := wantCode(t, err, codeLockBusy)
	if busy.kv["lock"] != "tab" {
		t.Fatalf("lock_busy 应说明锁类型，实际 %v", busy.kv)
	}
	if _, err := acquireTargetLock(id, lockShared, 0); err == nil {
		t.Fatalf("持有 EX 时 SH 也应等待")
	}
	ex.Release()

	// Reacquire works after release.
	again, err := acquireTargetLock(id, lockExclusive, 0)
	if err != nil {
		t.Fatalf("释放后应能重新获取: %v", err)
	}
	again.Release()

	// Two shared locks coexist; EX must wait.
	s1, err := acquireTargetLock(id, lockShared, 0)
	if err != nil {
		t.Fatalf("第一个 SH 失败: %v", err)
	}
	defer s1.Release()
	s2, err := acquireTargetLock(id, lockShared, 0)
	if err != nil {
		t.Fatalf("第二个 SH 应能共存: %v", err)
	}
	defer s2.Release()
	if _, err := acquireTargetLock(id, lockExclusive, 0); err == nil {
		t.Fatalf("持有 SH 时 EX 应等待")
	}

	// Target and browser locks use different keys and do not block each other.
	bl, err := acquireBrowserLock("http://lock-test/"+id, 0)
	if err != nil {
		t.Fatalf("browser 锁不应被 target 锁阻塞: %v", err)
	}
	bl.Release()

	// lockNone creates no lock and Release must be a safe no-op.
	nilLock, err := acquireTargetLock(id, lockNone, 0)
	if err != nil || nilLock != nil {
		t.Fatalf("lockNone 应返回 nil/nil，实际 %v/%v", nilLock, err)
	}
	nilLock.Release()
}
