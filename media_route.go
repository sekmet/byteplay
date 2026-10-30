package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var ErrMediaProbePending = errors.New("media probe incomplete")
var ErrUnsupportedMedia = errors.New("unsupported media")

type HLSRouteMode string

const (
	HLSRouteCopy           HLSRouteMode = "copy"
	HLSRouteAudioTranscode HLSRouteMode = "audio-transcode"
	HLSRouteVideoTranscode HLSRouteMode = "video-transcode"
)

type HLSLimits struct {
	MaxInputWidth  int
	MaxInputHeight int
}

type MediaProfile struct {
	VideoCodec       string
	VideoPixelFormat string
	VideoProfile     string
	VideoWidth       int
	VideoHeight      int
	VideoIndex       int
	AudioIndex       int
	AudioCodec       string
	AudioProfile     string
	FrameRate        float64
	HasAudio         bool
}

type HLSRoute struct {
	Mode         HLSRouteMode
	Reason       string
	VideoIndex   int
	AudioIndex   int
	VideoEncoder string
	AudioEncoder string
	FrameRate    float64
}

// Audio indexes are absolute FFprobe stream indices, not audio-only offsets.
func MediaProfileFromProbe(probe *ProbeResult, requestedAudio *int) (MediaProfile, error) {
	if probe == nil || len(probe.Streams) == 0 {
		return MediaProfile{}, ErrMediaProbePending
	}
	p := MediaProfile{VideoIndex: -1, AudioIndex: -1}
	seenDefaultAudio := false
	selectedAudioFound := false
	for _, stream := range probe.Streams {
		switch stream.Type {
		case "video":
			if p.VideoIndex == -1 {
				p.VideoIndex = stream.Index
				p.VideoCodec = strings.ToLower(stream.Codec)
				p.VideoPixelFormat = strings.ToLower(stream.PixelFormat)
				p.VideoProfile = stream.Profile
				p.VideoWidth = stream.Width
				p.VideoHeight = stream.Height
				p.FrameRate = stream.FrameRate
			}
		case "audio":
			choose := false
			if requestedAudio != nil {
				choose = stream.Index == *requestedAudio
				if choose {
					selectedAudioFound = true
				}
			} else if p.AudioIndex == -1 || (stream.Default && !seenDefaultAudio) {
				choose = true
			}
			if choose {
				p.AudioIndex = stream.Index
				p.AudioCodec = strings.ToLower(stream.Codec)
				p.AudioProfile = stream.Profile
				p.HasAudio = true
				if stream.Default {
					seenDefaultAudio = true
				}
			}
		}
	}
	if p.VideoIndex < 0 {
		return MediaProfile{}, fmt.Errorf("%w: no video stream", ErrUnsupportedMedia)
	}
	if requestedAudio != nil && !selectedAudioFound {
		return MediaProfile{}, fmt.Errorf("%w: invalid audio stream index", ErrUnsupportedMedia)
	}
	return p, nil
}

// A route is a reproducible plan, not proof that segments are playable.
// The HLS worker must verify FFmpeg output and may retry a failed copy once.
func ChooseHLSRoute(p MediaProfile, limits HLSLimits) (HLSRoute, error) {
	if p.VideoIndex < 0 || p.VideoCodec == "" || p.VideoWidth <= 0 || p.VideoHeight <= 0 {
		return HLSRoute{}, ErrMediaProbePending
	}
	if limits.MaxInputWidth <= 0 || limits.MaxInputHeight <= 0 {
		return HLSRoute{}, errors.New("HLS input limits must be set")
	}
	if p.VideoWidth > limits.MaxInputWidth || p.VideoHeight > limits.MaxInputHeight {
		return HLSRoute{}, fmt.Errorf("%w: source outside encoding budget", ErrUnsupportedMedia)
	}
	knownVideo := map[string]bool{
		"h264": true, "hevc": true, "vp9": true, "av1": true,
		"mpeg2video": true, "mpeg4": true, "vc1": true, "wmv3": true,
	}
	if !knownVideo[p.VideoCodec] {
		return HLSRoute{}, fmt.Errorf("%w: unknown video codec", ErrUnsupportedMedia)
	}
	route := HLSRoute{
		Mode: HLSRouteVideoTranscode, Reason: "video_conversion_required",
		VideoIndex: p.VideoIndex, AudioIndex: -1, VideoEncoder: "libx264",
		AudioEncoder: "none", FrameRate: p.FrameRate,
	}
	canCopyVideo := p.VideoCodec == "h264" &&
		p.VideoPixelFormat == "yuv420p" &&
		!strings.Contains(strings.ToLower(p.VideoProfile), "10")
	if canCopyVideo {
		route.Mode = HLSRouteCopy
		route.Reason = "h264_copy_candidate"
		route.VideoEncoder = "copy"
	}
	if p.HasAudio {
		knownAudio := map[string]bool{
			"aac": true, "mp3": true, "ac3": true, "eac3": true,
			"flac": true, "opus": true, "vorbis": true, "pcm_s16le": true,
		}
		if p.AudioIndex < 0 || !knownAudio[p.AudioCodec] {
			return HLSRoute{}, fmt.Errorf("%w: unknown audio codec", ErrUnsupportedMedia)
		}
		route.AudioIndex = p.AudioIndex
		canCopyAudio := p.AudioCodec == "aac" && strings.EqualFold(p.AudioProfile, "LC")
		if canCopyAudio && canCopyVideo {
			route.AudioEncoder = "copy"
		} else {
			route.AudioEncoder = "aac"
			if canCopyVideo {
				route.Mode = HLSRouteAudioTranscode
				route.Reason = "audio_conversion_required"
			}
		}
	}
	if route.Mode == HLSRouteVideoTranscode && route.FrameRate <= 0 {
		return HLSRoute{}, ErrMediaProbePending
	}
	return route, nil
}

func parseMediaFrameRate(raw string) float64 {
	parts := strings.SplitN(raw, "/", 2)
	numerator, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || numerator <= 0 {
		return 0
	}
	if len(parts) == 1 {
		return numerator
	}
	denominator, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || denominator <= 0 {
		return 0
	}
	return numerator / denominator
}
