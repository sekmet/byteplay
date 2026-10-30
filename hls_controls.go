package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"time"
)

const maxHLSGenerations = 256

type hlsSeekRequest struct {
	Seconds     *float64 `json:"seconds"`
	AudioStream *int     `json:"audioStream"`
}
type hlsControlRequest struct {
	Action string `json:"action"`
}

// A bounded, single-JSON-object decoder is shared by seek and control.
// Oversized requests get a stable 413 without falling through to media work.
func decodeHLSBody(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil {
		err = dec.Decode(new(interface{}))
		if errors.Is(err, io.EOF) {
			err = nil
		}
	}
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			writeHLSError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request exceeds 8 KiB")
		} else {
			writeHLSError(w, http.StatusBadRequest, "invalid_request", "Invalid request JSON")
		}
		return false
	}
	return true
}

func validateHLSSeek(position *float64, duration *float64) error {
	if position == nil {
		return errors.New("absolute seconds are required")
	}
	if math.IsNaN(*position) || math.IsInf(*position, 0) || *position < 0 || *position > 360000 {
		return errors.New("invalid absolute seconds")
	}
	if duration != nil && *duration > 0 && *position >= *duration {
		return errors.New("seek must be earlier than file duration")
	}
	return nil
}

// Seek serializes cancellation and Wait of the previous generation before
// admitting another FFmpeg worker, including rapid seek/audio-switch requests.
// Holding the manager lock excludes reads and teardown while changing the
// generation pointer; no old playlist can resolve into the new directory.
func (m *hlsManager) seek(w http.ResponseWriter, r *http.Request, id string) {
	var req hlsSeekRequest
	if !decodeHLSBody(w, r, &req) {
		return
	}
	m.mu.Lock()
	current := m.jobs[id]
	if current == nil {
		m.mu.Unlock()
		writeHLSError(w, http.StatusNotFound, "not_found", "Playback not found")
		return
	}
	current.mu.Lock()
	probe := current.probe
	duration := current.duration
	paused := current.paused
	audio := current.selectedAudio
	generation := current.generation
	current.mu.Unlock()
	if err := validateHLSSeek(req.Seconds, duration); err != nil {
		m.mu.Unlock()
		writeHLSError(w, http.StatusBadRequest, "invalid_seek", err.Error())
		return
	}
	if probe == nil {
		m.mu.Unlock()
		writeHLSError(w, http.StatusConflict, "probe_pending", "Media probe is not complete")
		return
	}
	if generation >= maxHLSGenerations {
		m.mu.Unlock()
		writeHLSError(w, http.StatusTooManyRequests, "generation_limit", "Too many playback seeks")
		return
	}
	if req.AudioStream != nil {
		audio = req.AudioStream
	}
	profile, err := MediaProfileFromProbe(probe, audio)
	if err == nil {
		_, err = ChooseHLSRoute(profile, HLSLimits{MaxInputWidth: 3840, MaxInputHeight: 2160})
	}
	if err != nil {
		m.mu.Unlock()
		writeHLSError(w, http.StatusBadRequest, "invalid_audio_stream", "Requested audio selection is not supported")
		return
	}
	// Claim the next torrent reader while the current one still owns a retain.
	// Otherwise a shutdown/idle reaper could close its client between jobs.
	retained, ok := retainTorrentSession(current.sessionID, true)
	if !ok {
		m.mu.Unlock()
		writeHLSError(w, http.StatusConflict, "torrent_unavailable", "Torrent session is no longer available")
		return
	}
	current.mu.Lock()
	if current.pauseTimer != nil {
		current.pauseTimer.Stop()
	}
	current.mu.Unlock()
	current.cancel()
	waitCtx, stopWait := context.WithTimeout(r.Context(), 12*time.Second)
	select {
	case <-current.done:
	case <-waitCtx.Done():
		stopWait()
		releaseTorrentSession(retained)
		m.mu.Unlock()
		writeHLSError(w, http.StatusConflict, "generation_draining", "Previous FFmpeg job is still draining")
		return
	}
	stopWait()
	if err := r.Context().Err(); err != nil {
		releaseTorrentSession(retained)
		m.mu.Unlock()
		return
	}
	// The old process has exited and no media response can retain its files
	// while the exclusive manager lock is held. Remove its disjoint output
	// before reserving space for a replacement generation.
	if err := os.RemoveAll(current.dir); err != nil {
		releaseTorrentSession(retained)
		m.mu.Unlock()
		writeHLSError(w, http.StatusInternalServerError, "cache_unavailable", "Cannot reclaim superseded HLS generation")
		return
	}
	if !m.cacheWithinBudget() {
		current.setError("failed", "cache_full", "Combined torrent/HLS cache budget reached")
		releaseTorrentSession(retained)
		m.mu.Unlock()
		writeHLSError(w, http.StatusInsufficientStorage, "cache_full", "Combined torrent/HLS cache budget reached")
		return
	}
	dir := hlsGenerationDir(m.root, id, generation+1)
	if err := os.MkdirAll(dir, 0700); err != nil {
		releaseTorrentSession(retained)
		m.mu.Unlock()
		writeHLSError(w, http.StatusInternalServerError, "cache_unavailable", "Cannot create generation directory")
		return
	}
	if !m.reserveCache(dir) {
		_ = os.RemoveAll(dir)
		current.setError("failed", "cache_full", "Insufficient reserved HLS cache capacity")
		releaseTorrentSession(retained)
		m.mu.Unlock()
		writeHLSError(w, http.StatusInsufficientStorage, "cache_full", "Insufficient reserved HLS cache capacity")
		return
	}
	owner, stopOwner := sessionRequestContext(retained, context.Background())
	ctx, cancel := context.WithCancel(owner)
	source, err := newMediaSource(retained.Torrent, current.fileIndex)
	if err != nil {
		cancel()
		stopOwner()
		m.releaseCache(dir)
		_ = os.RemoveAll(dir)
		releaseTorrentSession(retained)
		m.mu.Unlock()
		writeHLSError(w, http.StatusConflict, "torrent_unavailable", "Selected file no longer available")
		return
	}
	next := &hlsPlayback{
		id: id, sessionID: current.sessionID, fileIndex: current.fileIndex,
		selectedAudio: audio, generation: generation + 1, startSeconds: *req.Seconds,
		dir: dir, session: retained, source: source, ctx: ctx, cancel: cancel,
		stopOwner: stopOwner, done: make(chan struct{}), state: "preparing",
		paused: paused, lastDemand: time.Now(), progress: source.Progress(),
	}
	// Everything needed for the replacement is now owned. Publish one
	// generation pointer; old files remain isolated and inaccessible (410).
	m.jobs[id] = next
	if paused {
		m.schedulePauseLocked(next)
	}
	m.mu.Unlock()
	go m.run(next)
	w.Header().Set("Cache-Control", "no-store")
	respondWithJSON(w, http.StatusAccepted, map[string]interface{}{
		"playbackId": id, "generation": generation + 1, "state": "preparing",
		"statusUrl": "/api/v1/hls/" + id + "/status",
	})
}

// The pause timer may run while manifest polling continues. Only an explicit
// control action resets it; GET index.m3u8 does not count as demand.
func (m *hlsManager) schedulePauseLocked(job *hlsPlayback) {
	if job.pauseTimer != nil {
		job.pauseTimer.Stop()
	}
	job.pauseTimer = time.AfterFunc(m.pauseGrace, func() {
		m.mu.RLock()
		isCurrent := m.jobs[job.id] == job
		m.mu.RUnlock()
		if !isCurrent {
			return
		}
		job.mu.Lock()
		paused := job.paused
		complete := job.state == "complete"
		job.mu.Unlock()
		if !paused || complete {
			return
		}
		job.cancel()
		<-job.done // wait for CommandContext FFmpeg child to exit
		m.mu.RLock()
		isCurrent = m.jobs[job.id] == job
		m.mu.RUnlock()
		if !isCurrent {
			return
		}
		job.mu.Lock()
		if job.paused {
			job.state = "paused_idle"
			job.err = nil
		}
		job.mu.Unlock()
	})
}
func (m *hlsManager) control(w http.ResponseWriter, r *http.Request, id string) {
	var req hlsControlRequest
	if !decodeHLSBody(w, r, &req) {
		return
	}
	if req.Action != "pause" && req.Action != "resume" {
		writeHLSError(w, http.StatusBadRequest, "invalid_action", "Action must be pause or resume")
		return
	}
	m.mu.Lock()
	job := m.jobs[id]
	if job == nil {
		m.mu.Unlock()
		writeHLSError(w, http.StatusNotFound, "not_found", "Playback not found")
		return
	}
	job.mu.Lock()
	if req.Action == "resume" && (job.state == "paused_idle" || job.state == "cancelled" || job.state == "failed") {
		job.mu.Unlock()
		m.mu.Unlock()
		writeHLSError(w, http.StatusConflict, "seek_required", "Send a seek with the client absolute playback time before resuming")
		return
	}
	if req.Action == "pause" {
		if !job.paused {
			job.paused = true
			m.schedulePauseLocked(job)
		}
	} else {
		job.paused = false
		if job.pauseTimer != nil {
			job.pauseTimer.Stop()
		}
	}
	job.lastDemand = time.Now()
	state := job.state
	job.mu.Unlock()
	m.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	respondWithJSON(w, http.StatusOK, map[string]interface{}{"playbackId": id, "processingState": state})
}

func (m *hlsManager) idleExpired(job *hlsPlayback, now time.Time) bool {
	job.mu.Lock()
	defer job.mu.Unlock()
	// Playlist polls intentionally do not extend lastDemand.
	return !job.paused && job.state == "ready" && !job.lastDemand.IsZero() &&
		now.Sub(job.lastDemand) >= m.idleTimeout
}
