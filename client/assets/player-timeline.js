(function (scope) {
  "use strict";

  const finiteNonnegative = (value) =>
    typeof value === "number" && Number.isFinite(value) && value >= 0;

  function absoluteSeconds(generationStartSeconds, relativeSeconds) {
    if (!finiteNonnegative(generationStartSeconds) || !finiteNonnegative(relativeSeconds)) {
      throw new RangeError("Generation start and local time must be finite nonnegative seconds");
    }
    return generationStartSeconds + relativeSeconds;
  }

  function bufferedLocalSeconds(absoluteTarget, generationStartSeconds, ranges) {
    if (!finiteNonnegative(absoluteTarget) || !finiteNonnegative(generationStartSeconds)) {
      throw new RangeError("Absolute seek target and generation start must be valid seconds");
    }
    if (!ranges || typeof ranges.length !== "number") {
      return null;
    }
    const local = absoluteTarget - generationStartSeconds;
    if (local < 0) return null;
    for (let i = 0; i < ranges.length; i += 1) {
      if (local >= ranges.start(i) && local <= ranges.end(i)) return local;
    }
    return null;
  }

  // Remains outside Video.js; callers supply the TimeRanges object, seek
  // callback and server restart callback. Never mutate private VHS/player APIs.
  async function seekAbsolute({ targetSeconds, generationStartSeconds, buffered,
    seekBuffered, restartGeneration, audioStream }) {
    const local = bufferedLocalSeconds(targetSeconds, generationStartSeconds, buffered);
    if (local !== null && audioStream === undefined) {
      seekBuffered(local);
      return { type: "buffered", absoluteSeconds: targetSeconds };
    }
    if (typeof restartGeneration !== "function") {
      throw new TypeError("restartGeneration callback required");
    }
    const payload = { seconds: targetSeconds };
    if (audioStream !== undefined) {
      if (!Number.isInteger(audioStream) || audioStream < 0) {
        throw new RangeError("Invalid audio stream index");
      }
      payload.audioStream = audioStream;
    }
    const response = await restartGeneration(payload);
    if (!response || response.state !== "preparing" ||
        !Number.isInteger(response.generation) || response.generation < 1) {
      throw new Error("Server did not accept playback generation restart");
    }
    return { type: "restart", generation: response.generation,
      absoluteSeconds: targetSeconds };
  }

  scope.BytePlayTimeline = Object.freeze({
    absoluteSeconds,
    bufferedLocalSeconds,
    seekAbsolute
  });
})(typeof window === "object" ? window : globalThis);
