//go:build integration

package main

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCopySeekEffectiveStart(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("FFprobe not installed")
	}
	path := filepath.Join(t.TempDir(), "gop.mkv")
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24",
		"-t", "24", "-c:v", "libx264", "-preset", "ultrafast",
		"-g", "96", "-keyint_min", "96", "-sc_threshold", "0",
		"-pix_fmt", "yuv420p", "-an", path,
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create legal GOP fixture: %v (%s)", err, output)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatal("GOP fixture missing")
	}
	for _, tc := range []struct{ requested, effective float64 }{
		{0, 0}, {9, 8}, {10, 8}, {11, 8}, {12, 12}, {23, 20},
	} {
		got, err := probeCopySeekStart(ctx, ffprobe, path, tc.requested, "")
		if err != nil || math.Abs(got-tc.effective) > 0.07 {
			t.Fatalf("requested %.3f effective %.3f want %.3f: %v", tc.requested, got, tc.effective, err)
		}
	}
}
