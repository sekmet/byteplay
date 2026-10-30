package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHLSManifestWaitsForTwoReferencedFinalizedSegments(t *testing.T) {
	dir := t.TempDir()
	playlist := filepath.Join(dir, "index.m3u8")
	if err := os.WriteFile(playlist, []byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\nsegment-000000.ts\n#EXTINF:4.0,\nsegment-000001.ts\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "segment-000000.ts"), []byte("valid-zero"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, ok, _ := inspectHLSManifest(dir); n != 1 || ok {
		t.Fatalf("premature HLS readiness: n=%d ready=%v", n, ok)
	}
	if err := os.WriteFile(filepath.Join(dir, "segment-000001.ts.tmp"), []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, ok, _ := inspectHLSManifest(dir); n != 1 || ok {
		t.Fatalf("temporary segment counted: n=%d ready=%v", n, ok)
	}
	if err := os.WriteFile(filepath.Join(dir, "segment-000001.ts"), []byte("valid-one"), 0600); err != nil {
		t.Fatal(err)
	}
	if n, ok, files := inspectHLSManifest(dir); n != 2 || !ok || !files["segment-000001.ts"] {
		t.Fatalf("two finalized segments not recognized: n=%d ready=%v", n, ok)
	}
}
func TestHLSHTTPGenerationAndAssetGuards(t *testing.T) {
	m := newHLSManager(t.TempDir(), "no-ffmpeg-run-needed")
	id := "0123456789abcdef0123456789abcdef"
	dir := filepath.Join(m.root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4.0,\nsegment-000000.ts\n#EXTINF:4.0,\nsegment-000001.ts\n"
	if err := os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"segment-000000.ts", "segment-000001.ts", "not-listed.ts", "segment-000003.ts.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("TS"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &hlsPlayback{id: id, dir: dir, ctx: ctx, cancel: cancel, state: "preparing", done: make(chan struct{})}
	close(job.done)
	m.jobs[id] = job
	request := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		m.handler(w, httptest.NewRequest(method, path, nil))
		return w
	}
	base := "/api/v1/hls/" + id + "/g/1/"
	if result := request(http.MethodGet, base+"index.m3u8"); result.Code != http.StatusTooEarly {
		t.Fatalf("premature manifest served: %d", result.Code)
	}
	job.mu.Lock()
	job.playlistReady = true
	job.state = "ready"
	job.segments = 2
	job.mu.Unlock()
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"GET", base + "index.m3u8", 200},
		{"HEAD", base + "segment-000000.ts", 200},
		{"GET", base + "segment-000001.ts", 200},
		{"GET", base + "segment-000003.ts.tmp", 404},
		{"GET", base + "segment-000002.ts", 404},
		{"GET", base + "not-listed.ts", 404},
		{"GET", "/api/v1/hls/" + id + "/g/2/index.m3u8", 410},
		{"POST", base + "index.m3u8", 405},
		{"GET", "/api/v1/hls/" + id + "/status", 200},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := request(tc.method, tc.path)
			if r.Code != tc.code {
				t.Fatalf("%s %s => %d want %d", tc.method, tc.path, r.Code, tc.code)
			}
		})
	}
	if r := request("GET", base+"index.m3u8"); !strings.HasPrefix(r.Header().Get("Content-Type"), "application/vnd.apple.mpegurl") || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unsafe playlist headers: %v", r.Header())
	}
	statusResponse := request("GET", "/api/v1/hls/"+id+"/status")
	var status hlsStatus
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.PlaylistReady || status.PlaylistURL == nil || !strings.Contains(*status.PlaylistURL, "/g/1/") {
		t.Fatalf("status not generation-qualified: %+v", status)
	}
	if r := request("DELETE", "/api/v1/hls/"+id); r.Code != http.StatusNoContent {
		t.Fatalf("DELETE=%d", r.Code)
	}
	if r := request("DELETE", "/api/v1/hls/"+id); r.Code != http.StatusNoContent {
		t.Fatalf("idempotent DELETE=%d", r.Code)
	}
	if r := request("GET", "/api/v1/hls/"+id+"/status"); r.Code != 404 {
		t.Fatalf("deleted status=%d", r.Code)
	}
}
func TestHLSErrorResponseAndUnconfiguredWorker(t *testing.T) {
	manager := newHLSManager(t.TempDir(), "")
	w := httptest.NewRecorder()
	manager.handler(w, httptest.NewRequest("POST", "/api/v1/hls/sessions", strings.NewReader("{}")))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing encoder returned %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"ffmpeg_unavailable"`) {
		t.Fatalf("missing machine-readable error: %s", w.Body.String())
	}
}
func TestHLSStatusNeverPretendsUnknownSpeed(t *testing.T) {
	job := &hlsPlayback{id: "test", state: "queued"}
	snapshot := job.snapshot()
	if snapshot.DownloadBytesPerSecond != nil || snapshot.DurationSeconds != nil || snapshot.PlaylistReady || snapshot.Mode != nil {
		t.Fatalf("invented unmeasured media status: %+v", snapshot)
	}
}
func TestHLSStopAllCancelsOwnedWork(t *testing.T) {
	m := newHLSManager(t.TempDir(), "ffmpeg")
	ctx, cancel := context.WithCancel(context.Background())
	job := &hlsPlayback{id: "idle", ctx: ctx, cancel: cancel, done: make(chan struct{})}
	m.jobs["idle"] = job
	go func() { <-ctx.Done(); close(job.done) }()
	m.stopAll(time.Second)
	if ctx.Err() != context.Canceled {
		t.Fatal("shutdown failed to cancel worker")
	}
	if len(m.jobs) != 0 {
		t.Fatal("shutdown retained playback jobs")
	}
}

func TestHLSCreationBodyTooLargeReturnsStructured413(t *testing.T) {
	prev := ffprobePath
	ffprobePath = "test-ffprobe"
	defer func() { ffprobePath = prev }()
	manager := newHLSManager(t.TempDir(), "test-ffmpeg")
	body := strings.NewReader("{\"sessionId\":\"" + strings.Repeat("x", 9000) + "\",\"fileIndex\":0}")
	w := httptest.NewRecorder()
	manager.handler(w, httptest.NewRequest(http.MethodPost, "/api/v1/hls/sessions", body))
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "\"request_too_large\"") {
		t.Fatalf("oversized request returned %d %s", w.Code, w.Body.String())
	}
}
func TestHLSUnknownAssetReturnsStructured404(t *testing.T) {
	m := newHLSManager(t.TempDir(), "unused")
	w := httptest.NewRecorder()
	m.handler(w, httptest.NewRequest(http.MethodGet, "/api/v1/hls/0123456789abcdef0123456789abcdef/status", nil))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "\"not_found\"") {
		t.Fatalf("unknown HLS job: %d %s", w.Code, w.Body.String())
	}
}

func TestHLSProbeResultIsAvailableForAudioTrackSelection(t *testing.T) {
	session := &TorrentSession{}
	probe := &ProbeResult{Duration: 1800, Streams: []ProbeStream{
		{Index: 0, Type: "video", Codec: "h264"},
		{Index: 1, Type: "audio", Codec: "aac"},
		{Index: 2, Type: "audio", Codec: "aac"},
	}}
	cacheSessionProbe(session, 4, probe)
	session.ProbeMu.Lock()
	actual := session.ProbeCache[4]
	session.ProbeMu.Unlock()
	if actual == nil || len(actual.Streams) != 3 ||
		actual.Streams[1].Index != 1 || actual.Streams[2].Index != 2 {
		t.Fatalf("HLS probe cache lost distinct audio stream indexes: %+v", actual)
	}
	cacheSessionProbe(session, -1, nil)
	if len(session.ProbeCache) != 1 {
		t.Fatalf("invalid probe altered session cache: %+v", session.ProbeCache)
	}
}

func TestStartupCacheGatesPlaylistUntilSelectedFileReady(t *testing.T) {
	j := &hlsPlayback{id: "0123456789abcdef0123456789abcdef", generation: 1,
		state: "ready", playlistReady: true, startupCachePercent: 9,
		progress: MediaSourceProgress{FileBytesTotal: 1000, FileBytesCompleted: 89}}
	before := j.snapshot()
	if before.PlaylistURL != nil || before.StartupCacheReady || before.StartupCacheBytesRequired != 90 || !before.PlaylistReady {
		t.Fatalf("early manifest: %+v", before)
	}
	j.mu.Lock()
	j.progress.FileBytesCompleted = 90
	j.mu.Unlock()
	after := j.snapshot()
	if !after.StartupCacheReady || after.PlaylistURL == nil {
		t.Fatalf("ready manifest hidden: %+v", after)
	}
}
