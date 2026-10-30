package main

import (
	"errors"
	"testing"
)

func TestChooseHLSRouteMatrix(t *testing.T) {
	base := MediaProfile{
		VideoCodec: "h264", VideoPixelFormat: "yuv420p", VideoProfile: "High",
		VideoWidth: 1920, VideoHeight: 1080, VideoIndex: 0, FrameRate: 24,
		AudioIndex: 1, AudioCodec: "aac", AudioProfile: "LC", HasAudio: true,
	}
	limits := HLSLimits{MaxInputWidth: 3840, MaxInputHeight: 2160}
	tests := []struct {
		name   string
		edit   func(*MediaProfile)
		mode   HLSRouteMode
		video  string
		audio  string
		reject bool
	}{
		{"h264-aac-copy", func(*MediaProfile) {}, HLSRouteCopy, "copy", "copy", false},
		{"h264-ac3", func(p *MediaProfile) { p.AudioCodec = "ac3" }, HLSRouteAudioTranscode, "copy", "aac", false},
		{"hevc-aac", func(p *MediaProfile) { p.VideoCodec = "hevc" }, HLSRouteVideoTranscode, "libx264", "aac", false},
		{"vp9-opus", func(p *MediaProfile) { p.VideoCodec = "vp9"; p.AudioCodec = "opus" }, HLSRouteVideoTranscode, "libx264", "aac", false},
		{"h264-10bit", func(p *MediaProfile) { p.VideoPixelFormat = "yuv420p10le"; p.VideoProfile = "High 10" }, HLSRouteVideoTranscode, "libx264", "aac", false},
		{"h264-no-audio", func(p *MediaProfile) { p.HasAudio = false; p.AudioIndex = -1 }, HLSRouteCopy, "copy", "none", false},
		{"unknown-video", func(p *MediaProfile) { p.VideoCodec = "unknown" }, "", "", "", true},
		{"unknown-audio", func(p *MediaProfile) { p.AudioCodec = "unknown" }, "", "", "", true},
		{"outside-budget", func(p *MediaProfile) { p.VideoWidth = 7680 }, "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := base
			tt.edit(&profile)
			route, err := ChooseHLSRoute(profile, limits)
			if tt.reject {
				if !errors.Is(err, ErrUnsupportedMedia) {
					t.Fatalf("want unsupported, got %+v err=%v", route, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if route.Mode != tt.mode || route.VideoEncoder != tt.video || route.AudioEncoder != tt.audio {
				t.Fatalf("mode=%s video=%s audio=%s want %s %s %s", route.Mode, route.VideoEncoder, route.AudioEncoder, tt.mode, tt.video, tt.audio)
			}
		})
	}
}

func TestMediaProfileUsesAbsoluteAudioIndices(t *testing.T) {
	probe := &ProbeResult{Streams: []ProbeStream{
		{Index: 2, Type: "video", Codec: "h264", PixelFormat: "yuv420p", Width: 1280, Height: 720, FrameRate: 25},
		{Index: 5, Type: "audio", Codec: "aac", Profile: "LC", Default: true},
		{Index: 7, Type: "audio", Codec: "ac3"},
	}}
	p, err := MediaProfileFromProbe(probe, nil)
	if err != nil || p.VideoIndex != 2 || p.AudioIndex != 5 {
		t.Fatalf("default selection %+v err=%v", p, err)
	}
	selected := 7
	p, err = MediaProfileFromProbe(probe, &selected)
	if err != nil || p.AudioIndex != 7 || p.AudioCodec != "ac3" {
		t.Fatalf("explicit selection %+v err=%v", p, err)
	}
	selected = 9
	if _, err := MediaProfileFromProbe(probe, &selected); !errors.Is(err, ErrUnsupportedMedia) {
		t.Fatalf("invalid absolute audio index accepted: %v", err)
	}
}

func TestMediaProfileRejectsUnknownAndMissingVideo(t *testing.T) {
	if _, err := MediaProfileFromProbe(nil, nil); !errors.Is(err, ErrMediaProbePending) {
		t.Fatalf("missing probe should be pending: %v", err)
	}
	probe := &ProbeResult{Streams: []ProbeStream{{Index: 0, Type: "audio", Codec: "aac"}}}
	if _, err := MediaProfileFromProbe(probe, nil); !errors.Is(err, ErrUnsupportedMedia) {
		t.Fatalf("audio-only not rejected: %v", err)
	}
	profile := MediaProfile{VideoCodec: "h264", VideoIndex: 0}
	if _, err := ChooseHLSRoute(profile, HLSLimits{MaxInputWidth: 3840, MaxInputHeight: 2160}); !errors.Is(err, ErrMediaProbePending) {
		t.Fatalf("incomplete video profile should be pending: %v", err)
	}
}
