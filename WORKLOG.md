# BytePlay worklog

## 2026-10-29

- Verified main HEAD `bb366d5102085477b7270d7982e60712cc23fb41` exactly matches audited baseline; no drift.
- Created implementation branch. Inspected existing Go server, anacrolix/torrent, FFprobe/remux, vanilla JS, Docker and npm lockfile.
- Added first Phase 0 security changes and regression tests; these are not fully validated.
- Added self-hosted Linux/x64 CI workflow. GitHub Actions query currently shows zero runs; runner labels/capabilities cannot be verified with available permissions.
- Runtime Go 1.26.6, seeded torrent, FFmpeg integration and browser checks have NOT been executed.
- M1 remains incomplete; P0 proxy peer/tracker egress and complete security/error tests are outstanding.
- Next action: run CI on the branch, fix observed build/test regressions, complete M1, then M2–M8.

- Latest observed CI run: https://github.com/aisynapze/byteplay/actions/runs/40951000330 — QUEUED, not passed. No runner binding verified. Tests and formatting still require execution.

- Owner-approved temporary CI policy update: use GitHub-hosted `ubuntu-latest` for BytePlay quality jobs; retain self-hosted runners as a later option. Commit `76c5da86c12f1bff112bae0cfbe6b953018c2151`. Run #40951738080 started on GitHub-hosted infrastructure; full result not yet verified.

## 2026-10-29 — dependency download audit

- CI run #40951738080 failed in explicit `go mod download` after ~195 seconds with HTTP/2 proxy errors for Pion datachannel/DTLS and zombiezen sqlite.
- Removed eager `go mod download` from .github/workflows/ci.yml, using `go build ./...` to fetch only imported build dependencies under `GOFLAGS=-mod=readonly` and preserving setup-go caching.
- CI run #40953063735: gofmt, go build, go mod verify and go vet PASS on a cold GitHub-hosted runner; build fetched 73 modules. Pion datachannel and DTLS were fetched by build; zombiezen.com/go/sqlite was not.
- Dockerfile also had eager download; replaced it with BuildKit Go module/build cache mounts and lazy build with readonly module manifest.
- Unit test T-003 failed because oversized request content length was not checked before invalid multipart parsing; added early 413 ContentLength check while retaining MaxBytesReader for streaming/unknown-length bodies.
- No dependency versions changed; no `go mod tidy` or removal of transitive dependencies without import graph evidence.
- New CI run for latest branch must still complete before full gate status can be recorded.

## 2026-10-29 — GitHub-hosted CI repair verified

- Run #40953448643: all Go/frontend gates passed, FFmpeg availability failed because the GitHub-hosted Ubuntu runner had no ffmpeg binary (`ffmpeg: command not found`).
- Installed ffmpeg/ffprobe using apt in `.github/workflows/ci.yml` before the codec availability checks. Commit: `116132bbae1626a7fe2358736d20349a31a7b31b`.
- Run #40955652051 on that commit: **success** (https://github.com/aisynapze/byteplay/actions/runs/40955652051). Go build/vet/test/race, module verification, gofmt, JS syntax, CSS, ffmpeg install and libx264/AAC/ffprobe checks all passed.
- This is a green CI quality workflow, not full M0-M8 media, security, HLS, or UI release certification.

## 2026-10-29 — M2 session lifecycle initial batch

- Starting from last confirmed green CI, addressed concurrent-add duplicate creation and unsynchronized LastUsed/cleanup issues. Implemented per-infohash flight, retained handler guard, idle-only reap and active-purge denial with tests.
- M2 P1-03/P1-06 cancellation-and-drain/P1-07 SIGTERM still pending; CI for this commit pending.

- M2 P1-07 initial implementation: SIGTERM/SIGINT graceful HTTP drain and bounded timeout; CI/race proof pending. No claim of full FFmpeg teardown proof.

## 2026-10-29 verified CI / outstanding acceptance

- GitHub-hosted [CI #40957319988](https://github.com/aisynapze/byteplay/actions/runs/40957319988) at `522c3f7093ff4c0b026496333f16eb2ae1c5e06c`: **success**, including concurrent infohash add, retention, cleanup and active purge denial tests; `go test -race` passed.
- GitHub-hosted [CI #40957933859](https://github.com/aisynapze/byteplay/actions/runs/40957933859) at `ee2b5084d2775d02db3b6b02b6253bebaf963221`: **success**; synchronous listen, graceful SIGTERM/SIGINT with bounded HTTP Shutdown and fallback Close tested; entire configured Go/frontend/FFmpeg workflow passed.
- P1-01/02 code paths now have unit/race evidence. P1-07 HTTP layer has tests, but real container signal and media shutdown are not certified. Full P1-03 media cancellation, full P1-06 active purge cancellation/drain, crash-safe settings and real seeded torrent integration remain **OPEN**.
- Keep M2 **PARTIAL**, do not mark BP-001..BP-012 fully accepted.

- M2 P1-03/P1-06 follow-on: added session-owned context for Reader.SetContext, FFprobe and FFmpeg; purge cancels and waits up to 10s, retains cache on timeout. In-flight metadata fetch returns 409. Pending runtime CI and seeded-media evidence.

## GitHub-hosted CI evidence — M2 cancellation, 2026-10-29

- [Actions #40958991450](https://github.com/aisynapze/byteplay/actions/runs/40958991450) on commit `2667a4688abf15223af23b954baa520d0c31fd4d`: **completed SUCCESS**.
- Passed Go 1.26.6 compilation, gofmt, `go mod verify`, `go vet ./...`, `go test ./...`, `go test -race ./...`, JavaScript syntax, npm CSS build and FFmpeg/FFprobe encoder checks.
- Implemented: P1-01 synchronized LastUsed and retained handlers; P1-02 in-flight deduplication; P1-03 session-owned reader/probe/remux contexts; P1-06 cancel+drain before cache deletion with bounded 409-preserve behavior; P1-07 signal-triggered, bounded HTTP shutdown.
- Still NOT certified: an actual seeded torrent/FFmpeg cancellation integration run, cancellation of in-flight metadata fetches (currently 409), failure-injection for settings writes, and complete P1-08 status/code matrix. M2 remains PARTIAL, M3–M8 remain OPEN.

## 2026-10-29 — verified M3 local seed and selected-file range

- [GitHub Actions #40960730318](https://github.com/aisynapze/byteplay/actions/runs/40960730318) at 5fbc08ccf1a5f6e6de96ecf5352a109e4188ef45: SUCCESS. Two jobs, including offline local seeder on synthetic 60-second H264/AAC test-pattern clip.
- HTTP status 206 and exact clip bytes 1024–8191 validated at 20 and 100 Mbps rate-limited client configurations; torrent completed only 360448/74780311 and 393216/74780311 bytes, respectively.
- Improved assertions to require selected-file completion below 100%, and expanded sparse sibling padding to 220 MiB to match L3 M3 fixture-size gate. First-byte latency is now reported as a numeric millisecond value. A race-enabled local seed run was added; pending confirmation.
- These prove range reads before full completion; they do NOT certify 2 playable HLS segments, a true 1-minute startup budget, peak buffer or real-web peer behavior.

## 2026-10-29 — M4 route planning code

- Added typed MediaProfileFromProbe and ChooseHLSRoute with H264/AAC copy candidate, AAC-only conversion for AC3/Opus/etc, software x264 route for HEVC/VP9/AV1/high-bit-depth, explicit audio-track selection and bounded video input.
- Extended FFprobe stream metadata (pix_fmt, width, height, avg frame rate). Deterministic matrix tests were added for H264/AAC, H264/AC3, HEVC/AAC, VP9/Opus, high bit depth, invalid source, missing video and absolute audio indexes.
- CI status pending on this code. These route decisions are not actual MPEG-TS HLS output and do not certify playback.

## M4 FFmpeg executable integration additions — 2026-10-29

- A single BuildHLSFFmpegArgs implementation emits deterministic argv for `copy`, `audio-transcode` and `video-transcode`. Copy path omits all x264 encoding options; audio-only conversion preserves H.264. Video conversion uses explicit libx264 ultrafast, 8-bit 420, bounded profile and 4s keyframe cadence.
- CI now creates short synthetic H264/AC3 and MPEG4/AAC MKV fixtures in addition to the H264/AAC MP4. Tagged integration runs FFprobe → route selection → FFmpeg HLS output and verifies ENDLIST, at least two finalized TS segments, and H264/AAC in first segment. Results pending.
- This only validates *local file* media routing; on-demand torrent-backed HLS sessions and generation-aware APIs remain in M5/M6.

## 2026-10-29 — M3/M4 green GitHub-hosted CI evidence

- [CI #40962687772](https://github.com/aisynapze/byteplay/actions/runs/40962687772), source commit `1869c8609734ed7e79aee759b86f98ebb18ee3cd`: **SUCCESS**, both `validate` and `media-source` jobs completed successfully.
- Go 1.26.6: build, verify, vet, unit tests, race detector, frontend syntax/CSS, gofmt, FFmpeg availability all PASS.
- Offline seeded BitTorrent fixture: 244,451,667-byte torrent; validated exact 206 range with only 507,904 bytes completed (20 Mbps) and 458,752 bytes completed (100 Mbps) in non-race run. Selected-file bytes also remained below full size. First-byte latency measured 109 ms / 22 ms for non-race run (one observation, not production promise).
- Race-enabled repeat of M3 partial streaming test PASS; transport throttles are configured local client limits, not field-network benchmarks.
- FFprobe route selection and FFmpeg MPEG-TS HLS: local H264/AAC copy = 6 finalized segments, local H264/AC3 audio transcode = 2 finalized segments, local MPEG4/AAC x264 ultrafast = 5 finalized segments. Playlists contained ENDLIST, segments probe as H264/AAC, and zero temporary outputs remained. Both normal and race-enabled CI executions PASS.
- **Not demonstrated:** live torrent-to-FFmpeg/HLS generation before 100% of selected bytes, generation-qualified HLS serving APIs, browser playback, peak memory/cache accounting, PTS-aligned seeks, or mobile UI. M3 and M4 tests are green; release gate is NOT green.

## 2026-10-29 — M5 async playback manager initial implementation

- Added POST /api/v1/hls/sessions async create, GET /{id}/status, GET/HEAD generation=1 immutable playlists and numbered segments and idempotent DELETE.
- Playback session owns a retained torrent session, context-cancellable FFprobe/FFmpeg, 2 concurrent FFmpeg slots and 1 video transcode slot; caller no longer blocks on metadata or probe.
- Playback ID is cryptographically random 128-bit; output dir 0700. Finalized manifest must contain >=2 referenced non-empty TS files before ready. Strict segment-name check rejects traversal/tmp/unlisted paths. Status and creation responses use typed error/state contracts; no user auth tokens in URLs.
- Added bounded HTTP graceful shutdown cleanup and separate in-memory quota caps (10GiB HLS root, 2GiB playback). Torrent cache aggregate disk quota not yet enforced; generation=1 only and no seek/control endpoints.
- Unit/real seeded end-to-end CI evidence pending; do not mark M5 complete before these pass.

## 2026-10-29 — M5 core real-torrent HLS verification

- Verified source commit: `eb19ec1f7c8cff061dd0562928f7ecc024813ed5`. [GitHub Actions #40964985979](https://github.com/aisynapze/byteplay/actions/runs/40964985979): **completed SUCCESS**, both Go/security/frontend and local seeded media jobs.
- The end-to-end tagged test `TestTorrentBackedHLSReadyBeforeWholeTorrentCompletion` started a local-only seeder, selected the MP4 source via real `/api/v1/torrent/{id}/stream/{fileIndex}`, created the asynchronous playback by POST, polled status until `playlistReady`, fetched the generated immutable generation-1 manifest and two finalized MPEG-TS assets, and asserted torrent-wide completion below total. Normal and `-race` runs passed.
- Observed in previous green run #40964481318: 6 finalized HLS TS segments on the MP4 fixture; at status readiness, torrent-wide completion was 112,197,632 / 244,451,667 bytes in normal run and 92,274,688 / 244,451,667 bytes in race-enabled run. These are **fixture observations**, not buffered bytes, real-network performance, or evidence that the selected media file was incomplete at readiness.
- New `hls_service.go` owns playback IDs (128-bit CSPRNG), 0700 directories, cancellable FFprobe/FFmpeg children, bounded 2 global FFmpeg jobs/1 video transcode, state snapshots and machine-readable error codes. A manifest is withheld until at least two declared, finalized, non-empty TS files exist. Unlisted, temporary, invalid and superseded-generation content is denied. Existing Basic Auth middleware protects the new routes.
- Unit coverage: readiness gating with missing/.tmp segments, GET/HEAD assets, 425 before ready, 410 wrong generation, 404 unknown/unlisted resources, 405 wrong method, 413 body limit, DELETE idempotence, shutdown cancellation, null unmeasured metrics. `go test -race ./...` succeeded.
- **M5 stays PARTIAL**: only generation=1 is implemented. Need bounded source-specific prefetch/codec startup measurements, active worker and completed-playback TTL eviction, full shared-cache accounting (currently HLS-only 10 GiB + per playback 2 GiB), reproducible forced cancellation/orphan process stress, multi-playback/eviction integration, and readiness before entire *selected file* completion. M6 seek/control/pause/audio, M7 UI and M8 production certification are not implemented. Do NOT label BytePlay release-ready.

## 2026-10-29 — M6 implementation and GitHub Actions evidence

**Verified GitHub-hosted run:** [#40966823877](https://github.com/aisynapze/byteplay/actions/runs/40966823877), code commit `b917965d1254be08017f8b52106f9849efe5e6d1` — **SUCCESS**, both standard Go/security/frontend checks and seeded-torrent/FFmpeg integration. `go test -race ./...` passed. The independent Node 22 absolute timeline adapter suite passed **5/5** cases.

**Backend changes:**
- `POST /api/v1/hls/{id}/seek` validates absolute seconds, duration and optional absolute FFprobe audio index BEFORE canceling the current job. It serializes generation restart with an exclusive manager lock, cancels and waits for the old FFmpeg process, retains a torrent session, creates a new generation directory and returns `202 {playbackId,generation,state:"preparing",statusUrl}`. Only the new `playlistUrl` from ready status is published. Old-generation manifests/segments return **410**; stale pointers are rejected under a read lock.
- `POST /api/v1/hls/{id}/control` accepts only idempotent `pause`/`resume`, uses a default 90-second explicit pause grace before cancel + Wait, and responds with `409 seek_required` after a stopped worker instead of guessing a playhead.
- The independent no-demand timeout is 2 minutes and counts HLS segment and explicit control access; manifest-only polling does not reset it. Source-relative FFmpeg `-ss` is generated by the existing bounded argv builder.
- `client/assets/player-timeline.js` implements absolute position mapping, buffered-range-only local seeking and server restart when an absolute target is not buffered (or audio changes). It is standalone, without modifying undocumented Video.js internals.

**Executed evidence:** `TestTorrentBackedHLSReadyBeforeWholeTorrentCompletion` now covers multiple generation restarts, backward and forward absolute seeks on a 60-second synthetic fixture, explicit audio selection, invalid audio rejection without supersession, stale `410` URLs and idempotent controls. Normal and race-enabled tagged integration tests passed. Go unit tests verify paused worker drain despite manifest polling, grace cancellation by resume, invalid seek rejection without state mutation, no-demand accounting and stale pointer denial.

**Acceptance boundaries — M6 remains PARTIAL:** Current `startSeconds` carries the requested offset and **has not been demonstrated to equal the effective source keyframe/first audible PTS**. No claim of ±1s accurate seek or GOP-accurate stream-copy seek is made. The mandatory 30-minute MKV time-marker forward/backward seek, multiple audio-track selection rather than respecifying the same AAC track, subtitle synchronization, three rapid concurrent-seek stress/no-orphan-process assertions, Video.js pause intent integration and touch/browser E2E remain OPEN. The adapter is **not yet wired into the production watch UI**. Do not mark M6 or the release complete.

## 2026-10-30 — M7 branded home/watch UI and Chromium gates

- Built original BytePlay Midnight Current interface in client/index.html, client/assets/byteplay.css, byteplay-app.js, byteplay-core.mjs and original SVG favicon, retaining the old DOM and settings/indexer handlers for backward compatibility.
- Connected home magnet/infohash and .torrent conversion; conditional provider search; file chooser; watch status/progress; generation-aware Video.js HLS source; absolute player timeline; video/audio selection, local and torrent subtitles, ranged original download and file info.
- Added JS file/status/SRT tests, generation snapshot fencing (no obsolete HLS playlist after seek), reduced-motion/responsive CSS, and Node-based timeline tests. Video.js remains pinned in vendored comment at 8.24.0.
- Added scripts/verify-ui.cjs rendered Chromium test with real loopback BytePlay server, deterministic mocked watch metadata, archived six full-page screenshots. Inspected desktop/mobile/landscape images for hierarchy, contrast, no clipping and alignment; manual independent UI sign-off pending.
- Observed sporadic disconnected local loopback peer on one 100Mbps race subtest (#41006463850); added narrow test-only reconnection while ActivePeers=0 rather than weakening byte assertions. Last two runs green.
- Latest CI [#41007070375](https://github.com/aisynapze/byteplay/actions/runs/41007070375) **SUCCESS**: Go/frontend, seeded media/race, and Chromium UI jobs.
- M7 is PARTIAL, not release-ready; real browser HLS playback, cross-browser, zoom and human interaction tests remain open.

## 2026-10-30 — Chrome real playback and Docker CI milestone

- Added CI-only fixture host and Playwright real video decoding gate. Earlier run on stripped Chromium headless shell rejected H.264 MSE codec (no playlist fetched). Corrected test to installed Google Chrome and bounded the video.play wait. [#41008831042](https://github.com/aisynapze/byteplay/actions/runs/41008831042): 5/5 SUCCESS. Real H.264/AAC playback observed 34 frames/1.208469 seconds, 640x360 and 4 TS responses.
- Added locked npm CSS multi-stage Docker build and Docker smoke for Basic Auth (401 anonymous), valid FFmpeg/FFprobe/libx264/AAC and shipped CSS. All green.
- Updated README operator steps to byteplay fork, `go run .`, safe listener/auth requirements and local Docker build; avoided implying an unapproved published image.
- Enlarged responsive Chromium smoke viewport matrix: code committed, full new matrix rerun in progress; do not claim newly added viewport checks passed until that run finishes.
- M7 and M8 remain partial, with cross-browser, seek timestamp, resources, eviction, accessibility and release gates not certified.

## 2026-10-30 — Responsive viewport coverage

- [CI #41009203394](https://github.com/aisynapze/byteplay/actions/runs/41009203394) SUCCESS all five jobs. Browser smoke home 11 spec sizes, watch 5 sizes; actual Google Chrome HLS decoding separately green. Preliminary screenshot audit of 375 phone, 834 tablet, 1024 watch and 844 landscape showed no evident clipping. More M7/M8 acceptance remains OPEN.

## 2026-10-30 — Combined disk watermark and playback lifecycle

- Implemented 15-minute no-demand HLS session reaper and periodic minute sweep. Reclaims map slots, cancels/joins owned worker before deleting its generated HLS directory. Recent sessions are preserved.
- Fixed generation-1 directory ancestry bug found via seeded integration: all generations now use `hls-cache/{id}/g/{n}`, with distinct directories and cleanup of superseded generations.
- Added `BITPLAY_CACHE_MAX_BYTES` positive-integer configuration, default 10 GiB, accounting for both downloaded torrent data and HLS output. Fail closed on invalid config; `cache_full` persists after worker cancellation.
- Initial CI red due to expected gofmt/unused import and a real nested-generation deletion failure; corrected with tests. Latest [#41010209989](https://github.com/aisynapze/byteplay/actions/runs/41010209989): 5/5 green including real seeded HLS, normal/race tests and Chrome decoding.
- Atomic reservations, crash-orphan recovery, true disk eviction and stress acceptance remain open; no release tag or merge.

## 2026-10-30 — HLS-writer reservations and crash-restart evidence

## 2026-10-30 — M8 HLS reservations and restart recovery (verified)

- **Verified GitHub Actions:** [#41011597653](https://github.com/aisynapze/byteplay/actions/runs/41011597653), source commit `fe73ac3b8257d1fc9ab4bfd08efe81ad2b2a97e1`, **5/5 SUCCESS**: standard Go/JS/frontend including `go test -race`, locally seeded range and full HLS generation/seek, responsive Chromium, real Chrome decoded H.264 HLS frames, and authenticated Docker image. The same implementation's prior code-only run [#41011387517](https://github.com/aisynapze/byteplay/actions/runs/41011387517) also passed all five gates.
- `hls_retention.go` now serializes HLS-writer admission through `cacheMu` and an owned-generation `reservations` map. Each accepted FFmpeg generation reserves up to its configured maximum additional output (default 2 GiB; capped by a smaller global budget) **before** worker creation. Reservations are transferred by waiting for the previous process to exit and reclaiming the superseded generation, then explicitly reserving the replacement. The worker releases its reservation on exit.
- Accounting is `dirSize(torrent-data) + dirSize(hls-cache) + sum(max(0, reservation - already-written-owned-output))`, so actual segment bytes are **not double-counted**. Running workers still verify measured combined usage and enforce their own reserved output cap. Admission fails with 507 `cache_full` when insufficient space remains, and failed generations do not publish a playable URL.
- New `hls_reservations_test.go` exercises **32 simultaneous reservations** against space for exactly two, double-count protection after writing segments, rejection when torrent usage consumes headroom, preservation of actual disk bytes after release, startup orphan cleanup of generated 32-hex playback IDs, preservation of unrelated files and symlinks, idempotent recovery, and fail-closed behavior for a symlinked cache root or active jobs. Normal and race-enabled Go test suites passed.
- `main.go` invokes `recoverAbandonedCache` before serving any requests, removing only verified internally named playback directories in the HLS root. The **real Docker CI** writes a stale output directory and an unrelated operator file, sends `SIGKILL`, starts the same container and confirms the orphan is deleted while the unrelated file and HTTP Basic Auth protection survive. Verified log: `BYTEPLAY_DOCKER_CRASH_RECOVERY_PASS`.
- **Open/explicit limitations:** Startup recovery assumes a single instance owns the HLS directory and has no distributed lease; mounting the same HLS root into multiple running BytePlay instances is unsupported. Competing **HLS workers** reserve atomically; torrent downloads from independent anacrolix clients do not reserve their future disk bytes, so strict whole-cache oversubscription prevention is **not claimed**. Cache scanning is periodic and not a filesystem block reservation. No long-running stress/peak CPU-RSS, full remote-swarm performance, 30-minute GOP/PTS seek, Firefox/Safari or production certification was executed in this batch. Do not mark release ready.

## 2026-10-30 — Pre-write torrent storage budget and cache ownership (verified)

- [GitHub Actions #41015497190](https://github.com/aisynapze/byteplay/actions/runs/41015497190) code commit `76dafbe6d78324e39752155de60a0200b6790802`: **5/5 SUCCESS** on GitHub-hosted runners (Go/build/vet/unit/race, seeded torrent/FFmpeg/HLS, Chromium responsive home/watch, Google Chrome actual H264 Video.js playback and Docker auth/lock/crash-recovery).
- Added `hls_instance_lock.go`, POSIX `flock` implementation and tests: only one process can claim `hls-cache/.byteplay-instance.lock`; another process fails closed before startup orphan cleanup. The handle is held until process exit; kernel automatically releases it after SIGKILL. Invalid root/lock symlinks are rejected. On unimplemented operating systems startup fails closed rather than silently allowing competing cache deleters. Docker CI now starts a second container with the same HLS volume and explicitly verifies rejection; logs include `BYTEPLAY_SECOND_CACHE_OWNER_REJECTED` and `BYTEPLAY_DOCKER_CRASH_RECOVERY_PASS`.
- Added `torrent_budget_storage.go`: production `initTorrentWithProxy` uses a wrapping `storage.ClientImplCloser` around the existing `anacrolix/storage.NewFile`. The wrapper intercepts torrent piece `WriteAt` before disk allocation and serializes projected apparent-file-length headroom with HLS output reservations using `cacheMu`. Completion, hashing and byte-range reading remain with anacrolix's file backend. The browser HLS integration fixture now uses the same manager for torrent and HLS storage, and continued to decode 640x360 H264/AAC video (34 frames, 4 TS segments) in Google Chrome.
- **Important runtime discovery:** default anacrolix v1.61.0 mmap storage calls `Truncate(file.Length)` when opening a new `.part` file, even for a single 16 KiB request. A first attempt to reserve only requested-chunk growth was insufficient; tests exposed that full-length allocation. The corrected guard conservatively projects **each intersected file's declared full length** before allowing its first write. This preserves the apparent-size cache budget but means files larger than available headroom are rejected instead of partially streamed. The generic source can still stream without *downloading* all bytes of an admissible file, but mmap file length is preallocated.
- Normal/race tests cover first-write whole-file rejection with no output artifact, a small legal video beside a large denied file, shared HLS/torrent headroom, multi-file boundaries, unsafe metadata paths, and clearing stale file-end projections after successful cache purge. A failed write returns `errTorrentCacheFull` to the torrent engine.
- **Scope:** the limit governs BytePlay-owned HLS and torrent writes in one process as measured by *apparent file length*; it is not an OS physical allocated-block quota, does not guarantee underlying free disk space, and cannot protect against other programs or separate app instances modifying `torrent-data`. Advisory OS locks are local-filesystem assumptions and not a distributed lease. The guard may impose additional metadata/file-stat overhead; no CPU or 30-minute streaming performance claim has been measured. Release certification remains OPEN.

## 2026-10-30 — Settings label visibility and browser Played history

- **Verified GitHub-hosted CI [#41017266001](https://github.com/aisynapze/byteplay/actions/runs/41017266001)**, code commit `8034688e86c8dae3b553a5f613064cecc30ac8fd`: all **5/5 PASS** (Go/race/JS/frontend, seeded torrent/FFmpeg/HLS, Chromium responsive UI, real Google Chrome decoding, authenticated Docker).
- **Settings label contrast** in `byteplay.css`: isolate the retained legacy settings modal from the new Midnight Current shell with explicit dark panel, dark foreground for bright selected tab and save buttons, light foreground for dark unselected tab/test buttons, visible hover/focus states. Chromium browser test checks computed WCAG text/background contrast ratio >=4.5 for selected/unselected tabs, test button and save button at all 11 home viewport sizes; settings screenshots captured at 1920x1080 and 390x844. Existing tab actions/settings endpoints unchanged.
- **Played videos**: new `played-history.mjs` pure browser-storage module validates records, drops corrupt/untrusted entries, deduplicates by infohash+selected file index, limits entries to 30, handles unavailable storage without disrupting playback, and supports remove/clear and restart-position calculation. `byteplay-app.js` persists records only after Video.js emits a genuine `playing` event, updates at a bounded ~5-second interval and on pause/pagehide, and renders a semantic, safely text-constructed homepage list. Resume re-adds the saved magnet, reselects the original video file and requests a new generation near the saved absolute timestamp before attaching the initial playlist. Replay re-adds the torrent from position zero; expired server session IDs and HLS playlists are not reused.
- Browser smoke verifies localStorage survives page reload, Resume calls `/api/v1/hls/{id}/seek` at saved position minus 3s, Replay reopens a torrent, and Remove updates the list. The actual seeded Chrome HLS decoding test verifies that real playback writes history and displays it after reloading. History is client-local and stores magnet URLs that may include private tracker tokens; no backend persistence or cross-device sync is claimed.
- Unit tests cover deduplication, most-recent ordering, 30-item cap, corrupt input, invalid/untrusted sources, storage denial, per-file removal, clear, resume lead-in and close-to-end restart. CI remains GREEN. A follow-up `byteplay-app.js` patch additionally preserves last-watched time when duration is missing; its final separate CI confirmation follows the verified run above.
- **Still open**: separate Firefox and Safari HLS playback, larger media seek PTS/AV sync and multi-audio/subtitle interactions, 200% zoom, independent accessibility and visual sign-off, M8 long-run benchmarks/release approval.

## 2026-10-30 — Classic sparse torrent storage and settings/Played UI

- BytePlay now supports two upstream file backends through the existing `TORRENT_STORAGE_DEFAULT_FILE_IO` environment contract. **Classic** grows part-file lengths only to the written piece extent; **default mmap** truncates a part file to its complete declared length on first write. Our guarded `storage.ClientImplCloser` captures the backend mode at construction and projects the appropriate growth before writing under the same quota mutex as HLS reservations.
- Docker runtime defaults to `classic`; local `go run .` can opt in by setting the variable **before starting the process**. This avoids whole-file apparent-size preallocation for initial video pieces without forking `anacrolix/torrent` or adding dependencies. Far-offset writes can still grow an apparent file size beyond the quota and are rejected.
- Added classic-only fresh-process tests running under `TORRENT_STORAGE_DEFAULT_FILE_IO=classic` with race detection: 384-byte declared file in 128-byte piece headroom, denial of a far-offset write without disk expansion, sequential extension, shared HLS headroom, and multi-file piece extents. The existing default-mmap tests remain in the standard Go suite. The real Chrome Video.js integration runs with the classic backend and the same shared HLS/torrent quota wrapper; the locally seeded 20/100 Mbps range test remains a separate gate.
- The user-requested UI changes are independently green in [#41017416802](https://github.com/aisynapze/byteplay/actions/runs/41017416802): Settings button computed contrast, homepage Played history retained locally after actual playback, Resume/Replay including generation seek, remove/clear and the mobile viewport matrix.
- **Remaining release caveats:** Neither mode is an OS-level physical disk quota; independent programs can consume space and classic sparse files may still consume physical blocks as pieces arrive. Classic file I/O may have different CPU/memory profile from mmap, not benchmarked at production scale. This batch does not certify 30-minute MKV PTS seek, Firefox/Safari, accessibility, stress or release.

## 2026-10-30 — Sparse classic file backend and Played UI full gate verification

- **Final GitHub Actions [#41018622199](https://github.com/aisynapze/byteplay/actions/runs/41018622199)**, tested source commit `ec06d56499e7bc6525bc86804ee1d44e48a01615`: **all 5 jobs SUCCESS**, including default Go unit/race, separate race-enabled `TORRENT_STORAGE_DEFAULT_FILE_IO=classic` sparse write tests, 20/100 Mbps seeded-torrent range and HLS codec integrations, responsive Chromium UI, real Google Chrome H264/AAC decoded playback with browser-local history, and Docker authenticated ownership/crash recovery.
- In `torrent_budget_storage.go` the guarded piece writer captures whether the upstream classic backend is selected and projects the requested extent's furthest file offset, rather than the whole file length. Default mmap retains whole-file projection. Both paths remain serialized with the HLS cache reservation ledger. Docker runtime sets `TORRENT_STORAGE_DEFAULT_FILE_IO=classic`; local `go run .` users must set it before process startup to opt in.
- Tests prove a 384-byte declared file can receive its initial 32-byte piece with only 128 bytes available, and a far-offset write that would extend beyond the watermark is denied without writing. Separate coverage tests shared HLS/torrent headroom and a piece spanning two files. This demonstrates *sparse apparent-size growth*, not physical block quotas or production throughput.
- An intermittent disconnected loopback seed in the **race** run #41018173530 was isolated to the local 20Mbps fixture. The harness now permits one fresh leecher/client retry **only** after an actual read-context deadline. Incorrect bytes, missing 206 status, or invalid progress metrics remain immediate failures. The next full run #41018622199 passed.
- **Open**: physical free-space coordination and block-level quota, large files with far-tail MP4 metadata/30-min MKV GOP accuracy, real memory/CPU/peer load and cache pressure measurements, Firefox/Safari, human accessibility approval and authorized production release.

## 2026-10-30 — M6/M7/M8 continuation after CI #132

- [#133](https://github.com/aisynapze/byteplay/actions/runs/41021442920) **5/5 PASS**: shared legacy/HLS FFmpeg process budget, measured torrent delta rate, proxy test redaction and quality-gated Docker publication.
- [#134](https://github.com/aisynapze/byteplay/actions/runs/41022060090) **5/5 PASS**: copy-keyframe effective start oracle and Video.js playback-intent alignment.
- [#136](https://github.com/aisynapze/byteplay/actions/runs/41022573423) **5/5 PASS**: bounded settings requests, truthful SOCKS5 caution, readiness endpoint; formatting corrected before successful gate.
- [#138](https://github.com/aisynapze/byteplay/actions/runs/41023220118) **5/5 PASS**: real Chrome buffered seeks, pause/resume and SRT import.
- [#142](https://github.com/aisynapze/byteplay/actions/runs/41024156873) **5/5 PASS**: 30-minute MKV Chrome HLS far seek near 10m; displayed `09:57 / 30:00`. #143 then failed on initially missing audio tracks.
- [#144](https://github.com/aisynapze/byteplay/actions/runs/41026673267) **5/5 PASS**: HLS result caches FFprobe stream metadata, real fixture capability enabled, audio #1/#2 selectable and reverse seek near 2m displayed `01:57 / 30:00`.
- [#145](https://github.com/aisynapze/byteplay/actions/runs/41027137327) **5/5 PASS**: 720×450@2 DPR desktop reflow proxy and keyboard submission; synthetic 30s HLS FFmpeg profile: copy 0.07s wall/58,224KiB max RSS; x264 ultrafast 1.49s wall/76,720KiB max RSS.
- [#146](https://github.com/aisynapze/byteplay/actions/runs/41027689755) **5/5 PASS** on code `08d2cd665446101e70e07cb973d67fc034823510`: real Firefox MediaSource H.264/AAC HLS decoded **29 frames**, currentTime **1.218469s**, 640×360, one HTTP 200 playlist and two HTTP 200 .ts segments. The existing Chrome 30-minute MKV multi-audio/reverse-seek test also passed in the same job. This is desktop Playwright Firefox, not Safari/mobile certification.
- **Open:** real Safari/device playback, long-duration PTS ±1s and audible track/caption synchronization, full proxy peer/tracker network-isolation/security validation, sustained resource stress and release approval. No tag/main merge.

## 2026-10-30 — Configurable selected-file startup cache (CI verified)

- **GitHub-hosted CI [#150](https://github.com/aisynapze/byteplay/actions/runs/41030741653)** on executable commit `3872f336295d4d240f1008e87a4ac7bc9fba1f09`: **5/5 SUCCESS** (Go unit/race/security/JS, 20/100 Mbps seeded torrent and FFmpeg HLS routes with race, responsive settings UI, actual Chrome+Firefox H264/AAC decoding and 30-minute MKV seek, authenticated Docker).
- Added `StartupCachePercent` to persisted `Settings` with **9% default for existing configuration files**, configurable **0..100** in Settings → Maintenance; 0 disables only the additional wait. Separate JSON-only `POST /api/v1/settings/playback` validates bounds and request size, uses existing atomic settings-file writer, and does not expose proxy/indexer secrets.
- Each new HLS session snapshots the selected percentage, computes the target from **the selected media file's completed torrent bytes**, requests only the **initial selected-file piece range** via `Torrent.DownloadPieces` (never DownloadAll), and withholds the published playlist URL and HTTP manifest/segment access until both >=9% selected bytes and two finalized HLS segments are ready. The frontend shows truthful “Buffering before playback” text and percent progress rather than prematurely starting Video.js.
- Added backend tests for old-settings default, explicit 0%, 9% rounding, nonzero file offsets/piece bounds, malformed 8 KiB-limited POST, and playlist publication under/at threshold; frontend Node attach-rule test and Chromium Settings save/readback test change 9→12→9 on a real local server.
- #147/#148 failed Go formatting; exact `gofmt` output was committed at `c4d9579`. #149 passed 4/5 but the full race-instrumented media suite exceeded its 120s **package command budget** (normal suite passed and browser decoded). The race suite was given a bounded 180s budget only; **#150 retested everything green**.
- **Limits:** cached selected-byte fraction is not a promise of contiguous playable seconds or lack of later stalls. Big media can need substantial startup data; no-peer and quota pressure remain real. The threshold is not PTS/A/V certification or a physical-disk block quota. Existing M6 ±1s source-frame and ±100ms A/V evidence, Safari/device and release approval remain OPEN. See `docs/evidence/PTS-AV-Certification-Plan.md`.
