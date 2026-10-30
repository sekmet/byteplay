package main

import (
	"math"
	"reflect"
	"slices"
	"testing"
)

func TestBuildHLSFFmpegArgsRouteProfiles(t *testing.T) {
	opts := HLSFFmpegOptions{
		Input:          "http://127.0.0.1:3347/api/v1/torrent/fixture/stream/0",
		Playlist:       "/tmp/byteplay-owned/index.m3u8",
		SegmentPattern: "/tmp/byteplay-owned/segment-%06d.ts",
	}
	cases := []struct {
		name   string
		route  HLSRoute
		video  []string
		audio  []string
		absent []string
	}{
		{"copy", HLSRoute{Mode: HLSRouteCopy, VideoIndex: 0, AudioIndex: 1, VideoEncoder: "copy", AudioEncoder: "copy"},
			[]string{"-c:v", "copy"}, []string{"-c:a", "copy"}, []string{"libx264", "ultrafast", "-crf"}},
		{"audio-only", HLSRoute{Mode: HLSRouteAudioTranscode, VideoIndex: 0, AudioIndex: 2, VideoEncoder: "copy", AudioEncoder: "aac"},
			[]string{"-c:v", "copy"}, []string{"-c:a", "aac"}, []string{"libx264", "-crf"}},
		{"video-transcode", HLSRoute{Mode: HLSRouteVideoTranscode, VideoIndex: 0, AudioIndex: 1, VideoEncoder: "libx264", AudioEncoder: "aac", FrameRate: 25},
			[]string{"-c:v", "libx264"}, []string{"-c:a", "aac"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := BuildHLSFFmpegArgs(tc.route, opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range [][]string{tc.video, tc.audio, {"-readrate", "2"}, {"-hls_flags", "temp_file"}, {"-hls_playlist_type", "event"}, {"-hls_time", "4"}} {
				if !containsAdjacent(args, want) {
					t.Fatalf("argv missing %v: %v", want, args)
				}
			}
			for _, bad := range tc.absent {
				if slices.Contains(args, bad) {
					t.Fatalf("unexpected %q in argv: %v", bad, args)
				}
			}
			if tc.route.Mode == HLSRouteVideoTranscode {
				if !containsAdjacent(args, []string{"-preset", "ultrafast"}) || !containsAdjacent(args, []string{"-g", "100"}) {
					t.Fatalf("x264 route omitted bounded preset/GOP: %v", args)
				}
			}
			last := args[len(args)-1]
			if !reflect.DeepEqual(last, opts.Playlist) {
				t.Fatalf("playlist must be last argument: %v", args)
			}
		})
	}
}

func containsAdjacent(all, wanted []string) bool {
	for i := 0; i+len(wanted) <= len(all); i++ {
		if slices.Equal(all[i:i+len(wanted)], wanted) {
			return true
		}
	}
	return false
}

func TestBuildHLSFFmpegArgsRejectsUnsafeInputs(t *testing.T) {
	route := HLSRoute{Mode: HLSRouteCopy, VideoIndex: 0, AudioIndex: 1, VideoEncoder: "copy", AudioEncoder: "copy"}
	opts := HLSFFmpegOptions{Input: "in", Playlist: "out.m3u8", SegmentPattern: "segment-%06d.ts"}
	if _, err := BuildHLSFFmpegArgs(route, opts); err != nil {
		t.Fatal(err)
	}
	opts.StartSeconds = math.Inf(1)
	if _, err := BuildHLSFFmpegArgs(route, opts); err == nil {
		t.Fatal("infinite seek offset accepted")
	}
	opts.StartSeconds = 0
	opts.Input = ""
	if _, err := BuildHLSFFmpegArgs(route, opts); err == nil {
		t.Fatal("missing input accepted")
	}
}
