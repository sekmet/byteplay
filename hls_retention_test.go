package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHLSReapInactiveCancelsAndDeletesOnlyOwnedPlayback(t *testing.T) {
	root := t.TempDir()
	m := newHLSManager(root, "unused")
	m.retentionTTL = 15 * time.Minute
	id := strings.Repeat("a", 32)
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte("#EXTM3U"), 0600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "unrelated-file")
	if err := os.WriteFile(other, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &hlsPlayback{
		id: id, dir: dir, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), state: "ready",
		lastDemand: time.Now().Add(-16 * time.Minute),
	}
	go func() { <-ctx.Done(); close(job.done) }()
	m.jobs[id] = job
	if n := m.reapInactive(time.Now()); n != 1 {
		t.Fatalf("reaped %d, expected 1", n)
	}
	if m.get(id) != nil {
		t.Fatal("stale playback still occupies admission slot")
	}
	select {
	case <-job.done:
	case <-time.After(time.Second):
		t.Fatal("reaper did not cancel owned worker")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("HLS owned directory not reclaimed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unrelated cache file deleted: %v", err)
	}
}

func TestHLSReapRetainsPlaybackWithRecentRealDemand(t *testing.T) {
	m := newHLSManager(t.TempDir(), "unused")
	m.retentionTTL = 15 * time.Minute
	id := strings.Repeat("b", 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j := &hlsPlayback{id: id, state: "ready", ctx: ctx, cancel: cancel,
		done: make(chan struct{}), lastDemand: time.Now().Add(-5 * time.Minute)}
	m.jobs[id] = j
	if n := m.reapInactive(time.Now()); n != 0 {
		t.Fatalf("reaped active playback: %d", n)
	}
	if m.get(id) != j || ctx.Err() != nil {
		t.Fatal("recent playback was evicted")
	}
}

func TestHLSCombinedCacheBudgetIncludesTorrentAndHLS(t *testing.T) {
	dir := t.TempDir()
	m := newHLSManager(filepath.Join(dir, "hls-cache"), "unused")
	m.torrentRoot = filepath.Join(dir, "torrent-data")
	m.cacheLimit = 2048
	for _, p := range []string{m.root, m.torrentRoot} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(m.torrentRoot, "source.bin"), make([]byte, 1536), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.root, "segment.ts"), make([]byte, 1024), 0600); err != nil {
		t.Fatal(err)
	}
	if m.cacheWithinBudget() {
		t.Fatal("HLS-only accounting ignored torrent usage")
	}
	if err := os.Remove(filepath.Join(m.root, "segment.ts")); err != nil {
		t.Fatal(err)
	}
	if !m.cacheWithinBudget() {
		t.Fatal("cache budget did not free after HLS removal")
	}
}

func TestHLSCacheLimitValidation(t *testing.T) {
	for _, tc := range []struct {
		input string
		valid bool
		value int64
	}{
		{"", true, maxHLSCacheBytes}, {"1024", true, 1024},
		{"0", false, 0}, {"-1", false, 0}, {"abc", false, 0},
	} {
		value, err := parseHLSCacheLimit(tc.input)
		if (err == nil) != tc.valid {
			t.Fatalf("%q valid=%v err=%v", tc.input, tc.valid, err)
		}
		if tc.valid && value != tc.value {
			t.Fatalf("%q: %d expected %d", tc.input, value, tc.value)
		}
	}
}

func TestHLSCacheFailureSurvivesCancellation(t *testing.T) {
	j := &hlsPlayback{state: "ready"}
	j.setError("failed", "cache_full", "HLS cache budget reached")
	j.setError("cancelled", "cancelled", "Playback cancelled")
	s := j.snapshot()
	if s.State != "failed" || s.Error == nil || s.Error.Code != "cache_full" {
		t.Fatalf("cache failure was overwritten by cancellation: %+v", s)
	}
}

func TestHLSGenerationPathsAreDisjoint(t *testing.T) {
	root := t.TempDir()
	id := strings.Repeat("f", 32)
	first := hlsGenerationDir(root, id, 1)
	second := hlsGenerationDir(root, id, 2)
	if err := os.MkdirAll(second, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(first, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "segment-000000.ts"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(second, "segment-000000.ts")); err != nil || string(got) != "new" {
		t.Fatalf("cleanup of generation 1 deleted generation 2: %q, %v", got, err)
	}
}

func TestTorrentProjectionResetAfterSuccessfulCachePurge(t *testing.T) {
	m := reservationFixture(t, 128, 32)
	dir := filepath.Join(m.torrentRoot, "film.mkv.part")
	m.cacheMu.Lock()
	m.torrentFileEnds = map[string]int64{dir: 256}
	m.cacheMu.Unlock()
	if m.cacheWithinBudget() {
		t.Fatal("projected torrent file should count toward quota")
	}
	m.resetTorrentProjections()
	if !m.cacheWithinBudget() {
		t.Fatal("successful purge did not free reserved torrent file projection")
	}
}
