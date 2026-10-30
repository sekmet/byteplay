export const VIDEO_EXT = /\.(mp4|mkv|webm|avi|mov|m4v|wmv)$/i;
export const CAPTION_EXT = /\.(srt|vtt)$/i;
export const MAX_TORRENT_BYTES = 10 * 1024 * 1024;
export const MAX_CAPTION_BYTES = 5 * 1024 * 1024;

export function validateTorrentFile(file) {
  if (!file || !/\.torrent$/i.test(file.name)) return "Choose a .torrent metadata file.";
  if (file.size > MAX_TORRENT_BYTES) return "Torrent metadata files must be 10 MB or smaller.";
  if (file.size === 0) return "This torrent file is empty.";
  return null;
}
export function readableError(body, fallback = "The server could not complete that request.") {
  if (typeof body?.error === "string") return body.error;
  if (typeof body?.error?.message === "string") return body.error.message;
  return fallback;
}
export function normalizeFiles(data) {
  return Array.isArray(data)
    ? data.filter(file => Number.isInteger(file?.index) && typeof file?.name === "string" && Number.isFinite(file?.size))
    : [];
}
export function videoFiles(data) { return normalizeFiles(data).filter(file => VIDEO_EXT.test(file.name)); }
export function captionFiles(data) { return normalizeFiles(data).filter(file => CAPTION_EXT.test(file.name)); }
export function formatBytes(value) {
  if (!Number.isFinite(value) || value < 0) return "—";
  if (value < 1024) return String(Math.floor(value)) + " B";
  const units = ["KB", "MB", "GB", "TB"];
  let result = value, index = -1;
  do { result /= 1024; index += 1; } while (result >= 1024 && index < units.length - 1);
  return (result >= 100 ? result.toFixed(0) : result.toFixed(1)) + " " + units[index];
}
export function formatTime(seconds) {
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  const whole = Math.floor(seconds);
  const hours = Math.floor(whole / 3600);
  const minutes = String(Math.floor((whole % 3600) / 60)).padStart(2, "0");
  const secs = String(whole % 60).padStart(2, "0");
  return hours ? hours + ":" + minutes + ":" + secs : String(Math.floor(whole / 60)).padStart(2, "0") + ":" + secs;
}
export function canAttachHLS(status) {
  return Boolean(status?.playlistReady && status?.startupCacheReady !== false &&
    (status?.playlistURL || status?.playlistUrl));
}
export function stateText(server, browser = "waiting") {
  if (server?.error?.message) return { title: "Stream unavailable", detail: server.error.message, error: true };
  switch (server?.state) {
    case "queued": return { title: "Connecting to peers", detail: "Playback is queued while BytePlay prepares the source." };
    case "probing": return { title: "Reading metadata", detail: "Checking codecs and audio tracks for a compatible stream." };
    case "preparing": return { title: server.mode === "video-transcode" ? "Transcoding video" : server.mode === "audio-transcode" ? "Converting audio" : "Preparing stream", detail: "Generating finalized HLS segments from the selected file." };
    case "paused_idle": return { title: "Paused", detail: "The encoder stopped. Resume from the video position to continue." };
    case "failed": return { title: "Stream unavailable", detail: "The encoder did not complete playback.", error: true };
    case "cancelled": return { title: "Playback cancelled", detail: "Start the stream again to retry.", error: true };
    case "ready":
    case "complete":
      if (browser === "playing") return { title: "Playing", detail: "Your browser is playing the prepared HLS stream." };
      if (browser === "paused") return { title: "Paused", detail: "Playback is paused in your browser." };
      if (browser === "buffering") return { title: "Buffering", detail: "Waiting for usable video data." };
      return { title: "Ready to play", detail: "Play the prepared stream in your browser." };
    default: return { title: "Preparing stream", detail: "Waiting for media status." };
  }
}
export function srtToVtt(input) {
  const normalized = String(input).replace(/^\uFEFF/, "").replace(/\r\n?/g, "\n").trim();
  if (/^WEBVTT(?:\s|$)/.test(normalized)) return normalized + "\n";
  const blocks = normalized.split(/\n\s*\n/), cues = [];
  for (const block of blocks) {
    const lines = block.split("\n");
    if (/^\d+$/.test(lines[0]?.trim())) lines.shift();
    const first = lines[0] || "";
    if (!/^\d{2}:\d{2}:\d{2}[,.]\d{3}\s+-->\s+\d{2}:\d{2}:\d{2}[,.]\d{3}/.test(first)) continue;
    cues.push([first.replace(/,/g, "."), ...lines.slice(1)].join("\n"));
  }
  if (!cues.length) throw new Error("No valid subtitle cues were found in the file.");
  return "WEBVTT\n\n" + cues.join("\n\n") + "\n";
}
export function validateCaptionFile(file) {
  if (!file || !CAPTION_EXT.test(file.name)) return "Choose a .srt or .vtt subtitle file.";
  if (file.size > MAX_CAPTION_BYTES) return "Subtitle files must be 5 MB or smaller.";
  return file.size > 0 ? null : "This subtitle file is empty.";
}

export function isCurrentHLSStatus(status, expectedGeneration) {
  return Number.isInteger(status?.generation) && status.generation >= expectedGeneration;
}
