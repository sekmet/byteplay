package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

const defaultHLSRetentionTTL = 15 * time.Minute

var ownedHLSID = regexp.MustCompile("^[a-f0-9]{32}$")

// All generations have disjoint, owned subdirectories. In particular,
// generation 1 must never be an ancestor of generation 2.
func hlsGenerationDir(root, id string, generation int) string {
	return filepath.Join(root, id, "g", strconv.Itoa(generation))
}

func parseHLSCacheLimit(raw string) (int64, error) {
	if raw == "" {
		return maxHLSCacheBytes, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("BITPLAY_CACHE_MAX_BYTES must be a positive integer")
	}
	return n, nil
}

// Each active generation reserves its maximum allowed additional HLS output
// before FFmpeg begins. Already written files count toward that reservation,
// never twice. The ledger serializes competing HLS admissions, but torrent
// writers are independent: a concurrent torrent increase is caught by the
// running quota check and is NOT guaranteed to be prevented in advance.
func (m *hlsManager) accountedCacheBytesLocked() int64 {
	used := dirSize(m.torrentRoot) + dirSize(m.root)
	// Writes may target sparse offsets and extend default .part files.
	// Count all committed and in-flight projected file lengths that are not
	// already represented by the current on-disk apparent size.
	for path, end := range m.torrentFileEnds {
		remaining := end - projectedPartFileSize(path)
		if remaining > 0 {
			if used > math.MaxInt64-remaining {
				return math.MaxInt64
			}
			used += remaining
		}
	}
	for dir, cap := range m.reservations {
		remaining := cap - dirSize(dir)
		if remaining > 0 {
			if used > math.MaxInt64-remaining {
				return math.MaxInt64
			}
			used += remaining
		}
	}
	return used
}

func (m *hlsManager) cacheWithinBudget() bool {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	return m.cacheConfigErr == nil && m.cacheLimit > 0 &&
		m.accountedCacheBytesLocked() <= m.cacheLimit
}

func (m *hlsManager) reserveCache(dir string) bool {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if m.cacheConfigErr != nil || m.cacheLimit <= 0 ||
		m.reserveBytes <= 0 || m.reserveBytes > maxHLSPlaybackBytes {
		return false
	}
	if m.reservations == nil {
		m.reservations = make(map[string]int64)
	}
	if _, exists := m.reservations[dir]; exists {
		return false
	}
	used := m.accountedCacheBytesLocked()
	if used > m.cacheLimit || m.reserveBytes > m.cacheLimit-used {
		return false
	}
	m.reservations[dir] = m.reserveBytes
	return true
}

func (m *hlsManager) releaseCache(dir string) {
	m.cacheMu.Lock()
	delete(m.reservations, dir)
	m.cacheMu.Unlock()
}

// This is called once, before HTTP admission; an already-running application
// must never invoke it on another process's active cache. Fail closed on
// symlinked roots or deletion errors rather than accidentally traversing data.
func (m *hlsManager) recoverAbandonedCache() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.jobs) != 0 {
		return fmt.Errorf("cannot recover HLS cache with active playback sessions")
	}
	info, err := os.Lstat(m.root)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(m.root, 0700); err != nil {
			return err
		}
		info, err = os.Lstat(m.root)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("HLS cache root must be a directory, not a symlink or file")
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !ownedHLSID.MatchString(entry.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(m.root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// A retired playback is removed from admission and cancels its owned FFmpeg
// before its directory is deleted. The manager lock excludes any generation
// switch or in-progress media ServeFile while publishing retirement.
func (m *hlsManager) reapInactive(now time.Time) int {
	m.mu.Lock()
	var retired []*hlsPlayback
	for id, job := range m.jobs {
		job.mu.Lock()
		expired := !job.lastDemand.IsZero() && now.Sub(job.lastDemand) >= m.retentionTTL
		if expired && job.pauseTimer != nil {
			job.pauseTimer.Stop()
		}
		job.mu.Unlock()
		if expired {
			delete(m.jobs, id)
			retired = append(retired, job)
		}
	}
	m.mu.Unlock()
	for _, job := range retired {
		if job.cancel != nil {
			job.cancel()
		}
		if job.done == nil {
			continue
		}
		go func(job *hlsPlayback) {
			<-job.done
			// Never touch arbitrary directories or the persistent torrent root.
			if ownedHLSID.MatchString(job.id) {
				_ = os.RemoveAll(filepath.Join(m.root, job.id))
			}
		}(job)
	}
	return len(retired)
}

func (m *hlsManager) sweepLoop(ctx context.Context) {
	m.reapInactive(time.Now())
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.reapInactive(now)
		}
	}
}

func (m *hlsManager) resetTorrentProjections() {
	m.cacheMu.Lock()
	m.torrentFileEnds = nil
	m.cacheMu.Unlock()
}
