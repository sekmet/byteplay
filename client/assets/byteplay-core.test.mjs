import test from "node:test";
import assert from "node:assert/strict";
import {
  canAttachHLS, validateTorrentFile, validateCaptionFile, MAX_TORRENT_BYTES, videoFiles, captionFiles,
  readableError, formatBytes, formatTime, stateText, srtToVtt, isCurrentHLSStatus
} from "./byteplay-core.mjs";

test("torrent validation rejects empty, wrong extension, oversized metadata", () => {
  assert.match(validateTorrentFile(null), /\.torrent/);
  assert.match(validateTorrentFile({ name: "source.mp4", size: 1000 }), /\.torrent/);
  assert.match(validateTorrentFile({ name: "sample.torrent", size: MAX_TORRENT_BYTES + 1 }), /10 MB/);
  assert.match(validateTorrentFile({ name: "sample.torrent", size: 0 }), /empty/);
  assert.equal(validateTorrentFile({ name: "sample.TORRENT", size: 200 }), null);
});
test("available videos and subtitle files come only from valid API data", () => {
  const files = [
    { index: 3, name: "movie.mkv", size: 10000 },
    { index: 1, name: "captions.srt", size: 200 },
    { index: 2, name: "subtitles.vtt", size: 100 },
    { index: -1, name: "readme.txt", size: 50 },
    { name: "bad.webm", size: 100 },
    { index: 8, name: "file.mp4", size: NaN }
  ];
  assert.deepEqual(videoFiles(files).map(x => x.index), [3]);
  assert.deepEqual(captionFiles(files).map(x => x.index), [1, 2]);
  assert.deepEqual(videoFiles({}), []);
});
test("API error handling recognizes prior and new error shapes", () => {
  assert.equal(readableError({ error: "Legacy error" }), "Legacy error");
  assert.equal(readableError({ error: { code: "cache_full", message: "Storage full" } }), "Storage full");
  assert.equal(readableError({}), "The server could not complete that request.");
});
test("status copy does not fabricate playback speed or browser playing state", () => {
  assert.equal(stateText({ state: "ready" }).title, "Ready to play");
  assert.equal(stateText({ state: "ready" }, "buffering").title, "Buffering");
  assert.equal(stateText({ state: "ready" }, "playing").title, "Playing");
  assert.equal(stateText({ state: "preparing", mode: "video-transcode" }).title, "Transcoding video");
  assert.equal(stateText({ state: "preparing", mode: "audio-transcode" }).title, "Converting audio");
  assert.equal(stateText({ state: "paused_idle" }).title, "Paused");
  assert.equal(stateText({ state: "failed", error: { message: "Unsupported codec" } }).detail, "Unsupported codec");
});
test("time and byte formatting is bounded and works with unknown measurements", () => {
  assert.equal(formatBytes(null), "—");
  assert.equal(formatBytes(-2), "—");
  assert.equal(formatBytes(0), "0 B");
  assert.equal(formatTime(undefined), "—");
  assert.equal(formatTime(125), "02:05");
  assert.equal(formatTime(3601), "1:00:01");
});
test("subtitles accept BOM/CRLF and convert SRT commas to VTT dots", () => {
  const srt = "\uFEFF1\r\n00:00:01,000 --> 00:00:02,500\r\nHello\r\n\r\n2\r\n00:00:03,000 --> 00:00:04,000\r\nWorld";
  const out = srtToVtt(srt);
  assert.match(out, /^WEBVTT\n\n/);
  assert.match(out, /00:00:01\.000 --> 00:00:02\.500/);
  assert.match(out, /Hello\n\n00:00:03\.000/);
  assert.equal(srtToVtt("\uFEFFWEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHi\n").startsWith("WEBVTT"), true);
  assert.throws(() => srtToVtt("garbage"), /No valid subtitle cues/);
  assert.match(validateCaptionFile({ name: "bad.sub", size: 2 }), /\.srt or \.vtt/);
  assert.equal(validateCaptionFile({ name: "captions.vtt", size: 210 }), null);
});

test("status from superseded generation cannot reattach its old playlist", () => {
  assert.equal(isCurrentHLSStatus({ generation: 1, playlistReady: true }, 2), false);
  assert.equal(isCurrentHLSStatus({ generation: 2, playlistReady: false }, 2), true);
  assert.equal(isCurrentHLSStatus({ generation: 3, playlistReady: true }, 2), true);
  assert.equal(isCurrentHLSStatus({ state: "preparing" }, 2), false);
});

test("selected-file cache threshold keeps ready HLS detached until satisfied", () => {
  const ready = {playlistReady:true, playlistUrl:"/api/v1/hls/id/g/1/index.m3u8"};
  assert.equal(canAttachHLS({...ready, startupCacheReady:false}),false);
  assert.equal(canAttachHLS({...ready, startupCacheReady:true}),true);
  assert.equal(canAttachHLS({...ready, startupCacheReady:true, playlistReady:false}),false);
  assert.equal(canAttachHLS({...ready, playlistUrl:null}),false);
});
