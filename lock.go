package main

// Cross-process locks: each tinybk invocation is a separate process, so an
// in-process mutex is useless; use file locks.
//
// Two levels:
//   - target lock: serialize actions on one tab. Writers take EX, readers take SH.
//     The action and its observation stay under the same lock so another tinybk
//     cannot change the page halfway through.
//   - browser lock: serialize tab lifecycle operations (tab new / tab close).
//     tab close always takes target -> browser. Under the browser lock it re-lists
//     tabs and checks for the last tab, so two concurrent closes cannot reduce the
//     page count to zero.
//
// Deadlock analysis: actions take only target; tab new takes only browser; only
// tab close takes both, always target -> browser. No cycle exists.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type lockMode int

const (
	lockNone lockMode = iota
	lockShared
	lockExclusive
)

type fileLock struct {
	f *os.File
}

func (l *fileLock) Release() {
	if l != nil && l.f != nil {
		l.f.Close()
	}
}

// lockBusy reports a lock wait timeout: tell the caller another tinybk is operating
// instead of pretending success or waiting forever.
func lockBusy(kind, label string, timeout time.Duration) *cmdError {
	ms := timeout.Milliseconds()
	kv := map[string]any{"lock": kind, "timeout_ms": ms}
	if kind == "tab" {
		kv["short"] = shortID(label)
		kv["target_id"] = label
		return fail(codeLockBusy,
			fmt.Sprintf("tab %s is busy in another tinybk process (lock wait %dms); retry later or increase --lock-timeout", shortID(label), ms),
			kv)
	}
	return fail(codeLockBusy,
		fmt.Sprintf("browser tab lifecycle is busy in another tinybk process (lock wait %dms); retry later or increase --lock-timeout", ms),
		kv)
}

func lockFilePath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(os.TempDir(), "tinybk-"+hex.EncodeToString(sum[:8])+".lock")
}

func acquireLock(key, kind, label string, mode lockMode, timeout time.Duration) (*fileLock, error) {
	if mode == lockNone {
		return nil, nil
	}
	path := lockFilePath(key)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create lock file %s: %w", path, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := flockFile(f, mode == lockExclusive)
		if err == nil {
			return &fileLock{f: f}, nil
		}
		if !lockWouldBlock(err) {
			f.Close()
			return nil, fmt.Errorf("acquire lock %s: %w", path, err)
		}
		if timeout <= 0 || time.Now().After(deadline) {
			f.Close()
			return nil, lockBusy(kind, label, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// acquireTargetLock locks one tab. Target ids are unique, so use the id directly;
// this works whether --cdp uses 127.0.0.1 or localhost.
func acquireTargetLock(targetID string, mode lockMode, timeout time.Duration) (*fileLock, error) {
	return acquireLock("tab|"+targetID, "tab", targetID, mode, timeout)
}

// acquireBrowserLock locks tab lifecycle for one browser. The key is the --cdp string
// with its trailing slash removed; all agents must use the same spelling for one browser.
func acquireBrowserLock(cdpURL string, timeout time.Duration) (*fileLock, error) {
	key := "browser|" + strings.TrimRight(cdpURL, "/")
	return acquireLock(key, "browser", cdpURL, lockExclusive, timeout)
}
