# BytePlay — Source PTS / rendered-frame and A/V sync certification protocol
Date: 2026-10-30
Status: PROPOSED TEST PLAN — NOT CERTIFIED
Source of truth: BP-L3-2026-10-29 v0.1.1 (BP-007, T-015, T-017, T-018, T-022, T-023).

## Why existing tests cannot close this gate
Chrome and Firefox have decoded HLS frames with advancing media time. A 30-minute locally seeded MKV has exercised 600s forward / 120s backward seek and distinct stream indexes 1 and 2. A displayed `09:57 / 30:00` after requesting 10m is **honest stream-copy keyframe preroll**, not evidence of ±1s presentation accuracy. Playwright's DOM timeline alone is an insufficient oracle.

## Reference media and timestamps
1. Generate *licensed synthetic fixtures* with 30-minute constant-fps and variable-fps scenarios, 4s/8s GOPs, B frames, positive/nonzero start PTS, long MKV and faststart/tail-moov MP4. Include an unambiguous visual frame-number/timecode overlay and frame index manifest generated from the source.
2. Two independent audio tracks should contain distinguishable events: e.g. track 1 440 Hz clicks, track 2 880 Hz clicks at exact known PTS with 20ms impulse starts. Include sub-second cues, SRT/VTT captions with BOM/CRLF, and a test file containing an intentional timestamp offset.
3. Capture source video `best_effort_timestamp_time`, `pkt_dts_time`, `time_base`, keyframe and B-frame metadata, and audio stream/packet timestamps using pinned FFprobe; store JSON, codec version, SHA256, PTS map and expected event timings.
4. Keep a source identity mapping: `source PTS -> HLS stream PTS -> browser requestVideoFrameCallback.mediaTime -> actually decoded timecode` and audio `source impulse PTS -> captured browser audio onset`. Do not compare different clocks without calibration.

## Browser and server test operations
1. For both stream-copy and x264/AAC transcode routes, record FFmpeg command, exit status, process bounds, first two finalized segments, and HLS manifest timing. Parse source and output packet/frame PTS with FFprobe. No stream/generation negative drift, discontinuity or mixed-generation reads.
2. For 30-minute MKV: seek at 600.000s, reverse at 120.000s, repeat three fast seeks, switch absolute FFprobe audio index 1->2 and back, pause/resume, and verify expected seek-generation ids and 410 stale manifests. Test first presented frame and A/V after each.
3. Browser harness must use **actual locally seeded media** (not mocked JSON) and capture Video.js tech events, media errors, `requestVideoFrameCallback` (where available) with mediaTime / expectedDisplayTime / presentationTime and extracted visual marker or frame hash. A `playing` event or currentTime alone never closes accuracy. Browser capture method must independently resolve the visual marker or compare frame content. Calibrate drift and wall clock.
4. Capture audio from the actual decoded output (WebAudio MediaElementAudioSourceNode / AnalyserNode or OS loopback where the browser blocks capture) and compare impulse onset to visually encoded marker timeline. Validate the selected track's frequency and multi-audio identity before and after seek; include sample-rate conversion and channel layout cases. Browser audio clock and video presentation clock must be mapped with documented uncertainty.
5. Run Chrome, Firefox, macOS Safari and iOS Safari where supported. Report each independently; do not treat Chromium emulation as physical Safari. Where frame callback/audio capture is unavailable, run native or OS-loopback instrumentation and mark the limitation explicitly.

## Proposed quantitative acceptance (owner confirmation needed)
- Seek accuracy: absolute source timestamp of the first **intended presented** video frame differs from target by <=1.0s for 600s/120s, both directions, copy and transcode, with no unexplained preroll. If upstream GOP prevents this, decode from prior keyframe and discard/suppress preroll *before presentation*; do not claim that labeling the earlier keyframe is an accurate seek.
- Post-seek A/V sync: audio onset relative to matching visual event <=100ms absolute; verify ten events per seek/test. Calibrate WebAudio/OS-latency first and report measured uncertainty.
- Long playback: 30 minutes without cumulative drift >100ms or backwards PTS, with event samples at beginning, middle and end, covering a mix of GOP boundaries.
- Multi-audio: matching distinct audio identity and onset; inactive stream not mixed after switch.
- Subtitle: correct cue identity and absolute alignment to displayed video within <=250ms and keyboard-selectable SRT/VTT.
- Lifecycle: no leaked FFmpeg process, stale asset exposure, slot retention or hanging torrent reader after three rapid seeks, close/cancel/restart and pause grace. Concurrent run under race detector.
- Every result must include browser/OS/FFmpeg/FFprobe/version, file SHA, fixture duration, source route and 95th percentile/worst offset. Fail closed on missing measurement.

## Important architecture issue to resolve
The code currently sets the HLS generation's `startSeconds` to the actual preceding stream-copy keyframe, and the browser attaches with local playback time 0. That is an honest **effective-start label**, but it may play several seconds of preroll before a 600-second target. The implementation must use a verified source->HLS timestamp mapping and seek/discard preroll (possibly with browser-local initial offset) to satisfy <=1s. Do not merely override the displayed clock to the requested value.

## Relationship to the new startup cache gate
The configurable 9% threshold is based on **selected-file downloaded bytes**, not HLS output or media duration. It adds a startup wait; it **does not certify continuity of buffered playback or accuracy of PTS**. The pinned media test fixtures should run at 0%, 9% and an unsatisfiable quota setting; verify no playlist is exposed before both two finalized segments and the configured byte threshold. Cache quota rejects obviously impossible configured startup targets with a structured 507.

## Certification exit evidence to attach
- `docs/evidence/pts-reference-manifest.json` and annotated deterministic synthetic fixtures (generated by CI; SHA listed)
- `docs/evidence/pts-hls-map.json` for copy and transcode, including keyframe/PTS mapping
- Browser JSON traces/snapshots for each seek and audio event; actual decoded frame hashes/markers
- Per-browser summary with ± tolerances and failure offsets, timestamp source and calibration
- CPU/RSS/session/cancellation results for 30-minute fixtures and a release reviewer decision

No production certification or release should be declared solely from this plan.
