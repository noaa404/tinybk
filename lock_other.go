//go:build !unix

package main

// Non-Unix platforms have no flock, so cross-process mutual exclusion cannot be guaranteed.
// Fail closed: every command that needs a lock reports an error instead of running concurrently.

import "os"

var errLockUnsupported = fail(codeLockUnsupported, "cross-process locks are not supported on this platform; refusing unsafe tab concurrency", nil)

func flockFile(*os.File, bool) error { return errLockUnsupported }

func lockWouldBlock(error) bool { return false }
