package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func reservationFixture(t *testing.T, budget, perJob int64) *hlsManager {
	t.Helper()
	root := t.TempDir()
	m := newHLSManager(filepath.Join(root, "hls-cache"), "unused")
	m.torrentRoot = filepath.Join(root, "torrent-data")
	m.cacheLimit = budget
	m.reserveBytes = perJob
	if err := os.MkdirAll(m.root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.torrentRoot, 0700); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestHLSConcurrentReservationsNeverOversubscribe(t *testing.T) {
	m := reservationFixture(t, 256, 128)
	var successful atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dir := hlsGenerationDir(m.root, strings.Repeat("a", 31)+string(rune('a'+i)), 1)
			if m.reserveCache(dir) {
				successful.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := successful.Load(); got != 2 {
		t.Fatalf("32 competing HLS jobs admitted %d reservations, want exactly 2", got)
	}
	if !m.cacheWithinBudget() {
		t.Fatal("two 128-byte reservations should exactly fit a 256-byte cache")
	}
	m.cacheMu.Lock()
	for dir := range m.reservations {
		delete(m.reservations, dir)
	}
	m.cacheMu.Unlock()
	if !m.reserveCache(filepath.Join(m.root, "replacement")) {
		t.Fatal("released reservation capacity was not reusable")
	}
}

func TestHLSReservationDoesNotDoubleCountWrittenSegments(t *testing.T) {
	m := reservationFixture(t, 160, 128)
	dir := hlsGenerationDir(m.root, strings.Repeat("b", 32), 1)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if !m.reserveCache(dir) {
		t.Fatal("reservation failed")
	}
	if err := os.WriteFile(filepath.Join(dir, "segment-000000.ts"), make([]byte, 80), 0600); err != nil {
		t.Fatal(err)
	}
	if !m.cacheWithinBudget() {
		t.Fatal("80 bytes already included inside the 128-byte reservation")
	}
	if err := os.WriteFile(filepath.Join(dir, "segment-000000.ts"), make([]byte, 161), 0600); err != nil {
		t.Fatal(err)
	}
	if m.cacheWithinBudget() {
		t.Fatal("output grew beyond reserved headroom without detecting overflow")
	}
	m.releaseCache(dir)
	if m.cacheWithinBudget() {
		t.Fatal("releasing reservation may not erase actual 161-byte cache usage")
	}
}

func TestHLSReserveRejectsPersistentTorrentGrowth(t *testing.T) {
	m := reservationFixture(t, 200, 128)
	path := filepath.Join(m.torrentRoot, "source.bin")
	if err := os.WriteFile(path, make([]byte, 90), 0600); err != nil {
		t.Fatal(err)
	}
	if m.reserveCache(filepath.Join(m.root, "first")) {
		t.Fatal("accepted reservation beyond torrent+HLS budget")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !m.reserveCache(filepath.Join(m.root, "first")) {
		t.Fatal("released torrent storage should make one worker admissible")
	}
	if m.reserveCache(filepath.Join(m.root, "second")) {
		t.Fatal("two reservations exceeded the cache budget")
	}
}

func TestHLSStartupRecoveryOnlyRemovesOwnedPlaybackDirectories(t *testing.T) {
	m := reservationFixture(t, 1024, 128)
	id := strings.Repeat("c", 32)
	orphan := hlsGenerationDir(m.root, id, 4)
	if err := os.MkdirAll(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "segment-000000.ts"), []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(m.root, "operator-notes")
	if err := os.MkdirAll(unrelated, 0700); err != nil {
		t.Fatal(err)
	}
	preserved := filepath.Join(unrelated, "readme.txt")
	if err := os.WriteFile(preserved, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "untouched")
	if err := os.WriteFile(external, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(m.root, strings.Repeat("d", 32))
	if err := os.Symlink(external, symlink); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := m.recoverAbandonedCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.root, id)); !os.IsNotExist(err) {
		t.Fatalf("orphaned generation remains: %v", err)
	}
	if got, err := os.ReadFile(preserved); err != nil || string(got) != "keep" {
		t.Fatalf("unowned content was lost: %q %v", got, err)
	}
	if got, err := os.ReadFile(external); err != nil || string(got) != "outside" {
		t.Fatalf("external target was touched: %q %v", got, err)
	}
	if _, err := os.Lstat(symlink); err != nil {
		t.Fatalf("unknown symlink was modified: %v", err)
	}
	if err := m.recoverAbandonedCache(); err != nil {
		t.Fatalf("recovery must be repeatable: %v", err)
	}
}

func TestHLSStartupRecoveryFailsClosedForSymlinkedRootOrActiveJobs(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	m := newHLSManager(filepath.Join(root, "hls-cache"), "unused")
	if err := os.Symlink(outside, m.root); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := m.recoverAbandonedCache(); err == nil {
		t.Fatal("followed symlinked HLS cache root")
	}
	if err := os.Remove(m.root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.root, 0700); err != nil {
		t.Fatal(err)
	}
	m.jobs[strings.Repeat("e", 32)] = &hlsPlayback{}
	if err := m.recoverAbandonedCache(); err == nil {
		t.Fatal("startup recovery allowed deletion while jobs were registered")
	}
}
