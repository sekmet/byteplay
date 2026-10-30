package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// The lock serializes retain, retire, purge, and in-flight infohash leadership.
// sessions remains a sync.Map for compatibility with the existing handlers.
var sessionRegistryMu sync.Mutex
var sessionFlights = make(map[string]*sessionFlight)
var sessionPurgeInProgress bool
var sessionEpoch uint64

type sessionFlight struct {
	done    chan struct{}
	session *TorrentSession
	err     error
}

type sessionAddError struct {
	status  int
	message string
}

func (e *sessionAddError) Error() string { return e.message }

var errSessionUnavailable = &sessionAddError{http.StatusServiceUnavailable, "Torrent session unavailable"}

// Concurrent adds share a single metadata lookup and client. A failed lookup is
// returned to all waiters; a later request may safely retry.
func getOrCreateTorrentSession(ctx context.Context, id string, build func(context.Context) (*TorrentSession, error)) (*TorrentSession, error) {
	if id == "" {
		return nil, errors.New("empty infohash")
	}
	sessionRegistryMu.Lock()
	if sessionPurgeInProgress {
		sessionRegistryMu.Unlock()
		return nil, errSessionUnavailable
	}
	if item, ok := sessions.Load(id); ok {
		session := item.(*TorrentSession)
		session.lifecycleMu.Lock()
		if !session.closing {
			session.LastUsed = time.Now()
			session.lifecycleMu.Unlock()
			sessionRegistryMu.Unlock()
			return session, nil
		}
		session.lifecycleMu.Unlock()
	}
	if flight, ok := sessionFlights[id]; ok {
		sessionRegistryMu.Unlock()
		select {
		case <-flight.done:
			return flight.session, flight.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	flight := &sessionFlight{done: make(chan struct{})}
	sessionFlights[id] = flight
	epoch := sessionEpoch
	sessionRegistryMu.Unlock()

	created, err := build(ctx)
	sessionRegistryMu.Lock()
	if sessionEpoch != epoch || sessionPurgeInProgress {
		err = errSessionUnavailable
	}
	if err == nil && created == nil {
		err = errors.New("session builder returned nil without error")
	}
	if err == nil {
		created.lifecycleMu.Lock()
		created.LastUsed = time.Now()
		created.ensureStopContextLocked()
		created.lifecycleMu.Unlock()
		sessions.Store(id, created)
		flight.session = created
	}
	flight.err = err
	delete(sessionFlights, id)
	close(flight.done)
	sessionRegistryMu.Unlock()

	if err != nil && created != nil {
		closeTorrentClient(created)
	}
	return flight.session, err
}

// Retention is atomic with retirement, covering the entire handler including
// its slow, partial torrent reader or FFmpeg subprocess.
func retainTorrentSession(id string, touch bool) (*TorrentSession, bool) {
	sessionRegistryMu.Lock()
	defer sessionRegistryMu.Unlock()
	value, ok := sessions.Load(id)
	if !ok {
		return nil, false
	}
	session := value.(*TorrentSession)
	session.lifecycleMu.Lock()
	defer session.lifecycleMu.Unlock()
	if session.closing {
		return nil, false
	}
	session.ensureStopContextLocked()
	session.active++
	if touch {
		session.LastUsed = time.Now()
	}
	return session, true
}
func releaseTorrentSession(session *TorrentSession) {
	session.lifecycleMu.Lock()
	if session.active > 0 {
		session.active--
	}
	if session.active == 0 {
		session.LastUsed = time.Now()
		if session.closing && session.drained != nil {
			close(session.drained)
		}
	}
	session.lifecycleMu.Unlock()
}
func closeTorrentClient(session *TorrentSession) {
	if session == nil {
		return
	}
	if session.Torrent != nil {
		session.Torrent.Drop()
	}
	if session.Client != nil {
		session.Client.Close()
	}
	if session.Port > 0 {
		releasePort(session.Port)
	}
}
func reapInactiveSessions(now time.Time, ttl time.Duration) {
	var retired []*TorrentSession
	sessionRegistryMu.Lock()
	sessions.Range(func(key, value interface{}) bool {
		session := value.(*TorrentSession)
		session.lifecycleMu.Lock()
		if !session.closing && session.active == 0 && now.Sub(session.LastUsed) >= ttl {
			session.markClosingLocked()
			sessions.Delete(key)
			retired = append(retired, session)
		}
		session.lifecycleMu.Unlock()
		return true
	})
	sessionRegistryMu.Unlock()
	for _, session := range retired {
		closeTorrentClient(session)
	}
}

// Preserve all active cache data if any operation cannot be drained safely.
func drainIdleSessionRegistry() ([]*TorrentSession, bool) {
	sessionRegistryMu.Lock()
	defer sessionRegistryMu.Unlock()
	if sessionPurgeInProgress || len(sessionFlights) != 0 {
		return nil, false
	}
	busy := false
	sessions.Range(func(_, value interface{}) bool {
		session := value.(*TorrentSession)
		session.lifecycleMu.Lock()
		if session.active > 0 || session.closing {
			busy = true
		}
		session.lifecycleMu.Unlock()
		return !busy
	})
	if busy {
		return nil, false
	}
	sessionPurgeInProgress = true
	sessionEpoch++
	var retired []*TorrentSession
	sessions.Range(func(key, value interface{}) bool {
		session := value.(*TorrentSession)
		session.lifecycleMu.Lock()
		session.markClosingLocked()
		session.lifecycleMu.Unlock()
		sessions.Delete(key)
		retired = append(retired, session)
		return true
	})
	return retired, true
}
func finishSessionPurge() {
	sessionRegistryMu.Lock()
	sessionPurgeInProgress = false
	sessionRegistryMu.Unlock()
}

// All lifecycle helpers below hold lifecycleMu for state transitions.
func (session *TorrentSession) ensureStopContextLocked() {
	if session.stopCtx == nil {
		session.stopCtx, session.stopCancel = context.WithCancel(context.Background())
	}
}
func (session *TorrentSession) markClosingLocked() <-chan struct{} {
	session.ensureStopContextLocked()
	if !session.closing {
		session.closing = true
		session.drained = make(chan struct{})
		if session.active == 0 {
			close(session.drained)
		}
		session.stopCancel()
	}
	return session.drained
}

// Joined request and torrent-session cancellation propagates to torrent
// Reader.SetContext, FFprobe and exec.CommandContext FFmpeg workers.
func sessionRequestContext(session *TorrentSession, parent context.Context) (context.Context, func()) {
	session.lifecycleMu.Lock()
	session.ensureStopContextLocked()
	owner := session.stopCtx
	session.lifecycleMu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	unregister := context.AfterFunc(owner, cancel)
	return ctx, func() {
		unregister()
		cancel()
	}
}
func beginCancellingSessionPurge() ([]*TorrentSession, []<-chan struct{}, bool) {
	sessionRegistryMu.Lock()
	defer sessionRegistryMu.Unlock()
	if sessionPurgeInProgress || len(sessionFlights) != 0 {
		return nil, nil, false
	}
	sessionPurgeInProgress = true
	sessionEpoch++
	var retired []*TorrentSession
	var drained []<-chan struct{}
	sessions.Range(func(key, value interface{}) bool {
		session := value.(*TorrentSession)
		session.lifecycleMu.Lock()
		done := session.markClosingLocked()
		session.lifecycleMu.Unlock()
		sessions.Delete(key)
		retired = append(retired, session)
		drained = append(drained, done)
		return true
	})
	return retired, drained, true
}
func waitForDrainedSessions(ctx context.Context, channels []<-chan struct{}) error {
	for _, done := range channels {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
