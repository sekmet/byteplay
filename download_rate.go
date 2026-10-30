package main

import "time"

// downloadRateSampler measures torrent-wide byte deltas, never FFmpeg output.
// Values remain unknown until a full observation interval has elapsed.
type downloadRateSampler struct {
	lastBytes int64
	lastTime  time.Time
	value     *float64
}

func (s *downloadRateSampler) observe(bytes int64, at time.Time) {
	if at.IsZero() {
		return
	}
	if s.lastTime.IsZero() || bytes < s.lastBytes || at.Before(s.lastTime) {
		s.lastBytes = bytes
		s.lastTime = at
		s.value = nil
		return
	}
	elapsed := at.Sub(s.lastTime)
	if elapsed < time.Second {
		return
	}
	v := float64(bytes-s.lastBytes) / elapsed.Seconds()
	s.value = &v
	s.lastBytes = bytes
	s.lastTime = at
}
