package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStartupCachePercentDefaultAndOverrides(t *testing.T) {
	if got := configuredStartupCachePercent(Settings{}); got != 9 {
		t.Fatalf("legacy default=%d", got)
	}
	if got := configuredStartupCachePercent(Settings{StartupCachePercent: startupCachePercentPtr(0)}); got != 0 {
		t.Fatalf("disabled=%d", got)
	}
	if got := requiredStartupBytes(101, 9); got != 10 {
		t.Fatalf("round-up=%d", got)
	}
	if got := requiredStartupBytes(1<<50, 100); got != 1<<50 {
		t.Fatalf("large=%d", got)
	}
	if startupCacheReached(100, 8, 9) || !startupCacheReached(100, 9, 9) || !startupCacheReached(100, 0, 0) {
		t.Fatal("incorrect readiness gate")
	}
}
func TestStartupCachePrefetchRangeExcludesOtherFiles(t *testing.T) {
	for _, tc := range []struct {
		offset, size, piece int64
		percent, begin, end int
	}{
		{0, 1000, 100, 9, 0, 1}, {250, 1000, 100, 9, 2, 4},
		{1000, 1000, 100, 9, 10, 11}, {0, 1000, 100, 100, 0, 10},
	} {
		begin, end, ok := startupPieceRange(tc.offset, tc.size, tc.piece, tc.percent)
		if !ok || begin != tc.begin || end != tc.end {
			t.Fatalf("%+v got %d..%d %v", tc, begin, end, ok)
		}
	}
	if _, _, ok := startupPieceRange(0, 1000, 100, 0); ok {
		t.Fatal("0% prefetch")
	}
}
func TestPlaybackSettingsRejectsInvalidValues(t *testing.T) {
	for _, body := range []string{`{}`, `{"startupCachePercent":null}`, `{"startupCachePercent":-1}`,
		`{"startupCachePercent":101}`, `{"startupCachePercent":2.5}`, `{"startupCachePercent":"9"}`,
		`{"startupCachePercent":9,"extra":1}`, `{"startupCachePercent":9}{}`, strings.Repeat("X", 9000)} {
		before := startupCachePercent()
		w := httptest.NewRecorder()
		playbackSettingsHandler(w, httptest.NewRequest(http.MethodPost, "/api/v1/settings/playback", strings.NewReader(body)))
		if w.Code < 400 || startupCachePercent() != before {
			t.Fatalf("invalid request got %d: %.40s", w.Code, body)
		}
	}
}
