package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func withEmptyRegistry(t *testing.T) {
	t.Helper()
	sessionRegistryMu.Lock()
	defer sessionRegistryMu.Unlock()
	if sessionPurgeInProgress || len(sessionFlights) != 0 {
		t.Fatal("session registry busy at test start")
	}
	sessions.Range(func(key, _ interface{}) bool { sessions.Delete(key); return true })
	t.Cleanup(func() {
		sessionRegistryMu.Lock()
		defer sessionRegistryMu.Unlock()
		sessions.Range(func(key, _ interface{}) bool { sessions.Delete(key); return true })
		sessionFlights = make(map[string]*sessionFlight)
		sessionPurgeInProgress = false
	})
}

func TestConcurrentInfohashAddsCreateOneSession(t *testing.T) {
	withEmptyRegistry(t)
	const num = 32
	const id = "0123456789abcdef0123456789abcdef01234567"
	var made atomic.Int32
	start := make(chan struct{})
	build := func(ctx context.Context) (*TorrentSession, error) {
		made.Add(1)
		<-start
		return &TorrentSession{}, nil
	}
	var wg sync.WaitGroup
	var errorsMu sync.Mutex
	var errs []error
	for i := 0; i < num; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := getOrCreateTorrentSession(context.Background(), id, build)
			if err != nil || s == nil {
				errorsMu.Lock()
				errs = append(errs, err)
				errorsMu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(errs) != 0 {
		t.Fatalf("concurrent adds failed: %v", errs)
	}
	if got := made.Load(); got != 1 {
		t.Fatalf("built %d torrent clients, want 1", got)
	}
}

func TestConcurrentFailedInfohashAddsCanRetry(t *testing.T) {
	withEmptyRegistry(t)
	var n atomic.Int32
	boom := errors.New("metadata timeout")
	_, err := getOrCreateTorrentSession(context.Background(), "bad", func(context.Context) (*TorrentSession, error) {
		n.Add(1)
		return nil, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("first failure=%v", err)
	}
	s, err := getOrCreateTorrentSession(context.Background(), "bad", func(context.Context) (*TorrentSession, error) {
		n.Add(1)
		return &TorrentSession{}, nil
	})
	if err != nil || s == nil || n.Load() != 2 {
		t.Fatalf("retry: session=%v error=%v count=%d", s, err, n.Load())
	}
}

func TestRetainedSessionCannotBeReaped(t *testing.T) {
	withEmptyRegistry(t)
	id := "running"
	s := &TorrentSession{LastUsed: time.Now().Add(-time.Hour)}
	sessions.Store(id, s)
	ref, ok := retainTorrentSession(id, false)
	if !ok || ref != s {
		t.Fatal("failed to retain")
	}
	reapInactiveSessions(time.Now(), 15*time.Minute)
	if _, ok := sessions.Load(id); !ok {
		t.Fatal("retired active request")
	}
	releaseTorrentSession(ref)
	reapInactiveSessions(time.Now().Add(16*time.Minute), 15*time.Minute)
	if _, ok := sessions.Load(id); ok {
		t.Fatal("idle session was not retired")
	}
}

func TestPurgeCancelsActiveReaderAndDrainsAfterRelease(t *testing.T) {
	withEmptyRegistry(t)
	sessions.Store("active", &TorrentSession{LastUsed: time.Now()})
	s, ok := retainTorrentSession("active", true)
	if !ok {
		t.Fatal("failed to retain")
	}
	ctx, stop := sessionRequestContext(s, context.Background())
	defer stop()
	retired, drained, ok := beginCancellingSessionPurge()
	if !ok || len(retired) != 1 || len(drained) != 1 {
		t.Fatalf("unexpected purge start: ok=%v count=%d", ok, len(retired))
	}
	t.Cleanup(finishSessionPurge)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("reader context not cancelled")
	}
	if _, ok := retainTorrentSession("active", true); ok {
		t.Fatal("allowed retain after purge")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := waitForDrainedSessions(waitCtx, drained); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("purge did not wait for active reader: %v", err)
	}
	releaseTorrentSession(s)
	if err := waitForDrainedSessions(context.Background(), drained); err != nil {
		t.Fatalf("purge did not drain after release: %v", err)
	}
	for _, session := range retired {
		closeTorrentClient(session)
	}
}

func TestPurgeRejectsInFlightAddition(t *testing.T) {
	withEmptyRegistry(t)
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := getOrCreateTorrentSession(context.Background(), "new", func(context.Context) (*TorrentSession, error) {
			close(started)
			<-release
			return &TorrentSession{}, nil
		})
		done <- err
	}()
	<-started
	if _, _, ok := beginCancellingSessionPurge(); ok {
		t.Fatal("purge admitted while metadata fetch in flight")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("addition failed: %v", err)
	}
}
