//go:build integration

package main

import (
	"context"
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

// TestBrowserHLSFixtureHost is a CI-only loopback fixture host. It registers
// an actual torrent client and HLS manager, while the add endpoint returns
// the already seeded local torrent to avoid external trackers or networks.
// It is intentionally NOT exposed in normal builds.
func TestBrowserHLSFixtureHost(t *testing.T) {
	root := os.Getenv("BYTEPLAY_BROWSER_FIXTURE_ROOT")
	if root == "" {
		t.Skip("browser fixture root required")
	}
	encoder, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	prober, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	ffmpegPath, ffprobePath = encoder, prober
	// The fixture executes Go tests rather than main(), so it must mirror
	// the production checkFFmpeg() capability flag for /torrent/{id}/probe.
	ffmpegAvailable = true
	info := metainfo.Info{PieceLength: 256 << 10}
	if err := info.BuildFromFilePath(root); err != nil {
		t.Fatal(err)
	}
	encoded, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	meta := &metainfo.MetaInfo{InfoBytes: encoded}
	seedClient, err := torrent.NewClient(isolatedTorrentConfig(filepath.Dir(root), true))
	if err != nil {
		t.Fatal(err)
	}
	defer seedClient.Close()
	seed, err := seedClient.AddTorrent(meta)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-seed.Complete().On():
	case <-time.After(30 * time.Second):
		t.Fatal("fixture seeder not ready")
	}
	// Exercise the production torrent write-budget wrapper and HLS under
	// one shared ledger, not just independently passing API mocks.
	manager := newHLSManager(t.TempDir(), encoder)
	leecherRoot := t.TempDir()
	manager.torrentRoot = leecherRoot
	leecherConfig := isolatedTorrentConfig(leecherRoot, false)
	leecherConfig.DefaultStorage = newBudgetTorrentStorage(leecherRoot, manager)
	leecher, err := torrent.NewClient(leecherConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer leecher.Close()
	downloaded, err := leecher.AddTorrent(meta)
	if err != nil {
		t.Fatal(err)
	}
	downloaded.AddClientPeer(seedClient)
	hash := meta.HashInfoBytes().HexString()
	sessions.Store(hash, &TorrentSession{Client: leecher, Torrent: downloaded, LastUsed: time.Now(), ProbeCache: map[int]*ProbeResult{}})
	defer sessions.Delete(hash)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/torrent/add", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		respondWithJSON(w, http.StatusOK, map[string]string{"sessionId": hash})
	})
	mux.HandleFunc("/api/v1/torrent/", torrentHandler)
	mux.HandleFunc("/api/v1/hls/", manager.handler)
	mux.HandleFunc("/api/v1/capabilities", capabilitiesHandler)
	mux.HandleFunc("/api/v1/settings", settingsHandler)
	mux.HandleFunc("/__byteplay_browser_fixture", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		respondWithJSON(w, 200, map[string]string{"sessionId": hash})
	})
	mux.Handle("/", http.FileServer(http.Dir("./client")))
	listener, err := net.Listen("tcp", "127.0.0.1:3347")
	if err != nil {
		t.Fatal(err)
	}
	serverPort = listener.Addr().(*net.TCPAddr).Port
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	defer server.Close()
	defer manager.stopAll(10 * time.Second)
	go func() { _ = server.Serve(listener) }()

	// The external Playwright runner polls the fixture health endpoint and
	// controls test lifetime through a marker file, never sleeps blindly.
	marker := os.Getenv("BYTEPLAY_BROWSER_STOP_FILE")
	t.Logf("BYTEPLAY_BROWSER_FIXTURE_READY id=%s", hash)
	deadline := time.NewTimer(240 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			t.Fatal("browser fixture host timed out waiting for stop marker")
		case <-ticker.C:
			if marker != "" {
				if _, err := os.Stat(marker); err == nil {
					return
				}
			}
		}
	}
}
