package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxHLSPlaybacks           = 8
	maxHLSCacheBytes    int64 = 10 << 30
	maxHLSPlaybackBytes int64 = 2 << 30
)

var hlsSegmentName = regexp.MustCompile(`^segment-[0-9]{6}\.ts$`)

type hlsError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type hlsStatus struct {
	PlaybackID                 string    `json:"playbackId"`
	State                      string    `json:"state"`
	Mode                       *string   `json:"mode"`
	Generation                 int       `json:"generation"`
	PlaylistURL                *string   `json:"playlistUrl"`
	PlaylistReady              bool      `json:"playlistReady"`
	StartupCachePercent        int       `json:"startupCachePercent"`
	StartupCacheReady          bool      `json:"startupCacheReady"`
	StartupCacheBytesRequired  int64     `json:"startupCacheBytesRequired"`
	StatusURL                  string    `json:"statusUrl"`
	DurationSeconds            *float64  `json:"durationSeconds"`
	StartSeconds               float64   `json:"startSeconds"`
	SegmentsReady              int       `json:"segmentsReady"`
	TorrentBytesCompleted      int64     `json:"torrentBytesCompleted"`
	TorrentBytesTotal          int64     `json:"torrentBytesTotal"`
	SelectedFileBytesTotal     int64     `json:"selectedFileBytesTotal"`
	SelectedFileBytesCompleted *int64    `json:"selectedFileBytesCompleted"`
	DownloadBytesPerSecond     *float64  `json:"downloadBytesPerSecond"`
	Peers                      int       `json:"peers"`
	BufferedSecondsEstimate    *float64  `json:"bufferedSecondsEstimate"`
	Error                      *hlsError `json:"error"`
}
type hlsCreateRequest struct {
	SessionID   string `json:"sessionId"`
	FileIndex   *int   `json:"fileIndex"`
	AudioStream *int   `json:"audioStream"`
}
type hlsPlayback struct {
	mu                  sync.Mutex
	id                  string
	generation          int
	startSeconds        float64
	probe               *ProbeResult
	paused              bool
	pauseTimer          *time.Timer
	lastDemand          time.Time
	sessionID           string
	fileIndex           int
	selectedAudio       *int
	dir                 string
	session             *TorrentSession
	source              MediaSource
	ctx                 context.Context
	cancel              context.CancelFunc
	stopOwner           func()
	done                chan struct{}
	state               string
	mode                *string
	duration            *float64
	segments            int
	playlistReady       bool
	startupCachePercent int
	err                 *hlsError
	progress            MediaSourceProgress
	peers               int
	rate                downloadRateSampler
}
type hlsManager struct {
	mu              sync.RWMutex
	root            string
	torrentRoot     string
	cacheLimit      int64
	cacheConfigErr  error
	cacheMu         sync.Mutex
	reservations    map[string]int64
	torrentFileEnds map[string]int64
	reserveBytes    int64
	retentionTTL    time.Duration
	pauseGrace      time.Duration
	idleTimeout     time.Duration
	jobs            map[string]*hlsPlayback
	processSlots    chan struct{}
	videoSlots      chan struct{}
	ffmpeg          string
}

func newHLSManager(root, ffmpeg string) *hlsManager {
	limit, configErr := parseHLSCacheLimit(os.Getenv("BITPLAY_CACHE_MAX_BYTES"))
	return &hlsManager{root: root, torrentRoot: "./torrent-data", cacheLimit: limit,
		cacheConfigErr: configErr, retentionTTL: defaultHLSRetentionTTL,
		reservations: make(map[string]int64), reserveBytes: min(maxHLSPlaybackBytes, limit),
		ffmpeg: ffmpeg, jobs: make(map[string]*hlsPlayback),
		pauseGrace: 90 * time.Second, idleTimeout: 2 * time.Minute,
		processSlots: make(chan struct{}, 2), videoSlots: make(chan struct{}, 1)}
}

var activeHLS = newHLSManager("./hls-cache", "")

func writeHLSError(w http.ResponseWriter, code int, key, message string) {
	respondWithJSON(w, code, map[string]interface{}{"error": hlsError{Code: key, Message: message}})
}
func newPlaybackID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}
func (j *hlsPlayback) snapshot() hlsStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	generation := j.generation
	if generation == 0 {
		generation = 1
	}
	s := hlsStatus{PlaybackID: j.id, State: j.state, Mode: j.mode, Generation: generation,
		PlaylistReady: j.playlistReady, StatusURL: "/api/v1/hls/" + j.id + "/status",
		StartupCachePercent:       j.startupCachePercent,
		StartupCacheReady:         startupCacheReached(j.progress.FileBytesTotal, j.progress.FileBytesCompleted, j.startupCachePercent),
		StartupCacheBytesRequired: requiredStartupBytes(j.progress.FileBytesTotal, j.startupCachePercent),
		DurationSeconds:           j.duration, StartSeconds: j.startSeconds, SegmentsReady: j.segments,
		TorrentBytesCompleted: j.progress.TorrentBytesCompleted, TorrentBytesTotal: j.progress.TorrentBytesTotal,
		SelectedFileBytesTotal: j.progress.FileBytesTotal,
		DownloadBytesPerSecond: j.rate.value, Peers: j.peers, Error: j.err}
	if j.progress.FileBytesTotal > 0 {
		n := j.progress.FileBytesCompleted
		s.SelectedFileBytesCompleted = &n
	}
	if j.playlistReady && s.StartupCacheReady {
		url := "/api/v1/hls/" + j.id + "/g/" + strconv.Itoa(generation) + "/index.m3u8"
		s.PlaylistURL = &url
	}
	return s
}
func (j *hlsPlayback) setError(state, code, msg string) {
	j.mu.Lock()
	if state == "cancelled" && j.state == "failed" {
		j.mu.Unlock()
		return
	}
	j.state = state
	if state == "failed" {
		j.playlistReady = false
	}
	j.err = &hlsError{Code: code, Message: msg}
	j.mu.Unlock()
}
func (j *hlsPlayback) observe() {
	p := j.source.Progress()
	peers := 0
	if j.session != nil && j.session.Torrent != nil {
		peers = j.session.Torrent.Stats().ActivePeers
	}
	j.mu.Lock()
	j.rate.observe(p.TorrentBytesCompleted, time.Now())
	j.progress = p
	j.peers = peers
	j.mu.Unlock()
}
func (m *hlsManager) get(id string) *hlsPlayback {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.jobs[id]
}
func (m *hlsManager) create(w http.ResponseWriter, r *http.Request) {
	m.reapInactive(time.Now())
	if m.cacheConfigErr != nil {
		writeHLSError(w, http.StatusServiceUnavailable, "cache_config_invalid", "Invalid BITPLAY_CACHE_MAX_BYTES setting")
		return
	}
	if m.ffmpeg == "" || ffprobePath == "" {
		writeHLSError(w, http.StatusServiceUnavailable, "ffmpeg_unavailable", "FFmpeg and FFprobe must be installed")
		return
	}
	var req hlsCreateRequest
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err == nil {
		err = decoder.Decode(new(interface{}))
		if errors.Is(err, io.EOF) {
			err = nil
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeHLSError(w, http.StatusRequestEntityTooLarge, "request_too_large", "HLS creation request exceeds 8 KiB")
			return
		}
		writeHLSError(w, http.StatusBadRequest, "invalid_request", "Malformed HLS creation request")
		return
	}
	if req.SessionID == "" || req.FileIndex == nil {
		writeHLSError(w, http.StatusBadRequest, "invalid_request", "Expected sessionId and fileIndex")
		return
	}
	session, ok := retainTorrentSession(req.SessionID, true)
	if !ok {
		writeHLSError(w, http.StatusNotFound, "torrent_not_found", "Torrent session not found")
		return
	}
	release := true
	defer func() {
		if release {
			releaseTorrentSession(session)
		}
	}()
	source, err := newMediaSource(session.Torrent, *req.FileIndex)
	if err != nil {
		writeHLSError(w, http.StatusBadRequest, "invalid_file_index", "File index unavailable")
		return
	}
	switch strings.ToLower(filepath.Ext(source.file.DisplayPath())) {
	case ".mp4", ".mkv", ".avi", ".webm", ".mov", ".m4v", ".wmv":
	default:
		writeHLSError(w, http.StatusUnsupportedMediaType, "unsupported_media", "Selected file is not a supported video container")
		return
	}
	// Snapshot the startup threshold for this playback (not every poll).
	startupPercent := startupCachePercent()
	cacheTarget := requiredStartupBytes(source.file.Length(), startupPercent)
	if cacheTarget > m.cacheLimit {
		writeHLSError(w, http.StatusInsufficientStorage, "startup_cache_exceeds_budget",
			"Selected-file startup cache target exceeds the configured cache budget")
		return
	}
	m.mu.Lock()
	if len(m.jobs) >= maxHLSPlaybacks {
		m.mu.Unlock()
		w.Header().Set("Retry-After", "5")
		writeHLSError(w, http.StatusTooManyRequests, "playback_limit", "Too many playback sessions")
		return
	}
	// Check capacity before reserving another output directory.
	if !m.cacheWithinBudget() {
		m.mu.Unlock()
		writeHLSError(w, http.StatusInsufficientStorage, "cache_full", "Combined torrent/HLS cache capacity reached")
		return
	}
	id, err := newPlaybackID()
	if err != nil {
		m.mu.Unlock()
		writeHLSError(w, 500, "id_unavailable", "Failed to create playback")
		return
	}
	if err := os.MkdirAll(m.root, 0700); err != nil {
		m.mu.Unlock()
		writeHLSError(w, 500, "cache_unavailable", "HLS cache directory unavailable")
		return
	}
	dir := hlsGenerationDir(m.root, id, 1)
	if err := os.MkdirAll(dir, 0700); err != nil {
		m.mu.Unlock()
		writeHLSError(w, 500, "cache_unavailable", "HLS playback directory unavailable")
		return
	}
	if !m.reserveCache(dir) {
		_ = os.RemoveAll(filepath.Join(m.root, id))
		m.mu.Unlock()
		writeHLSError(w, http.StatusInsufficientStorage, "cache_full", "Insufficient reserved cache capacity for another HLS worker")
		return
	}
	ctx, stopOwner := sessionRequestContext(session, context.Background())
	ctx, cancel := context.WithCancel(ctx)
	job := &hlsPlayback{
		id: id, sessionID: req.SessionID, fileIndex: *req.FileIndex, selectedAudio: req.AudioStream,
		dir: dir, session: session, source: source, ctx: ctx, cancel: cancel, stopOwner: stopOwner,
		done: make(chan struct{}), state: "queued", generation: 1, lastDemand: time.Now(), progress: source.Progress(),
		startupCachePercent: startupPercent,
	}
	m.jobs[id] = job
	m.mu.Unlock()
	release = false
	// Prefetch only the selected-file prefix. FFprobe/FFmpeg keep their
	// independent high-priority readers around the actual playhead.
	if startupPercent > 0 && !startupCacheReached(job.progress.FileBytesTotal, job.progress.FileBytesCompleted, startupPercent) {
		prefetchStartupPieces(source, startupPercent)
	}
	go m.run(job)
	w.Header().Set("Cache-Control", "no-store")
	respondWithJSON(w, http.StatusCreated, job.snapshot())
}
func (m *hlsManager) run(job *hlsPlayback) {
	defer close(job.done)
	defer m.releaseCache(job.dir)
	defer releaseTorrentSession(job.session)
	defer job.stopOwner()
	defer job.cancel()
	if err := job.ctx.Err(); err != nil {
		job.setError("cancelled", "cancelled", "Playback cancelled")
		return
	}
	job.mu.Lock()
	job.state = "probing"
	job.mu.Unlock()
	probe, err := runFFprobe(job.ctx, loopbackStreamURL(job.sessionID, job.fileIndex))
	if err != nil {
		if job.ctx.Err() != nil {
			job.setError("cancelled", "cancelled", "Playback cancelled")
		} else {
			job.setError("failed", "probe_failed", "Unable to probe the selected media")
		}
		return
	}
	// The HLS worker already completed FFprobe successfully. Make that
	// result available to the legacy /probe endpoint so the browser audio
	// selector does not launch a redundant (and potentially stalled) probe.
	cacheSessionProbe(job.session, job.fileIndex, probe)
	profile, err := MediaProfileFromProbe(probe, job.selectedAudio)
	if err != nil {
		job.setError("failed", "unsupported_media", "Selected media has no supported video/audio streams")
		return
	}
	route, err := ChooseHLSRoute(profile, HLSLimits{MaxInputWidth: 3840, MaxInputHeight: 2160})
	if err != nil {
		job.setError("failed", "unsupported_media", "Unable to choose supported HLS encoding")
		return
	}
	opts := HLSFFmpegOptions{
		Input:          loopbackStreamURL(job.sessionID, job.fileIndex),
		Playlist:       filepath.Join(job.dir, "index.m3u8"),
		SegmentPattern: filepath.Join(job.dir, "segment-%06d.ts"),
		StartSeconds:   job.startSeconds,
	}
	args, err := BuildHLSFFmpegArgs(route, opts)
	if err != nil {
		job.setError("failed", "invalid_route", "Unable to prepare media encoder")
		return
	}
	if loopbackInternalToken != "" {
		// -headers is an FFmpeg input option; place it immediately before -i.
		for i, arg := range args {
			if arg == "-i" {
				a := append([]string{}, args[:i]...)
				a = append(a, "-headers", "X-BytePlay-Internal: "+loopbackInternalToken+"\r\n")
				args = append(a, args[i:]...)
				break
			}
		}
	}
	// Stream-copy seeks begin on a source keyframe, often before the
	// requested position. Never report the requested offset as measured.
	if job.startSeconds > 0 && route.VideoEncoder == "copy" {
		effective, err := probeCopySeekStart(job.ctx, ffprobePath,
			loopbackStreamURL(job.sessionID, job.fileIndex), job.startSeconds, loopbackInternalToken)
		if err != nil {
			job.setError("failed", "seek_position_unverified", "Cannot establish the stream-copy seek keyframe")
			return
		}
		job.mu.Lock()
		job.startSeconds = effective
		job.mu.Unlock()
	}
	job.mu.Lock()
	job.probe = probe
	mode := string(route.Mode)
	job.mode = &mode
	if probe.Duration > 0 {
		dur := probe.Duration
		job.duration = &dur
	}
	job.state = "preparing"
	job.mu.Unlock()
	// Do not reserve a process slot for metadata lookup or probing.
	select {
	case m.processSlots <- struct{}{}:
	case <-job.ctx.Done():
		job.setError("cancelled", "cancelled", "Playback cancelled")
		return
	}
	defer func() { <-m.processSlots }()
	if route.Mode == HLSRouteVideoTranscode {
		select {
		case m.videoSlots <- struct{}{}:
			defer func() { <-m.videoSlots }()
		case <-job.ctx.Done():
			job.setError("cancelled", "cancelled", "Playback cancelled")
			return
		}
	}
	if job.ctx.Err() != nil {
		job.setError("cancelled", "cancelled", "Playback cancelled")
		return
	}
	command := exec.CommandContext(job.ctx, m.ffmpeg, args...)
	command.WaitDelay = 3 * time.Second
	stderr := &limitedWriter{max: 4096}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		job.setError("failed", "encoder_start_failed", "Unable to start FFmpeg")
		return
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-wait:
			job.observe()
			count, ready, _ := inspectHLSManifest(job.dir)
			job.mu.Lock()
			if count > job.segments {
				job.segments = count
			}
			if ready {
				job.playlistReady = true
			}
			job.mu.Unlock()
			if job.ctx.Err() != nil {
				job.setError("cancelled", "cancelled", "Playback cancelled")
			} else if err != nil {
				job.setError("failed", "encoder_failed", "FFmpeg could not generate HLS media")
			} else if !ready {
				job.setError("failed", "invalid_playlist", "FFmpeg exited without two finalized segments")
			} else {
				job.mu.Lock()
				job.state = "complete"
				job.mu.Unlock()
			}
			return
		case <-ticker.C:
			job.observe()
			count, ready, _ := inspectHLSManifest(job.dir)
			job.mu.Lock()
			if count > job.segments {
				job.segments = count
			}
			if ready && !job.playlistReady {
				job.playlistReady = true
				job.state = "ready"
			}
			job.mu.Unlock()
			if m.idleExpired(job, time.Now()) {
				job.cancel()
			}
			if dirSize(job.dir) > m.reserveBytes || !m.cacheWithinBudget() {
				job.setError("failed", "cache_full", "Combined torrent/HLS cache budget reached")
				job.cancel()
			}
		}
	}
}

// A manifest is published only after at least two referenced, finalized,
// non-empty TS files are readable; unlisted or .tmp files are never served.
func inspectHLSManifest(dir string) (int, bool, map[string]bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "index.m3u8"))
	if err != nil || !strings.HasPrefix(string(raw), "#EXTM3U") {
		return 0, false, nil
	}
	names := make(map[string]bool)
	for _, line := range strings.Split(string(raw), "\n") {
		name := strings.TrimSpace(line)
		if !hlsSegmentName.MatchString(name) {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.IsDir() || info.Size() == 0 {
			continue
		}
		names[name] = true
	}
	return len(names), len(names) >= 2, names
}
func (m *hlsManager) serveMedia(w http.ResponseWriter, r *http.Request, job *hlsPlayback, generation, filename string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.jobs[job.id] != job {
		writeHLSError(w, http.StatusGone, "generation_expired", "Playback generation is no longer available")
		return
	}
	job.mu.Lock()
	currentGeneration := job.generation
	if currentGeneration == 0 {
		currentGeneration = 1
	}
	job.mu.Unlock()
	if generation != strconv.Itoa(currentGeneration) {
		writeHLSError(w, http.StatusGone, "generation_expired", "Playback generation is no longer available")
		return
	}
	if filename != "index.m3u8" && !hlsSegmentName.MatchString(filename) {
		writeHLSError(w, http.StatusNotFound, "not_found", "HLS resource not found")
		return
	}
	job.mu.Lock()
	ready := job.playlistReady && startupCacheReached(job.progress.FileBytesTotal, job.progress.FileBytesCompleted, job.startupCachePercent)
	job.mu.Unlock()
	if !ready {
		writeHLSError(w, http.StatusTooEarly, "preparing", "HLS media is not ready yet")
		return
	}
	_, valid, declared := inspectHLSManifest(job.dir)
	if !valid {
		writeHLSError(w, http.StatusTooEarly, "preparing", "No valid HLS manifest available")
		return
	}
	if filename != "index.m3u8" && !declared[filename] {
		writeHLSError(w, http.StatusNotFound, "not_found", "HLS resource not found")
		return
	}
	if filename == "index.m3u8" {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
	} else {
		job.mu.Lock()
		job.lastDemand = time.Now()
		job.mu.Unlock()
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Cache-Control", "private, max-age=120")
	}
	http.ServeFile(w, r, filepath.Join(job.dir, filename))
}
func (m *hlsManager) delete(w http.ResponseWriter, r *http.Request, id string) {
	m.mu.Lock()
	job := m.jobs[id]
	if job != nil {
		delete(m.jobs, id)
	}
	m.mu.Unlock()
	if job != nil {
		job.cancel()
		job.mu.Lock()
		if job.pauseTimer != nil {
			job.pauseTimer.Stop()
		}
		job.mu.Unlock()
		go func() {
			<-job.done
			_ = os.RemoveAll(filepath.Join(m.root, job.id))
		}()
	}
	w.WriteHeader(http.StatusNoContent)
}
func (m *hlsManager) stopAll(timeout time.Duration) {
	m.mu.Lock()
	jobs := make([]*hlsPlayback, 0, len(m.jobs))
	for id, j := range m.jobs {
		jobs = append(jobs, j)
		delete(m.jobs, id)
	}
	m.mu.Unlock()
	for _, j := range jobs {
		j.mu.Lock()
		if j.pauseTimer != nil {
			j.pauseTimer.Stop()
		}
		j.mu.Unlock()
		j.cancel()
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for _, j := range jobs {
		select {
		case <-j.done:
			_ = os.RemoveAll(filepath.Join(m.root, j.id))
		case <-deadline.C:
			return
		}
	}
}
func (m *hlsManager) handler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/hls/")
	if path == "sessions" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeHLSError(w, 405, "method_not_allowed", "Use POST")
			return
		}
		m.create(w, r)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) == 0 || len(parts[0]) != 32 {
		writeHLSError(w, http.StatusNotFound, "not_found", "HLS resource not found")
		return
	}
	if r.Method == http.MethodDelete && len(parts) == 1 {
		m.delete(w, r, parts[0])
		return
	}
	job := m.get(parts[0])
	if job == nil {
		writeHLSError(w, http.StatusNotFound, "not_found", "HLS resource not found")
		return
	}
	if len(parts) == 2 && parts[1] == "seek" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeHLSError(w, 405, "method_not_allowed", "Use POST")
			return
		}
		m.seek(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "control" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeHLSError(w, 405, "method_not_allowed", "Use POST")
			return
		}
		m.control(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "status" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeHLSError(w, 405, "method_not_allowed", "Use GET")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		respondWithJSON(w, 200, job.snapshot())
		return
	}
	if len(parts) == 4 && parts[1] == "g" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeHLSError(w, 405, "method_not_allowed", "Use GET or HEAD")
			return
		}
		m.serveMedia(w, r, job, parts[2], parts[3])
		return
	}
	http.NotFound(w, r)
}

func cacheSessionProbe(session *TorrentSession, fileIndex int, probe *ProbeResult) {
	if session == nil || fileIndex < 0 || probe == nil {
		return
	}
	session.ProbeMu.Lock()
	defer session.ProbeMu.Unlock()
	if session.ProbeCache == nil {
		session.ProbeCache = make(map[int]*ProbeResult)
	}
	session.ProbeCache[fileIndex] = probe
}
