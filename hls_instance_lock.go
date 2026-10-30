package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// acquireHLSCacheOwnership must precede startup orphan cleanup and HTTP
// admission. The lock file is deliberately outside owned playback IDs, so
// orphan cleanup cannot delete it. Closing the returned handle releases the
// lock, including after SIGKILL when the operating system closes descriptors.
func acquireHLSCacheOwnership(root string) (io.Closer, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(root, 0700); err != nil {
			return nil, fmt.Errorf("create HLS cache root: %w", err)
		}
		info, err = os.Lstat(root)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect HLS cache root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("HLS cache ownership requires a real directory (symlinks forbidden)")
	}
	path := filepath.Join(root, ".byteplay-instance.lock")
	existing, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect HLS ownership lock: %w", err)
	}
	if err == nil && !existing.Mode().IsRegular() {
		return nil, fmt.Errorf("HLS ownership lock must be a regular file")
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("open HLS ownership lock: %w", err)
	}
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("HLS ownership lock is not a regular file: %v", err)
	}
	if err := tryLockCacheFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("HLS cache ownership refused: %w", err)
	}
	return file, nil
}
