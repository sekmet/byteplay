import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

const source = readFileSync(new URL("./player-timeline.js", import.meta.url), "utf8");
const sandbox = {};
runInNewContext(source, sandbox);
const timeline = sandbox.BytePlayTimeline;

const ranges = {
  length: 2,
  start: (i) => i === 0 ? 0 : 12,
  end: (i) => i === 0 ? 8 : 25
};
test("absolute mapping is generation-start plus local time", () => {
  assert.equal(timeline.absoluteSeconds(120, 8.5), 128.5);
  assert.throws(() => timeline.absoluteSeconds(-1, 3), /finite nonnegative/);
  assert.throws(() => timeline.absoluteSeconds(1, Infinity), /finite nonnegative/);
});
test("only locally buffered time uses Video.js seek", async () => {
  let videoSeconds = -1;
  const result = await timeline.seekAbsolute({
    targetSeconds: 125, generationStartSeconds: 120, buffered: ranges,
    seekBuffered: (seconds) => { videoSeconds = seconds; },
    restartGeneration: () => { throw new Error("unexpected HLS restart"); }
  });
  assert.equal(result.type, "buffered");
  assert.equal(videoSeconds, 5);
});
test("distant target causes an absolute server seek, not a fake timebar jump", async () => {
  let payload;
  const response = await timeline.seekAbsolute({
    targetSeconds: 605, generationStartSeconds: 120, buffered: ranges,
    seekBuffered: () => { throw new Error("should restart"); },
    restartGeneration: async (request) => {
      payload = request;
      return { state: "preparing", generation: 7 };
    }
  });
  assert.equal(response.type, "restart");
  assert.equal(response.generation, 7);
  assert.equal(payload.seconds, 605);
});
test("audio switching restarts even inside local buffer", async () => {
  let payload;
  await timeline.seekAbsolute({
    targetSeconds: 125, generationStartSeconds: 120, buffered: ranges,
    audioStream: 3,
    seekBuffered: () => { throw new Error("audio requires new rendition"); },
    restartGeneration: async (request) => {
      payload = request;
      return { state: "preparing", generation: 8 };
    }
  });
  assert.equal(payload.seconds, 125);
  assert.equal(payload.audioStream, 3);
});
test("rejects invalid time and audio without starting playback", async () => {
  await assert.rejects(() => timeline.seekAbsolute({
    targetSeconds: -4, generationStartSeconds: 0, buffered: ranges,
    restartGeneration: async () => { throw new Error("unexpected"); }
  }));
  await assert.rejects(() => timeline.seekAbsolute({
    targetSeconds: 200, generationStartSeconds: 0, buffered: ranges,
    audioStream: -1,
    restartGeneration: async () => { throw new Error("unexpected"); }
  }));
});
