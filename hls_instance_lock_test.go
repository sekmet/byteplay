package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHLSOwnershipLockExcludesSecondInstance(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hls-cache")
	first, err := acquireHLSCacheOwnership(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := acquireHLSCacheOwnership(root); err == nil {
		second.Close()
		t.Fatal("second process acquired an already-owned HLS cache")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := acquireHLSCacheOwnership(root)
	if err != nil {
		t.Fatalf("cache ownership not recoverable after process exit: %v", err)
	}
	defer again.Close()
	if _, err := os.Stat(filepath.Join(root, ".byteplay-instance.lock")); err != nil {
		t.Fatal("owner lock file unexpectedly removed")
	}
}

func TestHLSOwnershipRejectsSymlinkedRootAndLock(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	symlink := filepath.Join(root, "linked")
	if err := os.Symlink(target, symlink); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if owner, err := acquireHLSCacheOwnership(symlink); err == nil {
		owner.Close()
		t.Fatal("accepted symlink as HLS cache root")
	}
	realRoot := filepath.Join(root, "hls-cache")
	if err := os.MkdirAll(realRoot, 0700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(realRoot, ".byteplay-instance.lock")
	if err := os.Symlink(filepath.Join(target, "lock"), lock); err != nil {
		t.Fatal(err)
	}
	if owner, err := acquireHLSCacheOwnership(realRoot); err == nil {
		owner.Close()
		t.Fatal("accepted symlinked HLS ownership lock")
	}
}

func TestHLSOwnershipLockIsNotAnOrphanPlayback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hls-cache")
	owner, err := acquireHLSCacheOwnership(root)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	m := newHLSManager(root, "unused")
	orphan := filepath.Join(root, strings.Repeat("a", 32))
	if err := os.MkdirAll(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.recoverAbandonedCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan not reclaimed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".byteplay-instance.lock")); err != nil {
		t.Fatalf("recovery damaged the ownership file: %v", err)
	}
}
