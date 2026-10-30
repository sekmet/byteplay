import {
  MAX_TORRENT_BYTES, MAX_CAPTION_BYTES, validateTorrentFile, validateCaptionFile,
  readableError, videoFiles, captionFiles, formatBytes, formatTime, stateText, srtToVtt, isCurrentHLSStatus, canAttachHLS
} from "./byteplay-core.mjs";
import { readPlayed, savePlayed, removePlayed, clearPlayed, replaySeconds } from "./played-history.mjs";

const el = (id) => document.getElementById(id);
const ui = { session: null, files: [], videos: [], captions: [], selected: null, playback: null,
  status: null, player: null, poll: null, token: 0, wantPlay: true,
  browser: "waiting", attachedGeneration: 0, expectedGeneration: 0, trackURLs: [], lastKnownTime: 0,
  suppressEvents: false, probe: null, probeLoading: false, probeNextAttempt: 0, provider: null, remoteTracks: [],
  source: null, hasPlayed: false, historySavedAt: 0, pendingResume: 0,
  initialPlaybackGeneration: 0 };


function rememberPlayback(force = false) {
  if (!ui.hasPlayed || !ui.session || !ui.selected || !ui.source) return;
  const now = Date.now();
  if (!force && now - ui.historySavedAt < 5000) return;
  let storage;
  try { storage = window.localStorage; } catch { return; }
  const duration = ui.status?.durationSeconds ?? ui.probe?.duration;
  if (savePlayed(storage, {
    sessionId: ui.session, fileIndex: ui.selected.index, source: ui.source,
    title: ui.selected.name, size: ui.selected.size,
    position: Number.isFinite(ui.lastKnownTime) ? ui.lastKnownTime : 0,
    duration: Number.isFinite(duration) ? duration : null, playedAt: now
  })) {
    ui.historySavedAt = now;
  }
}
function renderPlayed() {
  let list;
  try { list = readPlayed(window.localStorage); } catch { list = []; }
  const root = el("bp-played-list");
  root.replaceChildren();
  el("bp-played-empty").hidden = list.length > 0;
  el("bp-played-clear").hidden = list.length === 0;
  for (const item of list) {
    const card = document.createElement("article");
    card.className = "bp-played-item";
    const title = document.createElement("h3");
    title.textContent = item.title;
    const meta = document.createElement("div");
    meta.className = "bp-played-meta";
    const progress = item.duration ? Math.min(100, item.position / item.duration * 100) : null;
    const labels = [item.duration ? formatTime(item.position) + " / " + formatTime(item.duration) :
      formatTime(item.position) + " played", formatBytes(item.size),
      new Date(item.playedAt).toLocaleDateString()];
    for (const text of labels) {
      const span = document.createElement("span");
      span.textContent = text;
      meta.append(span);
    }
    card.append(title, meta);
    if (progress != null) {
      const bar = document.createElement("div");
      bar.className = "bp-played-bar";
      bar.setAttribute("role", "progressbar");
      bar.setAttribute("aria-label", "Watched percent");
      bar.setAttribute("aria-valuemin", "0");
      bar.setAttribute("aria-valuemax", "100");
      bar.setAttribute("aria-valuenow", String(Math.round(progress)));
      const fill = document.createElement("span");
      fill.style.width = progress.toFixed(1) + "%";
      bar.append(fill);
      card.append(bar);
    }
    const actions = document.createElement("div");
    actions.className = "bp-played-actions";
    const replay = (text, seconds, cssClass) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = cssClass;
      button.textContent = text;
      button.setAttribute("aria-label", text + ": " + item.title);
      button.addEventListener("click", () => {
        clearError("bp-played-error");
        el("bp-magnet").value = item.source;
        openMagnet(item.source, { fileIndex: item.fileIndex, resumeSeconds: seconds });
      });
      actions.append(button);
    };
    const seconds = replaySeconds(item);
    if (seconds > 0) replay("Resume", seconds, "bp-played-resume");
    replay("Replay", 0, "bp-played-replay");
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "bp-played-remove";
    remove.textContent = "Remove";
    remove.setAttribute("aria-label", "Remove from played: " + item.title);
    remove.addEventListener("click", () => {
      if (removePlayed(window.localStorage, item.sessionId, item.fileIndex)) renderPlayed();
      else showError("bp-played-error", "Browser storage is unavailable. History could not be updated.");
    });
    actions.append(remove);
    card.append(actions);
    root.append(card);
  }
}

async function request(path, opts = {}) {
  let response;
  try {
    response = await fetch(path, { credentials: "same-origin", ...opts });
  } catch (error) {
    throw new Error("BytePlay server is unreachable. Check the connection and retry.");
  }
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(readableError(body, "Server returned HTTP " + response.status));
    error.status = response.status;
    error.code = body?.error?.code;
    throw error;
  }
  return body;
}
function putText(id, value) { el(id).textContent = value; }
function showError(id, message) {
  const target = el(id);
  target.textContent = message;
  target.hidden = false;
}
function clearError(id) { el(id).hidden = true; el(id).textContent = ""; }
function showWatch() {
  el("bp-home").hidden = true;
  el("bp-watch").hidden = false;
  document.title = "Watch · BytePlay";
  window.scrollTo({ top: 0, behavior: "instant" });
}
function showHome() {
  el("bp-home").hidden = false;
  el("bp-watch").hidden = true;
  document.title = "BytePlay — Your video, ready to stream";
  renderPlayed();
}
function setOverlay(title, detail, visible = true) {
  el("bp-player-overlay").hidden = !visible;
  putText("bp-overlay-title", title);
  putText("bp-overlay-detail", detail);
}
function selectedVideoURL(file) {
  return "/api/v1/torrent/" + encodeURIComponent(ui.session) + "/stream/" + file.index;
}
function trackURL(file) {
  const root = selectedVideoURL(file);
  return /\.srt$/i.test(file.name) ? root + ".vtt?format=vtt" : root;
}
function setStage(status) {
  const state = stateText(status, ui.browser);
  putText("bp-stage", state.title);
  putText("bp-player-state", state.title);
  putText("bp-player-detail", state.detail);
  if (status?.peers != null) putText("bp-peers", String(status.peers));
  else putText("bp-peers", "—");
  putText("bp-speed", status?.downloadBytesPerSecond == null
    ? "—" : formatBytes(status.downloadBytesPerSecond) + "/s");
  const total = status?.selectedFileBytesTotal, downloaded = status?.selectedFileBytesCompleted;
  putText("bp-progress", total > 0 && downloaded != null
    ? Math.min(100, 100 * downloaded / total).toFixed(1) + "%" : "—");
  if (status?.startupCacheReady === false && !state.error) {
    const target = status.startupCachePercent ?? 9;
    const cached = total > 0 && downloaded != null ? Math.min(100, 100 * downloaded / total) : 0;
    const label = "Caching selected video " + cached.toFixed(1) + "% / " + target + "%";
    putText("bp-stage", label);
    putText("bp-player-state", "Buffering before playback");
    putText("bp-player-detail", "Waiting for " + target + "% of the selected video to be cached.");
    setOverlay("Buffering before playback", label, true);
  }
  if (state.error) {
    showError("bp-watch-error", state.detail);
    el("bp-retry").hidden = false;
    setOverlay(state.title, state.detail, true);
  }
  if (!status?.playlistReady && !state.error) setOverlay(state.title, state.detail, true);
}
function renderInfo() {
  if (!ui.selected) return;
  const status = ui.status || {};
  const video = (ui.probe?.streams || []).find(stream => stream.type === "video");
  const audio = (ui.probe?.streams || []).filter(stream => stream.type === "audio");
  const values = [
    ["Filename", ui.selected.name],
    ["File size", formatBytes(ui.selected.size)],
    ["Duration", formatTime(status.durationSeconds ?? ui.probe?.duration)],
    ["Video codec", video?.codec || "Not yet available"],
    ["Audio codecs", audio.length ? audio.map(track => track.codec).join(", ") : "Not yet available"],
    ["HLS route", { copy: "Copy", "audio-transcode": "Audio conversion", "video-transcode": "Video conversion" }[status.mode] || "Not yet determined"],
    ["Peers", status.peers == null ? "Unknown" : String(status.peers)],
    ["Download progress", el("bp-progress").textContent],
    ["Error", status.error?.message || "None reported"]
  ];
  const list = el("bp-info-list");
  list.replaceChildren();
  for (const [key, value] of values) {
    const term = document.createElement("dt");
    term.textContent = key;
    const description = document.createElement("dd");
    description.textContent = String(value);
    const block = document.createElement("div");
    block.append(term, description);
    list.append(block);
  }
}
function renderAudio() {
  const tracks = (ui.probe?.streams || []).filter(s => s.type === "audio" && Number.isInteger(s.index));
  const wrap = el("bp-audio-wrap"), select = el("bp-audio");
  wrap.hidden = tracks.length <= 1;
  select.replaceChildren();
  for (const track of tracks) {
    const option = document.createElement("option");
    option.value = String(track.index);
    option.textContent = (track.language || "Audio") + " · " + track.codec + " (#" + track.index + ")";
    select.append(option);
  }
  if (tracks.length) {
    const selected = tracks.find(t => t.default) || tracks[0];
    select.value = String(selected.index);
  }
}
function prepareVideo() {
  if (ui.player) return;
  if (typeof window.videojs !== "function") {
    throw new Error("Video.js could not load. Check the bundled player assets.");
  }
  ui.player = window.videojs("bp-video", {
    controls: true, fluid: true, aspectRatio: "16:9", autoplay: false,
    preload: "auto", playbackRates: [0.75, 1, 1.25, 1.5, 2],
    html5: { nativeTextTracks: false },
    controlBar: { progressControl: false }
  });
  const player = ui.player;
  player.on("play", () => {
    // Play is an intent; only the playing event confirms actual playback.
    ui.browser = "buffering";
    ui.wantPlay = true;
    if (!ui.suppressEvents) sendControl("resume");
    setStage(ui.status);
  });
  player.on("pause", () => {
    ui.browser = "paused";
    if (!ui.suppressEvents) {
      rememberPlayback(true);
      ui.wantPlay = false;
      sendControl("pause");
    }
    setStage(ui.status);
  });
  player.on("waiting", () => { ui.browser = "buffering"; setStage(ui.status); });
  player.on("playing", () => {
    // Video.js treats a growing EVENT manifest as live. On a newly
    // attached source it may start near the generated tail instead of
    // its beginning. Rewind only that first attach, never normal seeks.
    if (ui.initialPlaybackGeneration === ui.attachedGeneration) {
      ui.initialPlaybackGeneration = 0;
      if (player.currentTime() > 6) {
        player.currentTime(0);
        ui.browser = "buffering";
        setStage(ui.status);
        return;
      }
    }
    ui.browser = "playing";
    ui.hasPlayed = true;
    rememberPlayback(true);
    setStage(ui.status);
  });
  player.on("ended", () => { rememberPlayback(true); });
  player.on("timeupdate", () => {
    if (!ui.status) return;
    const absolute = window.BytePlayTimeline.absoluteSeconds(ui.status.startSeconds || 0, player.currentTime() || 0);
    ui.lastKnownTime = absolute;
    if (Number.isFinite(ui.status.durationSeconds) && ui.status.durationSeconds > 0) {
      const field = el("bp-seek");
      if (document.activeElement !== field) field.value = String(Math.min(Number(field.max), absolute));
      putText("bp-time", formatTime(absolute) + " / " + formatTime(ui.status.durationSeconds));
    }
    // Keep last-watched time even if source metadata has no duration.
    rememberPlayback();
  });
  player.on("error", () => {
    ui.browser = "error";
    const detail = player.error()?.message || "The browser cannot decode or load this HLS stream.";
    showError("bp-watch-error", detail);
    el("bp-retry").hidden = false;
    setOverlay("Playback error", detail, true);
  });
}
function updateSeekRange(status) {
  const duration = status?.durationSeconds;
  if (!Number.isFinite(duration) || duration <= 0) return;
  el("bp-timeline").hidden = false;
  el("bp-seek").max = String(Math.floor(duration));
  putText("bp-time", formatTime(ui.lastKnownTime) + " / " + formatTime(duration));
}
async function attachReady(status) {
  if (!status.playlistURL && !status.playlistUrl) return;
  const src = status.playlistURL || status.playlistUrl;
  if (ui.attachedGeneration === status.generation) return;
  prepareVideo();
  ui.attachedGeneration = status.generation;
  ui.initialPlaybackGeneration = status.generation;
  ui.status = status;
  ui.lastKnownTime = status.startSeconds || 0;
  ui.suppressEvents = true;
  ui.player.pause();
  const attachedGeneration = status.generation;
  ui.player.one("loadedmetadata", () => {
    if (ui.attachedGeneration === attachedGeneration) {
      ui.player.currentTime(0);
    }
  });
  ui.player.src({ src, type: "application/x-mpegURL" });
  ui.suppressEvents = false;
  setOverlay("Stream ready", "Press play to start watching.", false);
  updateSeekRange(status);
  installTorrentTracks();
  if (ui.wantPlay) {
    ui.player.play()?.catch(() => {
      ui.browser = "paused";
      setStage(status);
      putText("bp-player-detail", "Your browser requires a click to begin playback.");
    });
  } else {
    sendControl("pause");
  }
}
async function refreshProbe(token) {
  if (!ui.session || !ui.selected || ui.probe || ui.probeLoading ||
      Date.now() < ui.probeNextAttempt) return;
  ui.probeLoading = true;
  ui.probeNextAttempt = Date.now() + 3000;
  try {
    const data = await request("/api/v1/torrent/" + encodeURIComponent(ui.session) + "/probe/" + ui.selected.index);
    if (token !== ui.token) return;
    if (!data || !Array.isArray(data.streams)) throw new Error("Media probe incomplete");
    ui.probe = data;
    renderAudio();
    renderInfo();
  } catch {
    // A temporary metadata/probe failure is retried after HLS is ready.
  } finally {
    ui.probeLoading = false;
  }
}
async function pollStatus(token) {
  if (token !== ui.token || !ui.playback) return;
  try {
    const status = await request("/api/v1/hls/" + encodeURIComponent(ui.playback) + "/status");
    if (token !== ui.token) return;
    if (!isCurrentHLSStatus(status, ui.expectedGeneration)) {
      ui.poll = setTimeout(() => pollStatus(token), 300);
      return;
    }
    ui.expectedGeneration = status.generation;
    ui.status = status;
    setStage(status);
    updateSeekRange(status);
    renderInfo();
    if (canAttachHLS(status) && ui.pendingResume > 0) {
      // Resume by an explicit generation restart before exposing the old
      // playlist. This preserves absolute positions without modifying VHS.
      const resumeAt = Math.min(ui.pendingResume,
        Number.isFinite(status.durationSeconds) ? Math.max(0, status.durationSeconds - 1) : ui.pendingResume);
      ui.pendingResume = 0;
      await seekTo(resumeAt);
      if (ui.expectedGeneration !== status.generation) {
        ui.poll = setTimeout(() => pollStatus(token), 300);
        return;
      }
    }
    if (canAttachHLS(status)) {
      await attachReady(status);
      // The server caches the HLS worker's successful probe. Retry only
      // until metadata arrives; do not block playback on this optional UI.
      if (!ui.probe) void refreshProbe(token);
    }
  } catch (error) {
    if (token !== ui.token) return;
    showError("bp-watch-error", error.message);
    setOverlay("Connection interrupted", error.message);
    el("bp-retry").hidden = false;
  }
  if (token === ui.token && ui.playback) {
    ui.poll = setTimeout(() => pollStatus(token), 1500);
  }
}
function stopPolling() {
  ui.token++;
  clearTimeout(ui.poll);
  ui.poll = null;
}
async function releasePlayback() {
  rememberPlayback(true);
  stopPolling();
  const id = ui.playback;
  ui.playback = null;
  ui.status = null;
  ui.attachedGeneration = 0;
  ui.initialPlaybackGeneration = 0;
  ui.expectedGeneration = 0;
  if (ui.player) {
    ui.suppressEvents = true;
    ui.player.dispose();
    ui.player = null;
    ui.suppressEvents = false;
    const surface = document.querySelector(".bp-player-surface");
    const video = document.createElement("video");
    video.id = "bp-video";
    video.className = "video-js vjs-big-play-centered";
    video.setAttribute("controls", "");
    video.setAttribute("playsinline", "");
    surface.replaceChildren(video);
  }
  for (const url of ui.trackURLs) URL.revokeObjectURL(url);
  ui.trackURLs = [];
  ui.remoteTracks = [];
  ui.hasPlayed = false;
  if (id) {
    await fetch("/api/v1/hls/" + encodeURIComponent(id), { method: "DELETE", credentials: "same-origin" }).catch(() => {});
  }
}
async function startFile(file, resumeSeconds = 0) {
  await releasePlayback();
  ui.selected = file;
  ui.probe = null;
  ui.probeLoading = false;
  ui.probeNextAttempt = 0;
  ui.lastKnownTime = Number.isFinite(resumeSeconds) && resumeSeconds > 0 ? resumeSeconds : 0;
  ui.pendingResume = ui.lastKnownTime;
  ui.historySavedAt = 0;
  ui.browser = "waiting";
  ui.wantPlay = true;
  clearError("bp-watch-error");
  el("bp-retry").hidden = true;
  el("bp-timeline").hidden = true;
  putText("bp-title", file.name);
  putText("bp-subtitle", formatBytes(file.size) + " · Torrent source");
  el("bp-download").href = selectedVideoURL(file);
  el("bp-download").setAttribute("download", file.name.split("/").pop() || "video");
  setOverlay("Preparing stream", "Checking the video and collecting needed torrent pieces.");
  const token = ui.token;
  try {
    const ticket = await request("/api/v1/hls/sessions", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sessionId: ui.session, fileIndex: file.index })
    });
    if (token !== ui.token) {
      fetch("/api/v1/hls/" + ticket.playbackId, { method: "DELETE" }).catch(() => {});
      return;
    }
    ui.playback = ticket.playbackId;
    ui.expectedGeneration = ticket.generation || 1;
    ui.status = ticket;
    setStage(ticket);
    pollStatus(token);
  } catch (error) {
    if (token !== ui.token) return;
    showError("bp-watch-error", error.message);
    setOverlay("Unable to prepare stream", error.message);
    el("bp-retry").hidden = false;
  }
}
function renderFileSelector() {
  const select = el("bp-video-select");
  select.replaceChildren();
  for (const file of ui.videos) {
    const option = document.createElement("option");
    option.value = String(file.index);
    option.textContent = file.name;
    select.append(option);
  }
  select.value = String(ui.selected.index);
  document.querySelector(".bp-file-choice").hidden = ui.videos.length < 2;
}
async function openMagnet(value, replay = null) {
  const magnet = String(value || "").trim();
  if (!magnet) return showError("bp-home-error", "Enter a magnet link or a 40-character infohash.");
  clearError("bp-home-error");
  const button = el("bp-open");
  button.disabled = true;
  button.textContent = "Reading torrent metadata…";
  try {
    await releasePlayback();
    const ticket = await request("/api/v1/torrent/add", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ magnet })
    });
    ui.session = ticket.sessionId;
    ui.source = magnet;
    const files = await request("/api/v1/torrent/" + encodeURIComponent(ui.session));
    ui.files = Array.isArray(files) ? files : [];
    ui.videos = videoFiles(ui.files);
    ui.captions = captionFiles(ui.files);
    if (!ui.videos.length) throw new Error("No supported video file was found in this torrent.");
    ui.selected = replay == null ? ui.videos[0] :
      (ui.videos.find(file => file.index === replay.fileIndex) || ui.videos[0]);
    renderFileSelector();
    showWatch();
    await startFile(ui.selected, replay?.fileIndex === ui.selected.index ? replay?.resumeSeconds || 0 : 0);
  } catch (error) {
    showError("bp-home-error", error.message);
  } finally {
    button.disabled = false;
    button.textContent = "Open video ↗";
  }
}
async function sendControl(action) {
  if (!ui.playback) return;
  try {
    await request("/api/v1/hls/" + ui.playback + "/control", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action })
    });
  } catch (error) {
    if (error.code === "seek_required" && action === "resume") {
      await seekTo(ui.lastKnownTime);
    } else {
      showError("bp-watch-error", error.message);
    }
  }
}
async function seekTo(absolute, audioStream) {
  if (!ui.playback || !ui.status || !Number.isFinite(absolute)) return;
  const token = ui.token;
  // Preserve explicit browser play/pause intent across buffering and seek restarts.
  // A buffering player may be temporarily paused even when play was requested.
  setOverlay("Seeking video", "Starting the HLS stream at " + formatTime(absolute));
  clearError("bp-watch-error");
  try {
    const seekResult = await window.BytePlayTimeline.seekAbsolute({
      targetSeconds: absolute,
      generationStartSeconds: ui.status.startSeconds || 0,
      buffered: ui.player?.buffered(),
      audioStream,
      seekBuffered: (relative) => ui.player.currentTime(relative),
      restartGeneration: async (payload) => {
        const result = await request("/api/v1/hls/" + ui.playback + "/seek", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload)
        });
        return result;
      }
    });
    if (token !== ui.token) return;
    ui.lastKnownTime = absolute;
    if (seekResult.type === "buffered") {
      setOverlay("", "", false);
      return;
    }
    ui.expectedGeneration = seekResult.generation;
    ui.status = {
      ...ui.status, generation: seekResult.generation, state: "preparing",
      playlistReady: false, playlistUrl: null, playlistURL: null, startSeconds: absolute
    };
    // Never reattach an obsolete playlist while the generation is restarting.
    ui.attachedGeneration = 0;
    ui.suppressEvents = true;
    ui.player?.pause();
    ui.suppressEvents = false;
    putText("bp-player-state", "Preparing new generation");
  } catch (error) {
    if (token !== ui.token) return;
    showError("bp-watch-error", error.message);
    if (ui.status?.playlistReady) setOverlay("", "", false);
    el("bp-retry").hidden = false;
  }
}
function installTorrentTracks() {
  if (!ui.player) return;
  for (const old of ui.remoteTracks) {
    try { ui.player.removeRemoteTextTrack(old); } catch { /* stale track after source change */ }
  }
  ui.remoteTracks = [];
  for (const file of ui.captions) {
    const track = { kind: "subtitles", src: trackURL(file), srclang: "en",
      label: file.name.split("/").pop() };
    const added = ui.player.addRemoteTextTrack(track, false);
    if (added) ui.remoteTracks.push(added.track || added);
  }
}
async function loadCaptions(file) {
  const error = validateCaptionFile(file);
  if (error) return showError("bp-watch-error", error);
  if (!ui.player) return showError("bp-watch-error", "Start the video player before adding subtitles.");
  try {
    const raw = await file.text();
    const text = /\.srt$/i.test(file.name) ? srtToVtt(raw)
      : (/^\uFEFF?WEBVTT/.test(raw) ? raw.replace(/^\uFEFF/, "") : srtToVtt(raw));
    const url = URL.createObjectURL(new Blob([text], { type: "text/vtt" }));
    ui.trackURLs.push(url);
    const added = ui.player.addRemoteTextTrack({
      kind: "subtitles", src: url, srclang: "en", label: file.name
    }, false);
    const tracks = ui.player.textTracks();
    for (let i = 0; i < tracks.length; i++) {
      tracks[i].mode = tracks[i] === added.track ? "showing" : "disabled";
    }
    clearError("bp-watch-error");
    putText("bp-player-detail", "Subtitle file added: " + file.name);
  } catch (error) {
    showError("bp-watch-error", error.message);
  }
}
async function uploadTorrent(file) {
  const failure = validateTorrentFile(file);
  if (failure) return showError("bp-home-error", failure);
  clearError("bp-home-error");
  const form = new FormData();
  form.append("torrent", file);
  try {
    const converted = await request("/api/v1/torrent/convert", { method: "POST", body: form });
    el("bp-magnet").value = converted.magnet;
    await openMagnet(converted.magnet);
  } catch (error) {
    showError("bp-home-error", error.message);
  }
}
async function loadProvider() {
  try {
    const settings = await request("/api/v1/settings");
    ui.provider = settings.enableProwlarr ? "prowlarr" : settings.enableJackett ? "jackett" : null;
    el("bp-search-area").hidden = !ui.provider;
    if (Number.isInteger(settings.startupCachePercent)) {
      el("bp-startup-cache-percent").value = String(settings.startupCachePercent);
    }
  } catch {
    el("bp-search-area").hidden = true;
  }
}
async function searchProvider(query) {
  if (!ui.provider || !query.trim()) return;
  const results = el("bp-results");
  const message = el("bp-search-message");
  results.replaceChildren();
  message.textContent = "Searching your connected indexer…";
  try {
    const data = await request("/api/v1/" + ui.provider + "/search?q=" + encodeURIComponent(query), {
      method: "POST", headers: { "Content-Type": "application/json" }
    });
    message.textContent = Array.isArray(data) ? String(data.length) + " results" : "No results";
    for (const item of (Array.isArray(data) ? data : []).slice(0, 30)) {
      const row = document.createElement("article");
      row.className = "bp-result";
      const left = document.createElement("div");
      const title = document.createElement("strong");
      title.textContent = item.title || "Untitled torrent";
      const details = document.createElement("small");
      details.textContent = [item.indexer, item.size, item.seeders == null ? "" : item.seeders + " seeders"].filter(Boolean).join(" · ");
      left.append(title, details);
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = "Open";
      const magnet = item.downloadUrl || item.magnetUrl;
      button.disabled = !magnet;
      button.addEventListener("click", () => openMagnet(magnet));
      row.append(left, button);
      results.append(row);
    }
  } catch (error) {
    message.textContent = error.message;
  }
}
el("bp-open-form").addEventListener("submit", (event) => {
  event.preventDefault();
  openMagnet(el("bp-magnet").value);
});
el("bp-paste").addEventListener("click", async () => {
  try {
    el("bp-magnet").value = await navigator.clipboard.readText();
    el("bp-magnet").focus();
    clearError("bp-home-error");
  } catch { showError("bp-home-error", "Clipboard access is unavailable. Paste into the field instead."); }
});
el("bp-torrent-file").addEventListener("change", (event) => {
  if (event.target.files?.[0]) uploadTorrent(event.target.files[0]);
  event.target.value = "";
});
const drop = el("bp-drop");
for (const type of ["dragover", "dragenter"]) {
  drop.addEventListener(type, (event) => { event.preventDefault(); drop.classList.add("dragover"); });
}
for (const type of ["dragleave", "drop"]) {
  drop.addEventListener(type, (event) => { event.preventDefault(); drop.classList.remove("dragover"); });
}
drop.addEventListener("drop", (event) => {
  if (event.dataTransfer?.files?.[0]) uploadTorrent(event.dataTransfer.files[0]);
});
el("bp-settings").addEventListener("click", () => {
  el("settings-btn").click();
  void loadProvider();
});
el("bp-startup-cache-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const input = el("bp-startup-cache-percent");
  const feedback = el("bp-startup-cache-feedback");
  const value = input.value.trim();
  const percent = Number(value);
  if (!value || !Number.isInteger(percent) || percent < 0 || percent > 100) {
    feedback.textContent = "Enter a whole number from 0 to 100.";
    return;
  }
  const save = el("bp-startup-cache-save");
  save.disabled = true;
  try {
    const saved = await request("/api/v1/settings/playback", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ startupCachePercent: percent })
    });
    input.value = String(saved.startupCachePercent);
    feedback.textContent = "Saved. New video sessions wait for " + saved.startupCachePercent + "% caching.";
  } catch (error) {
    feedback.textContent = error.message;
  } finally {
    save.disabled = false;
  }
});
el("bp-played-clear").addEventListener("click", () => {
  if (!window.confirm("Clear played videos from this browser?")) return;
  if (clearPlayed(window.localStorage)) { clearError("bp-played-error"); renderPlayed(); }
  else showError("bp-played-error", "Browser storage is unavailable. History could not be cleared.");
});
window.addEventListener("pagehide", () => rememberPlayback(true));
el("bp-new").addEventListener("click", async () => {
  await releasePlayback();
  ui.session = null;
  ui.files = [];
  showHome();
});
el("bp-video-select").addEventListener("change", () => {
  const file = ui.videos.find(f => f.index === Number(el("bp-video-select").value));
  if (file) startFile(file);
});
el("bp-info-toggle").addEventListener("click", () => {
  const panel = el("bp-info");
  panel.hidden = !panel.hidden;
  if (!panel.hidden) renderInfo();
});
el("bp-close-info").addEventListener("click", () => { el("bp-info").hidden = true; });
el("bp-retry").addEventListener("click", () => { if (ui.selected) startFile(ui.selected); });
el("bp-seek").addEventListener("change", () => seekTo(Number(el("bp-seek").value)));
el("bp-audio").addEventListener("change", () => seekTo(ui.lastKnownTime, Number(el("bp-audio").value)));
el("bp-captions").addEventListener("change", async (event) => {
  if (event.target.files?.[0]) await loadCaptions(event.target.files[0]);
  event.target.value = "";
});
el("bp-search-form").addEventListener("submit", (event) => {
  event.preventDefault();
  searchProvider(el("bp-query").value);
});
const magnetParameter = new URLSearchParams(location.search).get("torrent");
if (magnetParameter) el("bp-magnet").value = magnetParameter;
renderPlayed();
loadProvider();
