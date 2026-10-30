//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd

package main

import (
	"errors"
	"os"
)

// Do not silently allow simultaneous orphan cleanup on platforms without
// a tested operating-system lock. Add a native lock and platform CI before
// enabling shared cache recovery there.
func tryLockCacheFile(_ *os.File) error {
	return errors.New("exclusive cache lock unsupported on this operating system")
}
