# BytePlay v0.1.1 acceptance tree

Baseline `bb366d5102085477b7270d7982e60712cc23fb41`. Source of truth: BP-L3-2026-10-29 v0.1.1. The old "all PENDING" state was stale after the executed CI runs.

**Core repository evidence:** M3–M5 core media works; M6 30-minute MKV forward/reverse seek and two-track Chrome playback passed #144; M7 browser/keyboard/reflow and M8 short synthetic FFmpeg profile passed #145. M0–M2 and M6–M8 remain **PARTIAL** against full acceptance. See `TODO.md`, `WORKLOG.md` and `docs/evidence/M0.md` … `M8.md`.

**Full acceptance still OPEN:** P0 network-proxy peer/tracker isolation, keyframe/PTS ±1s and A/V subtitle sync, Firefox/Safari/device real playback, sustained deployment CPU/RSS/disk stress, physical free-space accounting, accessibility/visual signoff, approved deployment and protected release.

**Acceptance discipline:** BP-001..BP-012 are individually tracked in `TODO.md`, all unchecked until their complete T-001..T-027 oracle passes. No hidden release certification, no main merge or tag. Browser Firefox work introduced at commit `08d2cd665446101e70e07cb973d67fc034823510` is independently verified by 5/5 green [#146](https://github.com/aisynapze/byteplay/actions/runs/41027689755); Safari/native-device certification remains open.
