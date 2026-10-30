package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// probeCopySeekStart finds the video keyframe where FFmpeg's input -ss
// stream-copy operation begins. It uses the ranged source, not the whole
// torrent, and fails rather than publishing an invented effective time.
func probeCopySeekStart(ctx context.Context, prober, input string, requested float64, internalToken string) (float64, error) {
	if requested == 0 {
		return 0, nil
	}
	if requested < 0 || requested > 360000 || math.IsNaN(requested) || math.IsInf(requested, 0) ||
		prober == "" || input == "" {
		return 0, errors.New("invalid source seek probe")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{
		"-v", "error",
		"-read_intervals", strconv.FormatFloat(requested, 'f', 3, 64) + "%+1",
		"-select_streams", "v:0",
		"-show_packets", "-show_entries", "packet=pts_time,flags",
		"-of", "json",
	}
	if internalToken != "" {
		args = append(args, "-headers", "X-BytePlay-Internal: "+internalToken+"\r\n")
	}
	args = append(args, "-i", input)
	command := exec.CommandContext(ctx, prober, args...)
	command.WaitDelay = 3 * time.Second
	stdout := &limitedWriter{max: 131072}
	command.Stdout = stdout
	command.Stderr = &limitedWriter{max: 4096}
	if err := command.Run(); err != nil {
		return 0, errors.New("source keyframe probe failed")
	}
	var parsed struct {
		Packets []struct {
			PTS   string `json:"pts_time"`
			Flags string `json:"flags"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(stdout.buf.Bytes(), &parsed); err != nil {
		return 0, errors.New("invalid keyframe probe output")
	}
	for _, packet := range parsed.Packets {
		if !strings.Contains(packet.Flags, "K") {
			continue
		}
		seconds, err := strconv.ParseFloat(packet.PTS, 64)
		if err == nil && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) &&
			seconds >= 0 && seconds <= requested+0.05 {
			return seconds, nil
		}
	}
	return 0, errors.New("source keyframe is unavailable")
}
