//go:build integration

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHLSFFmpegRouteFixtureMatrix(t *testing.T) {
	root := os.Getenv("BYTEPLAY_TEST_FIXTURE_ROOT")
	if root == "" {
		t.Skip("requires generated synthetic FFmpeg fixtures")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	probeBin, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	previousProbe := ffprobePath
	ffprobePath = probeBin
	t.Cleanup(func() { ffprobePath = previousProbe })

	for _, tc := range []struct {
		filename string
		mode     HLSRouteMode
	}{
		{"clip.mp4", HLSRouteCopy},
		{"audio-ac3.mkv", HLSRouteAudioTranscode},
		{"video-mpeg4.mkv", HLSRouteVideoTranscode},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			input := filepath.Join(root, tc.filename)
			probe, err := runFFprobe(ctx, input)
			if err != nil {
				t.Fatalf("FFprobe %s: %v", tc.filename, err)
			}
			profile, err := MediaProfileFromProbe(probe, nil)
			if err != nil {
				t.Fatal(err)
			}
			route, err := ChooseHLSRoute(profile, HLSLimits{MaxInputWidth: 3840, MaxInputHeight: 2160})
			if err != nil || route.Mode != tc.mode {
				t.Fatalf("unexpected HLS route %+v err=%v expected=%s", route, err, tc.mode)
			}
			output := t.TempDir()
			playlist := filepath.Join(output, "index.m3u8")
			args, err := BuildHLSFFmpegArgs(route, HLSFFmpegOptions{
				Input:          input,
				Playlist:       playlist,
				SegmentPattern: filepath.Join(output, "segment-%06d.ts"),
			})
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, ffmpeg, args...)
			command.WaitDelay = 3 * time.Second
			out, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("FFmpeg route %s failed: %v (%s)", tc.mode, err, string(out))
			}
			manifest, err := os.ReadFile(playlist)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(manifest), "#EXTM3U") || !strings.Contains(string(manifest), "#EXT-X-ENDLIST") {
				t.Fatalf("playlist is incomplete: %s", string(manifest))
			}
			files, err := filepath.Glob(filepath.Join(output, "segment-*.ts"))
			if err != nil || len(files) < 2 {
				t.Fatalf("need >=2 finalized TS files, got %d, err=%v", len(files), err)
			}
			tmp, err := filepath.Glob(filepath.Join(output, "*.tmp"))
			if err != nil || len(tmp) != 0 {
				t.Fatalf("unfinished segment files after success: %v, err=%v", tmp, err)
			}
			segments := strings.Count(string(manifest), ".ts")
			if segments < 2 || segments != len(files) {
				t.Fatalf("playlist/segments mismatch: declared=%d files=%d", segments, len(files))
			}
			first, err := exec.CommandContext(ctx, probeBin, "-v", "error", "-show_entries",
				"stream=codec_name", "-of", "csv=p=0", files[0]).CombinedOutput()
			if err != nil {
				t.Fatalf("first TS cannot be probed: %v %s", err, string(first))
			}
			if !strings.Contains(string(first), "h264") || !strings.Contains(string(first), "aac") {
				t.Fatalf("first segment does not contain expected H264/AAC: %s", string(first))
			}
			t.Logf("hls_route=%s finalized_segments=%d source=%s", tc.mode, len(files), tc.filename)
		})
	}
}
