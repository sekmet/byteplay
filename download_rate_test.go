package main

import (
	"math"
	"testing"
	"time"
)

func TestDownloadRateSamplerUsesMeasuredTorrentDeltas(t *testing.T) {
	start := time.Unix(100, 0)
	var sampler downloadRateSampler
	sampler.observe(4096, start)
	if sampler.value != nil {
		t.Fatal("initial reading must remain unknown")
	}
	sampler.observe(10240, start.Add(500*time.Millisecond))
	if sampler.value != nil {
		t.Fatal("sub-second sample is not a measured rate")
	}
	sampler.observe(106496, start.Add(time.Second))
	if sampler.value == nil || math.Abs(*sampler.value-102400) > 0.01 {
		t.Fatalf("expected measured 102400 B/s, got %v", sampler.value)
	}
	sampler.observe(106496, start.Add(2*time.Second))
	if sampler.value == nil || *sampler.value != 0 {
		t.Fatalf("zero throughput after observation must be measured, got %v", sampler.value)
	}
	sampler.observe(0, start.Add(3*time.Second))
	if sampler.value != nil {
		t.Fatal("counter reset must invalidate stale rate")
	}
}
