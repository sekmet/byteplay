// Browser-only, origin-scoped playback history. No server persistence or sync.
// Magnet URLs may contain private tracker tokens: never log or place them in DOM.
export const PLAYED_KEY = "byteplay.played.v1";
export const MAX_PLAYED = 30;
const HASH = /^[a-f0-9]{40}$/i;
const finite = (x) => typeof x === "number" && Number.isFinite(x) && x >= 0;
function clean(entry) {
  if (!entry || typeof entry !== "object") return null;
  const sessionId = String(entry.sessionId || "").toLowerCase();
  const fileIndex = entry.fileIndex;
  const source = String(entry.source || "");
  if (!HASH.test(sessionId) || !Number.isInteger(fileIndex) || fileIndex < 0 ||
      source.length > 8192 || !(HASH.test(source) || /^magnet:\?xt=urn:btih:/i.test(source))) return null;
  const title = String(entry.title || "").slice(0, 240).trim();
  if (!title) return null;
  const duration = finite(entry.duration) && entry.duration > 0 ? entry.duration : null;
  const rawPosition = finite(entry.position) ? entry.position : 0;
  const position = duration == null ? Math.min(rawPosition, 86400) : Math.min(rawPosition, duration);
  const playedAt = Number.isFinite(entry.playedAt) && entry.playedAt > 0 ?
    Math.min(entry.playedAt, Date.now() + 86400000) : Date.now();
  return { sessionId, fileIndex, source, title, position, duration,
    playedAt, size: finite(entry.size) ? entry.size : null };
}
export function readPlayed(storage) {
  try {
    const raw = JSON.parse(storage.getItem(PLAYED_KEY) || "[]");
    if (!Array.isArray(raw)) return [];
    const seen = new Set();
    return raw.slice(0, 150).map(clean).filter(item => {
      if (!item) return false;
      const key = item.sessionId + ":" + item.fileIndex;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    }).sort((a, b) => b.playedAt - a.playedAt).slice(0, MAX_PLAYED);
  } catch { return []; } // disabled/quota/corrupt storage: no app crash
}
export function savePlayed(storage, entry) {
  const next = clean(entry);
  if (!next) return false;
  try {
    const old = readPlayed(storage);
    const result = [next, ...old.filter(item =>
      item.sessionId !== next.sessionId || item.fileIndex !== next.fileIndex)]
      .sort((a, b) => b.playedAt - a.playedAt).slice(0, MAX_PLAYED);
    storage.setItem(PLAYED_KEY, JSON.stringify(result));
    return true;
  } catch { return false; }
}
export function removePlayed(storage, sessionId, fileIndex) {
  try {
    const remaining = readPlayed(storage).filter(item =>
      !(item.sessionId === sessionId && item.fileIndex === fileIndex));
    storage.setItem(PLAYED_KEY, JSON.stringify(remaining));
    return true;
  } catch { return false; }
}
export function clearPlayed(storage) {
  try { storage.removeItem(PLAYED_KEY); return true; } catch { return false; }
}
export function replaySeconds(entry) {
  const item = clean(entry);
  if (!item || item.position < 10) return 0;
  if (item.duration != null && item.duration - item.position < 20) return 0;
  return Math.max(0, Math.floor(item.position) - 3);
}
