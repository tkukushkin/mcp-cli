//go:build unix

package main

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes the same advisory lock codex takes on its credential stores. It blocks until
// the lock is free; closing the file releases it.
func lockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

// tryLockFile takes that lock without blocking, reporting false when another process holds it.
// The caller decides how long to keep trying, so a wedged holder cannot stall a run forever.
func tryLockFile(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}
