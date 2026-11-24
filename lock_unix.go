//go:build unix

package main

// Unix file-lock implementation. flock is tied to the open file descriptor and released
// automatically when the process exits or crashes, leaving no stale lock to clean up.

import (
	"errors"
	"os"
	"syscall"
)

func flockFile(f *os.File, exclusive bool) error {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	return syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
}

func lockWouldBlock(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
