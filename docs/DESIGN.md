# BytePlay · Midnight Current Design Contract

Specification: BP-L3-2026-10-29 v0.1.1 §11 and M7. Date: 2026-10-30.

## Visual system

- Background: midnight #07151E; surfaces: deep teal graphite #0D222B and raised #122A34.
- Text: #E9F5F5; secondary #96ADB5; accent #2DE0C0 / strong #0AA98D.
- Preparing/encoding: restrained warm #FFB56B; errors #FF777D; dividers #24404A.
- Responsive system-first typography; no third-party runtime fonts or fetched artwork.
- Original BytePlay symbol: outlined directional polygon and solid play triangle, also supplied as an SVG favicon. No upstream BitPlay/Webtor marks.

## Hierarchy and behavior

Home: 1200px aligned header/content, centered 800px hero and 720px form, exact required hero copy, magnet/infohash entry, explicit Paste, file-drop with 10 MB maximum, optional indexer search only when a provider is enabled, three real steps below fold. No invented marketing statistics.

Watch: maximum 1100px column, New torrent action, real peers/speed/selected file progress, title and actual file selector, Stream / Download original / File info, 16:9 Video.js, full-duration absolute seek control, audio chooser for multiple probed audio streams, torrent-derived captions and local SRT/VTT import. The selected file download link is a ranged source, not a claim that cache is complete.

State sources:
- Server HLS status owns queued / probing / preparing / ready / complete / paused_idle / failed / cancelled and nullable actual metrics.
- Browser Video.js events own playing, paused, buffering and decoder errors. A server ready state never pretends the browser is playing.
- Video.js attaches the application/x-mpegURL source only when two finalized segments are available. Position is mapped using generation.startSeconds plus local currentTime. Buffered seeks stay in the browser; far seeks and audio changes request new HLS generations.
- Play and pause events send explicit idempotent /control calls, and local subtitles are converted to VTT without a server upload.
- Missing speeds, codecs and durations are shown as unknown, not estimated.
- Errors are inline and actionable for malformed magnet, metadata timeout, unsupported file, probe/transcode error, cache limit, indexer failure, clipboard denial and subtitle validation.

## Preservation and maintenance

Existing settings/proxy/indexer/maintenance handlers remain bound to the hidden upstream DOM; the new Settings button opens their existing modal. The new indexer search uses the already-authorized provider APIs. Legacy UI removal is deferred until feature parity and the original settings forms pass full browser checks. No backend contract was renamed.

## Responsive and accessibility

- Desktop: 1920×1080, 1536×864, 1440×900, 1366×768 — aligned header and hero, 720px max form, 1100px watch.
- Tablet: 1024×768, 834×1194, 768×1024 — wrapping action strip and fluid video.
- Mobile: 390×844, 375×812, 320×720, landscape 844×390 — single-column input/drop, large touch targets, no cropped hero and 16:9 video.
- Visible focus, input labels, status/alert roles, keyboard form submission, native file picker, reduced-motion spinner fallback, descriptive errors and Escape semantics require testing.
- No status is conveyed by color alone; no hardcoded fictional torrent progress.

## Gates and evidence

- Frontend pure tests: node --test client/assets/player-timeline.test.mjs client/assets/byteplay-core.test.mjs
- JavaScript syntax: node --check; CSS: npm ci && npm run build
- Chromium rendered smoke: node scripts/verify-ui.cjs against an actual loopback BytePlay server; archived screenshots for human inspection.
- Watch browser smoke uses mock torrent metadata and status for deterministic UI states. A separate real local-seeded Go integration proves torrent → FFmpeg → HLS.
- M7 remains PARTIAL until real HLS Video.js playback in desktop Chrome/Firefox/Safari, completed manual screenshot critique, touch and 200% zoom, subtitle keyboard selection and provider/settings interaction parity pass. Automated smoke screenshots alone do not certify those gates.
