import test from "node:test";
import assert from "node:assert/strict";
import { PLAYED_KEY, MAX_PLAYED, readPlayed, savePlayed, removePlayed, clearPlayed, replaySeconds } from "./played-history.mjs";

function mockStorage() {
  const values = new Map();
  return {
    getItem: (key) => values.get(key) || null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key)
  };
}
const base = (id = "a".repeat(40), index = 0) => ({
  sessionId: id, fileIndex: index, source: "magnet:?xt=urn:btih:" + id,
  title: "Legal sample.mp4", position: 33, duration: 120,
  playedAt: 1720000000000, size: 100000
});
test("history round-trips across reads and deduplicates by torrent and file index", () => {
  const store = mockStorage();
  assert.equal(savePlayed(store, base()), true);
  assert.equal(savePlayed(store, { ...base(), position: 45, playedAt: 1720000000100 }), true);
  assert.equal(readPlayed(store).length, 1);
  assert.equal(readPlayed(store)[0].position, 45);
  assert.equal(savePlayed(store, { ...base("b".repeat(40)), playedAt: 1720000000200 }), true);
  assert.deepEqual(readPlayed(store).map(x => x.sessionId), ["b".repeat(40), "a".repeat(40)]);
});
test("bounded history preserves most recent thirty and supports removing just one file", () => {
  const store = mockStorage();
  for (let i = 0; i < MAX_PLAYED + 5; i++) {
    savePlayed(store, { ...base(), fileIndex: i, playedAt: 1720000000000 + i });
  }
  assert.equal(readPlayed(store).length, MAX_PLAYED);
  assert.equal(readPlayed(store)[0].fileIndex, 34);
  assert.equal(removePlayed(store, "a".repeat(40), 34), true);
  assert.equal(readPlayed(store)[0].fileIndex, 33);
  assert.equal(clearPlayed(store), true);
  assert.deepEqual(readPlayed(store), []);
  assert.equal(store.getItem(PLAYED_KEY), null);
});
test("discard malformed/corrupt/suspicious data and keep storage failures safe", () => {
  const store = mockStorage();
  store.setItem(PLAYED_KEY, "{not json");
  assert.deepEqual(readPlayed(store), []);
  assert.equal(savePlayed(store, { ...base(), source: "javascript:alert(1)" }), false);
  assert.equal(savePlayed(store, { ...base(), sessionId: "not-hash" }), false);
  const broken = { getItem() { throw Error("blocked"); }, setItem() { throw Error("blocked"); }, removeItem() { throw Error("blocked"); } };
  assert.deepEqual(readPlayed(broken), []);
  assert.equal(savePlayed(broken, base()), false);
  assert.equal(removePlayed(broken, "a".repeat(40), 0), false);
  assert.equal(clearPlayed(broken), false);
});
test("resume starts near last position, restarts completed videos, clamps limits", () => {
  assert.equal(replaySeconds(base()), 30);
  assert.equal(replaySeconds({ ...base(), position: 7 }), 0);
  assert.equal(replaySeconds({ ...base(), position: 116 }), 0);
  assert.equal(replaySeconds({ ...base(), position: 55, duration: 120 }), 52);
  assert.equal(savePlayed(mockStorage(), { ...base(), position: 999 }), true);
  const storage = mockStorage();
  savePlayed(storage, { ...base(), position: 999 });
  assert.equal(readPlayed(storage)[0].position, 120);
});
