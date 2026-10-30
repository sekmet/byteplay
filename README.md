# BytePlay

**Torrent-to-HLS playback in your browser.** BytePlay is a single-process Go application with an `anacrolix/torrent` engine, on-demand FFprobe/FFmpeg HLS packaging, selective H.264/AAC transcoding, Video.js 8 playback, and the Midnight Current interface.

This repository evolves the upstream BitPlay project while retaining the existing SOCKS5, Prowlarr, Jackett, subtitles, and settings integrations. Use only media and torrents you are authorized to access. **A proxy setting is not a guarantee of privacy or anonymity.** The app's SOCKS5 option is best-effort for supported HTTP traffic; BitTorrent peer/tracker egress isolation has not been certified. For a fail-closed network requirement, run BytePlay inside a VPN/network namespace with enforced egress boundaries.

> **Release status:** Development branch `implement/byteplay-v0.1.1` is under active verification. GitHub-hosted CI has passed real locally seeded HLS playback in Chrome and a Docker authentication/media smoke test. A tagged image and production release are **not certified or guaranteed to be published**; see `TODO.md` and `docs/evidence/M8.md` for open gates.

## What works

- Open a magnet URI, raw 40-character infohash, or upload a `.torrent` metadata file (10 MiB limit).
- Select a video within a torrent, begin retrieving needed pieces, and play from a growing HLS playlist.
- Stream-copy compatible H.264/AAC files; convert incompatible audio to AAC; convert other supported video to H.264 using `libx264 -preset ultrafast`. Requires FFmpeg and FFprobe.
- Video.js player with generation-based seeking, explicit pause/resume handling, captions, file information, original file download, and multiple-audio selection when present.
- Keep existing settings for SOCKS5 proxy, Prowlarr, Jackett, and session/cache maintenance.
- A **Played videos** section in the homepage that remembers actually played files locally in your browser, with Resume, Replay, Remove, and Clear history controls. The legacy Settings modal now has explicit readable tab/test/save label colors.

## Played history (browser-only)

When video playback actually begins, BytePlay records its filename, selected torrent file index, torrent magnet/infohash, approximate resume position, duration (if known), file size, and last-played time in that browser's `localStorage`. It updates the saved position during playback and on pause/page exit, retaining up to **30 recent videos**. These entries are not stored in BytePlay's server database, are not synced between browsers, and do not keep a torrent session or HLS playlist alive.

From the homepage's **Played videos** section, choose **Resume** (restart HLS near the saved absolute time) or **Replay** (start at zero), remove an entry, or clear the complete list. Both replay paths reopen the saved torrent source and the chosen file through the existing authenticated API, and therefore still require reachable torrent peers and compatible media. Saved magnet links may include private tracker tokens; use **Clear history** on shared computers. Clearing browser/site data also removes the Played history. Browser storage may be disabled or unavailable; video streaming must still work without it. All user-supplied filenames are inserted with text-safe DOM APIs.

Playback availability depends on actual torrent peers, codec support, local resources, and browser support. Downloading torrent data is distinct from creating HLS output; selected-file completion is not required before playback in the verified synthetic fixture. The app does not promise a video will play when the source is unsupported or unavailable.

## Requirements

- Go 1.26.6 toolchain (the module declares a Go 1.25 minimum, but CI uses the pinned toolchain).
- FFmpeg, FFprobe, including the libx264 and AAC encoders, available on `PATH`.
- Node.js 22 and npm for recompiling the Tailwind v4 stylesheet when changing frontend source files.
- Docker with BuildKit for the multi-stage container option.

## Local development

```bash
git clone https://github.com/aisynapze/byteplay.git
cd byteplay
git switch implement/byteplay-v0.1.1
npm ci
npm run build
# Recommended: sparse classic torrent file I/O (set before process startup).
TORRENT_STORAGE_DEFAULT_FILE_IO=classic go run .
```

Open <http://127.0.0.1:3347>. The default listener is loopback-only and does not require credentials. Build with `go run .` rather than `go run main.go`: BytePlay uses multiple Go source files in package main. Go downloads imported module dependencies on demand; `go mod download` is not required as a standalone step.

For access from another computer, **set both basic-auth credentials** and explicitly choose a network listener; prefer HTTPS via a secured reverse proxy rather than directly exposing HTTP credentials:

```bash
BITPLAY_LISTEN_HOST=0.0.0.0 \
BITPLAY_AUTH_USERNAME=byteplay-admin \
BITPLAY_AUTH_PASSWORD='replace-with-a-strong-password' \
go run .
```

The application rejects non-loopback, unauthenticated binding unless an explicit unsafe development opt-in is set. Do not enable unauthenticated LAN mode on an internet-accessible host.

## Docker (build locally; not dependent on an unpublished registry tag)

```bash
docker build -t byteplay:local .
mkdir -p config torrent-data
docker run -d --name byteplay \
  -p 127.0.0.1:3347:3347 \
  -e BITPLAY_LISTEN_HOST=0.0.0.0 \
  -e BITPLAY_AUTH_USERNAME=byteplay-admin \
  -e BITPLAY_AUTH_PASSWORD='replace-with-a-strong-password' \
  -v "$PWD/config:/app/config" \
  -v "$PWD/torrent-data:/app/torrent-data" \
  --restart unless-stopped \
  byteplay:local
```

Open <http://127.0.0.1:3347> and authenticate. The Docker image rebuilds CSS using the locked npm dependencies, compiles the Go executable with module/build caches, and ships FFmpeg/FFprobe without Node or the Go toolchain. For access from other computers, deliberately change the host-side `-p` binding **only** after configuring authentication and an appropriate HTTPS ingress.

`config/settings.json` stores persistent settings; `torrent-data` stores downloaded torrent pieces. The HLS cache lives in `./hls-cache` inside the application runtime (separate from persistent `torrent-data`). Its sessions expire after 15 minutes with no genuine demand, and superseded HLS generations are removed. The combined disk watermark counts both roots. Competing HLS generations reserve their permitted output space before FFmpeg runs; already written bytes count inside the reservation, and the reservation is released on worker exit. At startup BytePlay removes abandoned, internally named HLS playback directories; unrelated files and symlinks are preserved. **This recovery assumes one BytePlay instance owns the HLS directory**: do not share one HLS cache volume between independently running servers. This is not archival storage. Torrent piece writes now use a guarded file-storage adapter sharing the same cache ledger as HLS workers. The Docker runtime sets `TORRENT_STORAGE_DEFAULT_FILE_IO=classic` to use the upstream **classic file I/O**, which grows a `.part` file only to the farthest written piece offset and can therefore fetch initial video pieces even when the full file size exceeds the configured budget. BytePlay's guarded storage projects that sparse growth before each write under the shared HLS reservation mutex. For a local `go run .` without that environment variable, anacrolix defaults to **mmap**, which preallocates the whole file's apparent length before the first piece; BytePlay conservatively reserves that full length and rejects the write when capacity is unavailable. Sparse mode does not make far-offset seeks free: a jump near the end may still extend the apparent file size past the budget and be rejected. This is a single-process apparent-file-length limit, **not** an OS-level disk-block quota or protection against unrelated processes writing to either data directory. Container teardown or cache purge may remove temporary playback output; do not treat it as archival media storage.

The intended future published image name is `ghcr.io/aisynapze/byteplay`, contingent on release certification. Do not assume a `:latest` tag is already published or production-approved.

## Playback startup cache threshold

**Default: 9% of the selected video's downloaded bytes.** BytePlay requests the first needed selected-file pieces alongside FFprobe/FFmpeg and attaches Video.js only once this byte threshold and at least two finalized HLS segments are ready. This is **not** a percentage of the entire torrent or HLS output directory. Settings → Maintenance permits **0–100%**; **0% disables the additional startup gate**. It persists in `config/settings.json` and applies only to new playbacks. For a large selected video, 9% may require hundreds of MiB, slow startup or exceed the cache quota; this does not guarantee no later rebuffering.

## Settings and safety

| Environment variable | Purpose |
|---|---|
| `BITPLAY_LISTEN_HOST` | Defaults to `127.0.0.1` locally; Docker explicitly sets `0.0.0.0` |
| `BITPLAY_AUTH_USERNAME` / `BITPLAY_AUTH_PASSWORD` | Required pair for an authenticated network listener; protects UI, API, settings and streaming routes |
| `BITPLAY_ALLOW_UNAUTHENTICATED_LAN=1` | Dangerous development-only bypass; never use for a public deployment |
| `BITPLAY_MAX_REMUX` | Existing remux concurrency control (the new HLS worker also has explicit bounded slots) |
| `BITPLAY_CACHE_MAX_BYTES` | Positive-integer combined budget for `torrent-data` and `hls-cache`; default 10737418240 bytes (10 GiB). HLS output reservations and torrent piece growth checks are serialized within one BytePlay instance. The mmap backend reserves full file lengths; this does not constitute filesystem-level free-space or multi-instance allocation control. |
| `TORRENT_STORAGE_DEFAULT_FILE_IO` | Set to `classic` **before process startup** for sparse partial-file writes (default in the Docker runtime); unset uses anacrolix mmap and requires whole-file allocation headroom. This env variable is read by the library during package initialization, not switchable at runtime. |

The Settings UI retains proxy, search-provider credentials, connection tests, and maintenance actions. Secret fields are redacted from settings reads; successful configuration does **not** establish that all host traffic is routed through a VPN. Avoid copying real credentials into CI logs or bug reports.

## Verification

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./...
node --test client/assets/player-timeline.test.mjs client/assets/byteplay-core.test.mjs
npm ci && npm run build
```

The GitHub-hosted CI workflow `.github/workflows/ci.yml` additionally verifies:
- Separate race-enabled `TORRENT_STORAGE_DEFAULT_FILE_IO=classic` tests: small video-piece writes under a budget smaller than the declared file size, rejection of over-budget far-offset writes, and HLS/torrent shared headroom. The real Chrome torrent-backed HLS test also uses the classic backend; default mmap whole-file rejection tests remain independent.

- A locally seeded legal H.264/AAC torrent with FFmpeg HLS, codec-route fixtures, segment readiness and generation-safe seeks.
- Responsive home/watch UI and settings modal in headless Chromium.
- **Real Video.js HLS decoding in installed Google Chrome**: the test requires downloaded playlist/segments, decoded frames, and advancing playback time. Playwright's bundled Chromium headless shell does not necessarily ship H.264 codecs and must not be used as a substitute for the Chrome media gate.
- A locally built Docker image starts with authentication, rejects anonymous API access, exposes FFmpeg/libx264/AAC, and contains freshly built CSS.

The real-browser fixture is **integration-test-only**, bound to loopback and not included in normal binaries. Tests use generated sine/test-pattern media rather than downloaded copyrighted videos.

**Cache-owner restrictions:** Linux, macOS and supported BSD systems use an advisory OS file lock held for the process lifetime. The application fails closed when this lock cannot be obtained or the root/lock file is a symlink. Do not deploy multiple BytePlay instances on the same HLS cache directory, or rely on network filesystems that do not guarantee advisory locks. Other operating systems require an audited native lock implementation before startup is allowed.


## Open release gates

BytePlay remains pre-release until real-media Firefox/Safari coverage, 30-minute MKV/keyframe/PTS seek accuracy, multi-audio and subtitle interaction checks, CPU/RSS/performance and physical disk-block/space-pressure benchmarks, broader accessibility/mobile viewports, and independent production review are completed. The recommended classic backend supports partial allocation of larger files, but its far-offset seek behavior, startup latency and sustained resource profile still require production-size benchmarks. Unset/default mmap continues to require whole-file apparent-size headroom. Evidence and outstanding checks are tracked in `docs/evidence/`, `WORKLOG.md`, and `TODO.md`.

Original upstream project: [aculix/bitplay](https://github.com/aculix/bitplay). This fork is branded BytePlay.
