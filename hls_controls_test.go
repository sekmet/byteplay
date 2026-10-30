package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testHLSControl(m *hlsManager, id, action string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	m.handler(w, httptest.NewRequest(http.MethodPost, "/api/v1/hls/"+id+"/control", strings.NewReader("{\"action\":\""+action+"\"}")))
	return w
}
func TestM6SeekValidationPreservesCurrentGeneration(t *testing.T) {
	m := newHLSManager(t.TempDir(), "ffmpeg")
	id := "0123456789abcdef0123456789abcdef"
	duration := 60.0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &hlsPlayback{id: id, generation: 1, state: "ready", ctx: ctx, cancel: cancel,
		probe: &ProbeResult{Streams: []ProbeStream{
			{Index: 0, Type: "video", Codec: "h264", PixelFormat: "yuv420p", Width: 1280, Height: 720, FrameRate: 24},
			{Index: 1, Type: "audio", Codec: "aac", Profile: "LC"},
		}}, duration: &duration}
	m.jobs[id] = job
	for _, tc := range []struct{ body, code string }{
		{"{\"seconds\":-1}", "invalid_seek"},
		{"{\"seconds\":60}", "invalid_seek"},
		{"{\"audioStream\":1}", "invalid_seek"},
		{"{\"seconds\":10,\"audioStream\":99}", "invalid_audio_stream"},
		{"{\"seconds\":\"bad\"}", "invalid_request"},
	} {
		w := httptest.NewRecorder()
		m.handler(w, httptest.NewRequest(http.MethodPost, "/api/v1/hls/"+id+"/seek", strings.NewReader(tc.body)))
		if w.Code != 400 || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("%s => %d %s", tc.body, w.Code, w.Body.String())
		}
		if m.get(id) != job || job.snapshot().Generation != 1 || ctx.Err() != nil {
			t.Fatal("rejected seek cancelled or replaced current generation")
		}
	}
}
func TestM6PauseGraceEvenWithManifestPolling(t *testing.T) {
	m := newHLSManager(t.TempDir(), "unused")
	m.pauseGrace = 40 * time.Millisecond
	id := "0123456789abcdef0123456789abcdef"
	dir := filepath.Join(m.root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := "#EXTM3U\n#EXTINF:4,\nsegment-000000.ts\n#EXTINF:4,\nsegment-000001.ts\n"
	if err := os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"segment-000000.ts", "segment-000001.ts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("TS"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &hlsPlayback{id: id, generation: 1, dir: dir, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), state: "ready", playlistReady: true, lastDemand: time.Now()}
	m.jobs[id] = job
	go func() { <-ctx.Done(); close(job.done) }()
	for i := 0; i < 2; i++ {
		if w := testHLSControl(m, id, "pause"); w.Code != 200 {
			t.Fatalf("pause=%d %s", w.Code, w.Body.String())
		}
	}
	for i := 0; i < 4; i++ {
		w := httptest.NewRecorder()
		m.handler(w, httptest.NewRequest(http.MethodGet, "/api/v1/hls/"+id+"/g/1/index.m3u8", nil))
		if w.Code != 200 {
			t.Fatalf("playlist poll=%d", w.Code)
		}
		time.Sleep(12 * time.Millisecond)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && job.snapshot().State != "paused_idle" {
		time.Sleep(time.Millisecond)
	}
	if job.snapshot().State != "paused_idle" || ctx.Err() != context.Canceled {
		t.Fatalf("no 90s-grace behavior: %+v", job.snapshot())
	}
	w := testHLSControl(m, id, "resume")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "seek_required") {
		t.Fatalf("resume without position=%d %s", w.Code, w.Body.String())
	}
}
func TestM6ResumeWithinGraceCancelsTimer(t *testing.T) {
	m := newHLSManager(t.TempDir(), "unused")
	m.pauseGrace = 25 * time.Millisecond
	id := "0123456789abcdef0123456789abcdef"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := &hlsPlayback{id: id, generation: 1, state: "ready", ctx: ctx, cancel: cancel}
	m.jobs[id] = job
	if w := testHLSControl(m, id, "pause"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := testHLSControl(m, id, "resume"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	time.Sleep(55 * time.Millisecond)
	if ctx.Err() != nil || job.snapshot().State != "ready" {
		t.Fatal("resume failed to cancel timer")
	}
}
func TestM6ManifestPollingDoesNotExtendDemand(t *testing.T) {
	m := newHLSManager(t.TempDir(), "unused")
	m.idleTimeout = time.Minute
	id := "0123456789abcdef0123456789abcdef"
	dir := filepath.Join(m.root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"index.m3u8":        "#EXTM3U\n#EXTINF:4,\nsegment-000000.ts\n#EXTINF:4,\nsegment-000001.ts\n",
		"segment-000000.ts": "TS", "segment-000001.ts": "TS",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	job := &hlsPlayback{id: id, generation: 1, dir: dir, state: "ready", playlistReady: true, lastDemand: time.Now().Add(-3 * time.Minute)}
	m.jobs[id] = job
	get := func(path string) int {
		w := httptest.NewRecorder()
		m.handler(w, httptest.NewRequest(http.MethodGet, "/api/v1/hls/"+id+"/g/1/"+path, nil))
		return w.Code
	}
	if code := get("index.m3u8"); code != 200 {
		t.Fatalf("playlist=%d", code)
	}
	if !m.idleExpired(job, time.Now()) {
		t.Fatal("playlist incorrectly refreshed demand")
	}
	if code := get("segment-000000.ts"); code != 200 {
		t.Fatalf("segment=%d", code)
	}
	if m.idleExpired(job, time.Now()) {
		t.Fatal("segment failed to refresh demand")
	}
}
func TestM6StaleGenerationURLAndPointerGone(t *testing.T) {
	m := newHLSManager(t.TempDir(), "unused")
	id := "0123456789abcdef0123456789abcdef"
	active := &hlsPlayback{id: id, generation: 3, startSeconds: 17.5, state: "preparing"}
	m.jobs[id] = active
	if s := active.snapshot(); s.Generation != 3 || s.StartSeconds != 17.5 || s.PlaylistURL != nil {
		t.Fatalf("bad generation status %+v", s)
	}
	w := httptest.NewRecorder()
	m.handler(w, httptest.NewRequest("GET", "/api/v1/hls/"+id+"/g/2/index.m3u8", nil))
	if w.Code != http.StatusGone {
		t.Fatalf("old generation=%d", w.Code)
	}
	old := &hlsPlayback{id: id, generation: 2, state: "ready", playlistReady: true}
	w = httptest.NewRecorder()
	m.serveMedia(w, httptest.NewRequest("GET", "/api/v1/hls/"+id+"/g/2/index.m3u8", nil), old, "2", "index.m3u8")
	if w.Code != http.StatusGone {
		t.Fatalf("old job pointer=%d", w.Code)
	}
}
