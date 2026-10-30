//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// This runs with an actual loopback torrent peer and the real HTTP stream,
// FFprobe, FFmpeg, generation-1 manifest and finalized TS endpoints.
func TestTorrentBackedHLSReadyBeforeWholeTorrentCompletion(t *testing.T) {
	root := os.Getenv("BYTEPLAY_TEST_FIXTURE_ROOT")
	if root == "" {
		t.Skip("requires locally generated legal fixture")
	}
	encoder, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	prober, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	prevProbe, prevPort := ffprobePath, serverPort
	ffprobePath = prober
	t.Cleanup(func() { ffprobePath = prevProbe; serverPort = prevPort })
	info := metainfo.Info{PieceLength: 256 << 10}
	if err := info.BuildFromFilePath(root); err != nil {
		t.Fatal(err)
	}
	encoded, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := &metainfo.MetaInfo{InfoBytes: encoded}
	seedClient, err := torrent.NewClient(isolatedTorrentConfig(filepath.Dir(root), true))
	if err != nil {
		t.Fatal(err)
	}
	defer seedClient.Close()
	seed, err := seedClient.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-seed.Complete().On():
	case <-time.After(30 * time.Second):
		t.Fatal("local seeder did not verify torrent files")
	}
	leechClient, err := torrent.NewClient(isolatedTorrentConfig(t.TempDir(), false))
	if err != nil {
		t.Fatal(err)
	}
	defer leechClient.Close()
	leech, err := leechClient.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	leech.AddClientPeer(seedClient)
	id := mi.HashInfoBytes().HexString()
	idx := -1
	for i, file := range leech.Files() {
		if filepath.Base(file.DisplayPath()) == "clip.mp4" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("fixture mp4 not included in torrent")
	}
	sessions.Store(id, &TorrentSession{Client: leechClient, Torrent: leech, LastUsed: time.Now()})
	defer sessions.Delete(id)

	manager := newHLSManager(t.TempDir(), encoder)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/torrent/", torrentHandler)
	mux.HandleFunc("/api/v1/hls/", manager.handler)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverPort = listener.Addr().(*net.TCPAddr).Port
	httpServer := &http.Server{Handler: mux}
	defer httpServer.Close()
	go httpServer.Serve(listener)
	base := fmt.Sprintf("http://127.0.0.1:%d", serverPort)
	client := &http.Client{Timeout: 25 * time.Second}
	body := fmt.Sprintf("{\"sessionId\":%q,\"fileIndex\":%d}", id, idx)
	resp, err := client.Post(base+"/api/v1/hls/sessions", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	created, readErr := io.ReadAll(io.LimitReader(resp.Body, 8192))
	resp.Body.Close()
	if readErr != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("HLS create: %d body=%s err=%v", resp.StatusCode, created, readErr)
	}
	var ticket hlsStatus
	if err := json.Unmarshal(created, &ticket); err != nil {
		t.Fatal(err)
	}
	if ticket.PlaybackID == "" || ticket.Generation != 1 || ticket.StatusURL == "" {
		t.Fatalf("invalid create response: %+v", ticket)
	}
	defer func() {
		req, _ := http.NewRequest(http.MethodDelete, base+"/api/v1/hls/"+ticket.PlaybackID, nil)
		if response, err := client.Do(req); err == nil {
			response.Body.Close()
		}
		manager.stopAll(10 * time.Second)
	}()
	deadline := time.Now().Add(65 * time.Second)
	var ready hlsStatus
	for time.Now().Before(deadline) {
		response, err := client.Get(base + ticket.StatusURL)
		if err != nil {
			t.Fatal(err)
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&ready)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if ready.State == "failed" || ready.State == "cancelled" {
			t.Fatalf("HLS failed: %+v", ready)
		}
		if ready.PlaylistReady {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !ready.PlaylistReady || ready.PlaylistURL == nil || ready.SegmentsReady < 2 {
		t.Fatalf("HLS did not become ready after two segments: %+v", ready)
	}
	if ready.TorrentBytesCompleted >= ready.TorrentBytesTotal {
		t.Fatalf("HLS waited for entire seeded torrent: %+v", ready)
	}
	playlistResponse, err := client.Get(base + *ready.PlaylistURL)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := io.ReadAll(io.LimitReader(playlistResponse.Body, 65536))
	playlistResponse.Body.Close()
	if err != nil || playlistResponse.StatusCode != 200 {
		t.Fatalf("HLS manifest status=%d err=%v", playlistResponse.StatusCode, err)
	}
	if !bytes.HasPrefix(manifest, []byte("#EXTM3U")) {
		t.Fatalf("invalid manifest: %s", manifest)
	}
	segments := 0
	for _, line := range bytes.Split(manifest, []byte("\n")) {
		name := string(bytes.TrimSpace(line))
		if !hlsSegmentName.MatchString(name) {
			continue
		}
		segments++
		response, err := client.Get(base + "/api/v1/hls/" + ticket.PlaybackID + "/g/1/" + name)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 512))
		response.Body.Close()
		if response.StatusCode != 200 || err != nil || len(data) == 0 {
			t.Fatalf("missing finalized TS segment %s status=%d err=%v", name, response.StatusCode, err)
		}
		if segments >= 2 {
			break
		}
	}
	if segments < 2 {
		t.Fatalf("ready manifest had %d finalized segments", segments)
	}
	// An invalid audio track is rejected before cancelling the existing job.
	invalidAudio := fmt.Sprintf("{\"seconds\":8,\"audioStream\":999}")
	resp, err = client.Post(base+"/api/v1/hls/"+ticket.PlaybackID+"/seek", "application/json", bytes.NewBufferString(invalidAudio))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid audio should return 400, got %d", resp.StatusCode)
	}
	if m := manager.get(ticket.PlaybackID); m == nil || m.snapshot().Generation != 1 {
		t.Fatal("invalid audio seek superseded current playback")
	}
	for _, action := range []string{"pause", "pause", "resume", "resume"} {
		response, err := client.Post(base+"/api/v1/hls/"+ticket.PlaybackID+"/control",
			"application/json", bytes.NewBufferString("{\"action\":\""+action+"\"}"))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("control %s returned %d", action, response.StatusCode)
		}
	}
	// A far seek and subsequent audio-track selection must restart under
	// immutable generation IDs. Old playlist URLs must return 410.
	for _, step := range []struct {
		seconds int
		audio   bool
	}{
		{8, false}, {14, true}, {20, false}, {26, false},
	} {
		payload := fmt.Sprintf("{\"seconds\":%d}", step.seconds)
		if step.audio {
			payload = fmt.Sprintf("{\"seconds\":%d,\"audioStream\":1}", step.seconds)
		}
		previous := ready.PlaylistURL
		response, err := client.Post(base+"/api/v1/hls/"+ticket.PlaybackID+"/seek", "application/json", bytes.NewBufferString(payload))
		if err != nil {
			t.Fatal(err)
		}
		var started struct {
			PlaybackID string `json:"playbackId"`
			Generation int    `json:"generation"`
			State      string `json:"state"`
			StatusURL  string `json:"statusUrl"`
		}
		err = json.NewDecoder(response.Body).Decode(&started)
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted || err != nil || started.State != "preparing" {
			t.Fatalf("seek response %d %+v err=%v", response.StatusCode, started, err)
		}
		if started.Generation != ready.Generation+1 {
			t.Fatalf("generation skipped %+v previous %+v", started, ready)
		}
		if previous != nil {
			stale, err := client.Get(base + *previous)
			if err != nil {
				t.Fatal(err)
			}
			stale.Body.Close()
			if stale.StatusCode != http.StatusGone {
				t.Fatalf("old generation returned %d instead of 410", stale.StatusCode)
			}
		}
		limit := time.Now().Add(30 * time.Second)
		for time.Now().Before(limit) {
			statusResp, err := client.Get(base + started.StatusURL)
			if err != nil {
				t.Fatal(err)
			}
			err = json.NewDecoder(io.LimitReader(statusResp.Body, 8192)).Decode(&ready)
			statusResp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if ready.State == "failed" || ready.State == "cancelled" {
				t.Fatalf("seek generation failed: %+v", ready)
			}
			if ready.Generation == started.Generation && ready.PlaylistReady {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if ready.Generation != started.Generation || !ready.PlaylistReady || ready.PlaylistURL == nil || ready.SegmentsReady < 2 {
			t.Fatalf("seek generation not ready: %+v", ready)
		}
		// Report the source GOP keyframe rather than the requested second.
		if ready.StartSeconds > float64(step.seconds)+0.05 || ready.StartSeconds < float64(step.seconds)-12 {
			t.Fatalf("effective keyframe %.3f outside GOP before %ds", ready.StartSeconds, step.seconds)
		}
		check, err := client.Get(base + *ready.PlaylistURL)
		if err != nil {
			t.Fatal(err)
		}
		bits, _ := io.ReadAll(io.LimitReader(check.Body, 65536))
		check.Body.Close()
		if check.StatusCode != 200 || !bytes.Contains(bits, []byte("#EXTM3U")) {
			t.Fatalf("generation %d manifest invalid HTTP %d", ready.Generation, check.StatusCode)
		}
	}
	t.Logf("torrent_hls_ready id=%s mode=%v segments=%d torrent_completed=%d/%d",
		ticket.PlaybackID, ready.Mode, ready.SegmentsReady, ready.TorrentBytesCompleted, ready.TorrentBytesTotal)
}
