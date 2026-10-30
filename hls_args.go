package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// HLSFFmpegOptions contains only server-generated paths and validated selectors.
// Never pass a user-controlled output directory or arbitrary FFmpeg flag here.
type HLSFFmpegOptions struct {
	Input          string
	Playlist       string
	SegmentPattern string
	StartSeconds   float64
}

// BuildHLSFFmpegArgs is the only command shape used for future HLS jobs.
// Execution remains separate, with an owned context and concurrency slot.
func BuildHLSFFmpegArgs(route HLSRoute, opts HLSFFmpegOptions) ([]string, error) {
	if opts.Input == "" || opts.Playlist == "" || opts.SegmentPattern == "" {
		return nil, errors.New("HLS input and owned outputs are required")
	}
	if math.IsNaN(opts.StartSeconds) || math.IsInf(opts.StartSeconds, 0) ||
		opts.StartSeconds < 0 || opts.StartSeconds > 360000 {
		return nil, errors.New("invalid HLS start time")
	}
	if route.VideoIndex < 0 || route.VideoIndex > 512 || route.AudioIndex > 512 || route.AudioIndex < -1 {
		return nil, errors.New("invalid FFprobe stream index")
	}
	// Read no faster than 2x playback time. Stream-copy can otherwise
	// generate minutes of unwatched EVENT HLS and force Video.js to join
	// far ahead of the requested generation position.
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-readrate", "2"}
	if opts.StartSeconds > 0 {
		args = append(args, "-ss", strconv.FormatFloat(opts.StartSeconds, 'f', 3, 64))
	}
	args = append(args, "-i", opts.Input, "-map", fmt.Sprintf("0:%d", route.VideoIndex))
	if route.AudioIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:%d", route.AudioIndex))
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-sn", "-dn")
	switch route.Mode {
	case HLSRouteCopy:
		if route.VideoEncoder != "copy" {
			return nil, errors.New("copy route has incorrect video encoder")
		}
		args = append(args, "-c:v", "copy")
	case HLSRouteAudioTranscode:
		if route.VideoEncoder != "copy" {
			return nil, errors.New("audio conversion may not reencode video")
		}
		args = append(args, "-c:v", "copy")
	case HLSRouteVideoTranscode:
		if route.VideoEncoder != "libx264" || route.FrameRate <= 0 ||
			math.IsNaN(route.FrameRate) || math.IsInf(route.FrameRate, 0) || route.FrameRate > 120 {
			return nil, errors.New("video conversion requires valid x264 route and source frame rate")
		}
		gop := int(math.Round(route.FrameRate * 4))
		// Keep source dimensions or downscale to <=1920x1080, never upscale.
		args = append(args,
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
			"-crf", "23", "-maxrate", "4500k", "-bufsize", "9000k", "-threads", "2",
			"-vf", "scale=w='min(1920,iw)':h='min(1080,ih)':force_original_aspect_ratio=decrease,scale=trunc(iw/2)*2:trunc(ih/2)*2",
			"-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop),
			"-sc_threshold", "0", "-force_key_frames", "expr:gte(t,n_forced*4)",
		)
	default:
		return nil, errors.New("unknown HLS route")
	}
	switch route.AudioEncoder {
	case "copy":
		if route.AudioIndex < 0 {
			return nil, errors.New("audio copy requires an audio track")
		}
		args = append(args, "-c:a", "copy")
	case "aac":
		if route.AudioIndex < 0 {
			return nil, errors.New("AAC conversion requires an audio track")
		}
		args = append(args, "-c:a", "aac", "-b:a", "128k", "-ac", "2", "-ar", "48000")
	case "none":
		if route.AudioIndex >= 0 {
			return nil, errors.New("audio track selected but no audio encoder")
		}
	default:
		return nil, errors.New("unknown HLS audio route")
	}
	args = append(args,
		"-f", "hls", "-hls_time", "4", "-hls_list_size", "0",
		"-hls_playlist_type", "event", "-hls_flags", "temp_file",
		"-hls_segment_filename", opts.SegmentPattern, opts.Playlist,
	)
	return args, nil
}
