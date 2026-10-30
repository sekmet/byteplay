/* Browser smoke for M7: real BytePlay HTTP server, mock metadata only for
 * deterministic watch-state UI. Real torrent→FFmpeg tests run in Go CI.
 * Screenshots are artifacts for manual review, not automatic visual approval.
 */
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { chromium } = require("playwright");

const BASE = process.env.BYTEPLAY_E2E_URL || "http://127.0.0.1:3347";
const OUTPUT = process.env.BYTEPLAY_SCREENSHOT_DIR || "/tmp/byteplay-ui-shots";
const TORRENT = "a".repeat(40);
const PLAYBACK = "b".repeat(32);
fs.mkdirSync(OUTPUT, { recursive: true });

async function run() {
  const browser = await chromium.launch({ headless: true });
  const errors = [];
  try {
    for (const viewport of [
      { width: 1920, height: 1080 },
      { width: 1536, height: 864 },
      { width: 1440, height: 900 },
      { width: 1366, height: 768 },
      { width: 1024, height: 768 },
      { width: 834, height: 1194 },
      { width: 768, height: 1024 },
      // 1440x900 desktop at 200% browser zoom has ~720x450 CSS pixels.
      // DPR 2 captures the corresponding physical-resolution evidence;
      // this is a reflow proxy, not a complete manual zoom audit.
      { width: 720, height: 450, scale: 2 },
      { width: 390, height: 844 },
      { width: 375, height: 812 },
      { width: 320, height: 720 },
      { width: 844, height: 390 }
    ]) {
      const page = await browser.newPage({
        viewport: { width: viewport.width, height: viewport.height },
        deviceScaleFactor: viewport.scale || 1
      });
      page.on("pageerror", error => errors.push(viewport.width + ": " + error.message));
      await page.goto(BASE, { waitUntil: "networkidle" });
      await page.locator("#bp-hero-title").waitFor({ state: "visible" });
      assert.match(await page.locator("#bp-hero-title").innerText(), /Your video/);
      assert.equal(await page.locator("#legacy-main").isVisible(), false);
      assert.equal(await page.locator("#bp-open").isVisible(), true);
      assert.equal(await page.locator("#bp-torrent-file").count(), 1);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      assert.ok(overflow <= 2, viewport.width + " home horizontally overflows by " + overflow + "px");
      await page.screenshot({ path: path.join(OUTPUT, "home-" + viewport.width + "x" + viewport.height + ".png"), fullPage: true });
      // Keyboard-only form submission must use the same validation path.
      await page.locator("#bp-magnet").focus();
      await page.keyboard.press("Enter");
      assert.match(await page.locator("#bp-home-error").innerText(), /Enter a magnet link/);
      await page.locator("#bp-settings").click();
      assert.equal(await page.locator("#settings-model").isVisible(), true, "legacy settings dialog must remain functional");
      if (viewport.width === 1920) {
        await page.locator('#settings-model .tab-btn[data-index="3"]').click();
        const cached = page.locator("#bp-startup-cache-percent");
        assert.equal(await cached.inputValue(), "9");
        await cached.fill("12");
        await page.locator("#bp-startup-cache-save").click();
        await page.waitForFunction(() => document.getElementById("bp-startup-cache-feedback")?.textContent.includes("Saved."));
        const stored = await page.request.get(BASE + "/api/v1/settings");
        assert.equal((await stored.json()).startupCachePercent, 12);
        await cached.fill("9");
        await page.locator("#bp-startup-cache-save").click();
        await page.waitForFunction(async () => (await (await fetch("/api/v1/settings")).json()).startupCachePercent === 9);
        await page.locator('#settings-model .tab-btn[data-index="0"]').click();
      }
      // The legacy modal must never inherit dark-on-dark / light-on-light
      // text from the global theme. Check both active/inactive buttons and
      // every save/test action using actual computed browser colors.
      const colorContrast = await page.evaluate(() => {
        const calculate = (color) => {
          const parts = color.match(/[0-9.]+/g)?.slice(0, 3).map(Number) || [0, 0, 0];
          const rgb = parts.map(x => {
            const v = x / 255;
            return v <= 0.04045 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
          });
          return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
        };
        const selectors = [
          "#settings-model .tab-btn.bg-primary",
          "#settings-model .tab-btn:not(.bg-primary)",
          "#settings-model #test-proxy",
          "#settings-model #proxy-settings-form button[type=submit]"
        ];
        return selectors.map(selector => {
          const e = document.querySelector(selector);
          const style = getComputedStyle(e);
          const a = calculate(style.color), b = calculate(style.backgroundColor);
          return { selector, ratio: (Math.max(a,b) + 0.05) / (Math.min(a,b) + 0.05),
            foreground: style.color, background: style.backgroundColor };
        });
      });
      for (const contrast of colorContrast) {
        assert.ok(contrast.ratio >= 4.5, "unreadable settings label: " + JSON.stringify(contrast));
      }
      if (viewport.width === 1920 || viewport.width === 390) {
        await page.screenshot({ path: path.join(OUTPUT, "settings-" + viewport.width + "x" + viewport.height + ".png"), fullPage: true });
      }
      await page.locator(".close-settings").first().click({ position: { x: 5, y: 5 } });
      assert.equal(await page.locator("#settings-model").isVisible(), false);
      await page.locator("#bp-settings").click();
      await page.keyboard.press("Escape");
      assert.equal(await page.locator("#settings-model").isVisible(), false,
        "Escape must dismiss Settings dialog");
      if ([1920, 1024, 720, 390, 375, 844].includes(viewport.width)) {
        await page.route("**/api/v1/torrent/add", route => route.fulfill({
          status: 200, contentType: "application/json", body: JSON.stringify({ sessionId: TORRENT })
        }));
        await page.route("**/api/v1/torrent/" + TORRENT, route => route.fulfill({
          status: 200, contentType: "application/json",
          body: JSON.stringify([{ index: 0, name: "Legal synthetic sample (H.264).mkv", size: 12345678 },
            { index: 1, name: "English.srt", size: 1200 }])
        }));
        await page.route("**/api/v1/hls/sessions", route => route.fulfill({
          status: 201, contentType: "application/json",
          body: JSON.stringify({ playbackId: PLAYBACK, state: "queued", generation: 1,
            playlistReady: false, statusUrl: "/api/v1/hls/" + PLAYBACK + "/status", segmentsReady: 0 })
        }));
        await page.route("**/api/v1/hls/" + PLAYBACK + "/status", route => route.fulfill({
          status: 200, contentType: "application/json",
          body: JSON.stringify({ playbackId: PLAYBACK, state: "preparing", generation: 1, mode: "copy",
            playlistReady: false, statusUrl: "/api/v1/hls/" + PLAYBACK + "/status",
            peers: 2, downloadBytesPerSecond: null, selectedFileBytesTotal: 12345678,
            selectedFileBytesCompleted: 1572864, segmentsReady: 0 })
        }));
        await page.route("**/api/v1/torrent/" + TORRENT + "/probe/0", route => route.fulfill({
          status: 200, contentType: "application/json", body: JSON.stringify({ duration: 60,
            streams: [{ type: "video", index: 0, codec: "h264" },
              { type: "audio", index: 1, codec: "aac", language: "en" }] })
        }));
        await page.locator("#bp-magnet").fill("magnet:?xt=urn:btih:" + TORRENT);
        await page.locator("#bp-open").click();
        await page.locator("#bp-watch").waitFor({ state: "visible" });
        await page.locator("#bp-stage").getByText("Preparing stream").waitFor();
        assert.equal(await page.locator("#bp-title").innerText(), "Legal synthetic sample (H.264).mkv");
        assert.equal(await page.locator("#bp-peers").innerText(), "2");
        assert.equal(await page.locator("#bp-speed").innerText(), "—", "speed must not be invented");
        await page.locator("#bp-info-toggle").click();
        assert.equal(await page.locator("#bp-info").isVisible(), true);
        const watchOverflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
        assert.ok(watchOverflow <= 2, viewport.width + " watch horizontally overflows by " + watchOverflow + "px");
        await page.screenshot({ path: path.join(OUTPUT, "watch-" + viewport.width + "x" + viewport.height + ".png"), fullPage: true });
        await page.locator("#bp-new").click();
        await page.locator("#bp-home").waitFor({ state: "visible" });
        if (viewport.width === 1920) {
          // Browser-local history survives page reload and re-opens the right
          // torrent/file using the existing authenticated add/HLS endpoints.
          await page.evaluate(({key, source}) => localStorage.setItem(key, JSON.stringify([{
            sessionId: source, fileIndex: 0, source: "magnet:?xt=urn:btih:" + source,
            title: "Legal synthetic sample (H.264).mkv", position: 35,
            duration: 60, size: 12345678, playedAt: Date.now()
          }])), { key: "byteplay.played.v1", source: TORRENT });
          await page.reload({ waitUntil: "networkidle" });
          assert.equal(await page.locator(".bp-played-item").count(), 1);
          assert.equal(await page.locator(".bp-played-item h3").innerText(), "Legal synthetic sample (H.264).mkv");
          assert.equal(await page.locator(".bp-played-actions .bp-played-resume").count(), 1);
          // First test Resume: a ready generation must be restarted at the
          // saved absolute timestamp, rather than playing from zero.
          let requestedSeek = null;
          await page.unroute("**/api/v1/hls/" + PLAYBACK + "/status");
          await page.route("**/api/v1/hls/" + PLAYBACK + "/status", route => route.fulfill({
            status: 200, contentType: "application/json",
            body: JSON.stringify({ playbackId: PLAYBACK, state: "ready", generation: 1,
              playlistReady: true, playlistUrl: "/api/v1/hls/" + PLAYBACK + "/g/1/index.m3u8",
              durationSeconds: 60, startSeconds: 0, segmentsReady: 2 })
          }));
          await page.route("**/api/v1/hls/" + PLAYBACK + "/seek", async route => {
            requestedSeek = route.request().postDataJSON();
            await route.fulfill({ status: 200, contentType: "application/json",
              body: JSON.stringify({ state: "preparing", generation: 2 }) });
          });
          await page.getByRole("button", { name: "Resume: Legal synthetic sample (H.264).mkv" }).click();
          await page.waitForFunction(() => document.querySelector("#bp-watch:not([hidden])") != null);
          await page.waitForFunction(() => document.querySelector("#bp-player-state")?.textContent === "Preparing new generation", null, { timeout: 10000 });
          assert.equal(requestedSeek?.seconds, 32, "Resume must restart at saved position minus 3s lead-in");
          await page.locator("#bp-new").click();
          await page.locator("#bp-home").waitFor({ state: "visible" });
          // Replay must restart from 0 using the saved magnet and file index.
          await page.getByRole("button", { name: "Replay: Legal synthetic sample (H.264).mkv" }).click();
          await page.locator("#bp-watch").waitFor({ state: "visible" });
          assert.equal(await page.locator("#bp-title").innerText(), "Legal synthetic sample (H.264).mkv");
          await page.locator("#bp-new").click();
          await page.locator("#bp-home").waitFor({ state: "visible" });
          await page.getByRole("button", { name: "Remove from played: Legal synthetic sample (H.264).mkv" }).click();
          assert.equal(await page.locator(".bp-played-item").count(), 0);
          assert.equal(await page.locator("#bp-played-empty").isVisible(), true);
        }
      }
      await page.close();
    }
    assert.deepEqual(errors, [], "uncaught frontend exceptions");
    console.log("BYTEPLAY_M7_BROWSER_SMOKE_PASS");
  } finally {
    await browser.close();
  }
}
run().catch(error => { console.error(error); process.exitCode = 1; });
