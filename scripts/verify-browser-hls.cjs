// Playwright decodes real HLS from a loopback BitTorrent peer + FFmpeg.
// Unlike verify-ui.cjs, this test does NOT mock the torrent or HLS HTTP APIs.
const assert=require("node:assert/strict");
const fs=require("node:fs");
const path=require("node:path");
const { chromium, firefox }=require("playwright");
const BROWSER=process.env.BYTEPLAY_BROWSER_ENGINE || "chrome";

const BASE=process.env.BYTEPLAY_E2E_URL || "http://127.0.0.1:3347";
const OUT=process.env.BYTEPLAY_SCREENSHOT_DIR || "/tmp/byteplay-browser-media";
fs.mkdirSync(OUT,{recursive:true});

async function run() {
  const browser=BROWSER==="firefox"
    ? await firefox.launch({headless:true,firefoxUserPrefs:{"media.autoplay.default":0}})
    : await chromium.launch({channel:"chrome",headless:true,args:["--autoplay-policy=no-user-gesture-required"]});
  const page=await browser.newPage({viewport:{width:1440,height:900}});
  const errors=[], segments=[], playlists=[];
  page.on("pageerror",err=>errors.push(err.message));
  page.on("response",res=>{
    const url=res.url();
    if (/\/g\/[0-9]+\/segment-[0-9]{6}\.ts$/.test(url))segments.push({status:res.status(),url});
    if (/\/g\/[0-9]+\/index\.m3u8$/.test(url))playlists.push({status:res.status(),url});
  });
  try {
    const fixture=await page.request.get(BASE+"/__byteplay_browser_fixture");
    assert.equal(fixture.status(),200,"fixture server not running");
    const {sessionId}=await fixture.json();
    assert.match(sessionId,/^[0-9a-f]{40}$/);
    await page.goto(BASE,{waitUntil:"domcontentloaded"});
    const supportsAVC=await page.evaluate(()=>Boolean(window.MediaSource?.isTypeSupported?.('video/mp4; codecs="avc1.42E01E, mp4a.40.2"')));
    assert.equal(supportsAVC,true,BROWSER+" must support MSE H.264/AAC for the HLS decode gate");
    await page.locator("#bp-magnet").fill("magnet:?xt=urn:btih:"+sessionId);
    await page.locator("#bp-open").click();
    await page.locator("#bp-watch").waitFor({state:"visible",timeout:20000});
    await page.waitForFunction(()=>{
      const stage=document.getElementById("bp-stage")?.textContent;
      const error=document.getElementById("bp-watch-error");
      if (error && !error.hidden) throw new Error("Playback status: "+error.textContent);
      return stage==="Ready to play" || stage==="Playing" || stage==="Buffering";
    },null,{timeout:55000});
    // Chromium can need a user gesture even when a source is ready.
    const playerVideo=page.locator(".bp-player-surface video");
    await playerVideo.waitFor({state:"attached"});
    await page.evaluate(()=>{
      const video=document.querySelector(".bp-player-surface video");
      if (!video) throw new Error("Video.js tech missing");
      // Do not await a play() promise before installing the bounded playback
      // oracle: MSE may leave it pending indefinitely during a stall.
      void video.play().catch(()=>{});
    });
    // Require decoded visual frames AND advancing media time. A 200 playlist
    // response, an HLS manifest, or a spinning player is not sufficient.
    await page.waitForFunction(()=>{
      const video=document.querySelector(".bp-player-surface video");
      if (!video) return false;
      const quality=video.getVideoPlaybackQuality?.();
      const decoded=quality?.totalVideoFrames??video.webkitDecodedFrameCount??0;
      return video.currentTime >= 1.2 && video.readyState >= 2 && decoded > 0;
    },null,{timeout:45000});
    const decoded=await page.evaluate(()=>{
      const v=document.querySelector(".bp-player-surface video");
      return {currentTime:v.currentTime,readyState:v.readyState,decodedFrames:v.getVideoPlaybackQuality?.()?.totalVideoFrames ?? v.webkitDecodedFrameCount ?? null,
        width:v.videoWidth,height:v.videoHeight,error:v.error?.message||null};
    });
    assert.ok(decoded.width>0 && decoded.height>0,"decoded dimensions missing");
    assert.ok(decoded.currentTime>=1.2,"no playback time advancement");
    assert.ok(decoded.decodedFrames>0,"no decoded video frames");
    assert.ok(playlists.some(x=>x.status===200),"browser did not fetch an HLS playlist");
    assert.ok(segments.some(x=>x.status===200),"browser did not fetch HLS TS segments");
    assert.deepEqual(errors,[],"unhandled browser JavaScript exception");
    if (BROWSER==="firefox") {
      // Independently require real Firefox decode and progression, not a
      // successful playlist fetch, mock metadata or an HLS capability flag.
      console.log("BYTEPLAY_FIREFOX_REAL_HLS_PASS "+JSON.stringify({
        decoded, playlist200:playlists.filter(x=>x.status===200).length,
        segments200:segments.filter(x=>x.status===200).length
      }));
      await page.screenshot({path:path.join(OUT,"real-hls-firefox-1440x900.png"),fullPage:true});
      return;
    }
    // The synthetic 32-second fixture is often fully buffered before this
    // check. A timebar seek inside that buffer must stay local in Video.js;
    // requiring a new HLS generation here would be a false acceptance oracle.
    await page.locator("#bp-seek").evaluate(input=>{
      input.value="14";
      input.dispatchEvent(new Event("change",{bubbles:true}));
    });
    await page.waitForFunction(()=>{
      const video=document.querySelector(".bp-player-surface video");
      return video && video.currentTime>=13 && video.currentTime<=15.5;
    },null,{timeout:15000});
    await page.evaluate(()=>{
      const player=window.videojs?.getPlayer?.("bp-video");
      void player?.play()?.catch(()=>{});
    });
    await page.waitForFunction(()=>{
      const v=document.querySelector(".bp-player-surface video");
      return v && v.readyState>=2 && v.currentTime>=14.2 && !v.paused;
    },null,{timeout:20000});
    const paused=page.waitForResponse(res=>
      res.url().endsWith("/control") && res.request().method()==="POST" &&
      res.status()===200,{timeout:15000});
    await page.evaluate(()=>window.videojs.getPlayer("bp-video").pause());
    await paused;
    const resumed=page.waitForResponse(res=>
      res.url().endsWith("/control") && res.request().method()==="POST" &&
      res.status()===200,{timeout:15000});
    await page.evaluate(()=>{
      void window.videojs.getPlayer("bp-video").play().catch(()=>{});
    });
    await resumed;
    await page.locator("#bp-captions").setInputFiles({
      name:"local-caption.srt",mimeType:"text/plain",
      buffer:Buffer.from("1\n00:00:01,000 --> 00:00:03,000\nSynthetic cue\n")
    });
    await page.waitForFunction(()=>
      document.getElementById("bp-player-detail")?.textContent.includes("Subtitle file added"),
      null,{timeout:10000});
    await page.screenshot({path:path.join(OUT,"real-hls-buffered-seek-and-caption-1440x900.png"),fullPage:true});
    // A Played entry is created only after real browser media playback.
    const stored = await page.evaluate(() => JSON.parse(localStorage.getItem("byteplay.played.v1") || "[]"));
    assert.ok(stored.some(item => item.sessionId === sessionId &&
      item.title.endsWith("clip.mp4") && item.playedAt > 0),
      "decoded Chrome playback did not persist browser-local Played history");
    await page.screenshot({path:path.join(OUT,"real-hls-playback-1440x900.png"),fullPage:true});
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.locator(".bp-played-item").waitFor({ state: "visible", timeout: 10000 });
    assert.match(await page.locator(".bp-played-item h3").first().innerText(), /clip\.mp4$/);
    // Reopen a 30-minute synthetic MKV through the real torrent file picker.
    // Unlike the short clip, 600 seconds is outside the browser's buffered
    // range and must request an isolated HLS generation from the Go server.
    await page.locator("#bp-magnet").fill("magnet:?xt=urn:btih:"+sessionId);
    await page.locator("#bp-open").click();
    await page.locator("#bp-watch").waitFor({state:"visible",timeout:30000});
    await page.locator("#bp-video-select").selectOption({label:"long.mkv"});
    await page.waitForFunction(()=>
      document.getElementById("bp-title")?.textContent.includes("long.mkv"),
      null,{timeout:30000});
    await page.waitForFunction(()=>{
      const slider=document.getElementById("bp-seek");
      const stage=document.getElementById("bp-stage")?.textContent;
      return Number(slider?.max)>=1750 &&
        (stage==="Ready to play" || stage==="Playing" || stage==="Buffering");
    },null,{timeout:100000});
    const longSeekAccepted=page.waitForResponse(res=>
      res.url().endsWith("/seek") && res.request().method()==="POST" &&
      res.status()===202,{timeout:30000});
    const longManifest=page.waitForResponse(res=>
      res.url().includes("/g/2/index.m3u8") && res.status()===200,{timeout:85000});
    await page.locator("#bp-seek").evaluate(slider=>{
      slider.value="600";
      slider.dispatchEvent(new Event("change",{bubbles:true}));
    });
    await longSeekAccepted;
    await longManifest;
    await page.evaluate(()=>{
      void window.videojs?.getPlayer?.("bp-video")?.play()?.catch(()=>{});
    });
    await page.waitForFunction(()=>{
      const v=document.querySelector(".bp-player-surface video");
      return v && v.readyState>=2 && v.currentTime>=1.2 && v.currentTime<15;
    },null,{timeout:65000});
    const absoluteLabel=await page.locator("#bp-time").innerText();
    assert.ok(/^(09|10):[0-5][0-9] \/ 30:/.test(absoluteLabel),
      "absolute playback timestamp did not reflect the 10-minute generation: "+absoluteLabel);
    await page.screenshot({path:path.join(OUT,"real-hls-long-mkv-far-seek-1440x900.png"),fullPage:true});
    console.log("BYTEPLAY_LONG_MKV_FAR_SEEK_PASS "+JSON.stringify({absoluteLabel}));
    // A different audio track must restart the SAME playback with a fresh,
    // immutable generation; selecting an existing track is not sufficient.
    await page.locator("#bp-audio-wrap").waitFor({state:"visible",timeout:20000});
    const tracks=await page.locator("#bp-audio option").evaluateAll(opts=>
      opts.map(option=>Number(option.value)));
    assert.deepEqual(tracks,[1,2],"fixture must expose two distinct audio indexes");
    const audioChanged=page.waitForResponse(res=>
      res.url().endsWith("/seek") && res.request().method()==="POST" &&
      res.status()===202 && res.request().postDataJSON()?.audioStream===2,{timeout:30000});
    const thirdManifest=page.waitForResponse(res=>
      res.url().includes("/g/3/index.m3u8") && res.status()===200,{timeout:85000});
    await page.locator("#bp-audio").selectOption("2");
    await audioChanged;
    await thirdManifest;
    await page.evaluate(()=>{void window.videojs.getPlayer("bp-video").play().catch(()=>{});});
    await page.waitForFunction(()=>{
      const v=document.querySelector(".bp-player-surface video");
      return v && v.readyState>=2 && v.currentTime>=1 && v.currentTime<15;
    },null,{timeout:45000});
    const reverseAccepted=page.waitForResponse(res=>
      res.url().endsWith("/seek") && res.request().method()==="POST" &&
      res.status()===202 && Math.abs(res.request().postDataJSON()?.seconds-120)<1,{timeout:30000});
    const fourthManifest=page.waitForResponse(res=>
      res.url().includes("/g/4/index.m3u8") && res.status()===200,{timeout:85000});
    await page.locator("#bp-seek").evaluate(slider=>{
      slider.value="120";
      slider.dispatchEvent(new Event("change",{bubbles:true}));
    });
    await reverseAccepted;
    await fourthManifest;
    await page.evaluate(()=>{void window.videojs.getPlayer("bp-video").play().catch(()=>{});});
    await page.waitForFunction(()=>{
      const v=document.querySelector(".bp-player-surface video");
      return v && v.readyState>=2 && v.currentTime>=1 && v.currentTime<15;
    },null,{timeout:45000});
    const reverseLabel=await page.locator("#bp-time").innerText();
    assert.match(reverseLabel,/^0[12]:[0-5][0-9] \/ 30:/,
      "backward seek did not restore absolute timeline");
    console.log("BYTEPLAY_MULTI_AUDIO_REVERSE_SEEK_PASS "+JSON.stringify({tracks,reverseLabel}));
    await page.screenshot({path:path.join(OUT,"real-hls-audio-and-reverse-seek-1440x900.png"),fullPage:true});
    console.log("BYTEPLAY_REAL_BROWSER_HLS_PASS "+JSON.stringify({
      decoded,playlist200:playlists.filter(x=>x.status===200).length,
      segments200:segments.filter(x=>x.status===200).length}));
  }catch(err){
    const state=await page.evaluate(()=>{
      const v=document.querySelector(".bp-player-surface video");
      const player=window.videojs?.getPlayer?.("bp-video");
      const src=player?.currentSource?.();
      const Html5=window.videojs?.getTech?.("Html5");
      const candidate={src:src?.src||"/test.m3u8",type:"application/x-mpegURL"};
      const mediaSupported={
        MediaSource:typeof window.MediaSource,
        isSupported:window.MediaSource?.isTypeSupported?.('video/mp4; codecs="avc1.42E01E, mp4a.40.2"'),
        VHS:typeof window.videojs?.Vhs,
        hlsHtml5Play:Html5?.canPlaySource?.(candidate) || null,
        source:src,version:window.videojs?.VERSION
      };
      return {mediaSupported,stage:document.getElementById("bp-stage")?.textContent,
        error:document.getElementById("bp-watch-error")?.textContent,
        overlay:document.getElementById("bp-overlay-title")?.textContent,
        video:v?{readyState:v.readyState,currentTime:v.currentTime,error:v.error?.message}:null};
    }).catch(()=>({}));
    console.error("M7 browser-media diagnostics",JSON.stringify({state,errors,segments,playlists}));
    await page.screenshot({path:path.join(OUT,"real-hls-failure-1440x900.png"),fullPage:true}).catch(()=>{});
    throw err;
  }finally{
    await page.close();
    await browser.close();
  }
}
run().catch(error=>{console.error(error);process.exitCode=1});
