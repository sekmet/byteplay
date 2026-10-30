//go:build linux || darwin || freebsd || netbsd || openbsd

package main

import (
	"fmt"
	"os"
	"syscall"
)

func tryLockCacheFile(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another BytePlay instance may be using the cache: %w", err)
	}
	return nil
}
