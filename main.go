package main

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"net/url"
	"path/filepath"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/net/proxy"
)

var (
	currentSettings Settings
	settingsMutex   sync.RWMutex
)

type TorrentSession struct {
	Client      *torrent.Client
	Torrent     *torrent.Torrent
	Port        int
	LastUsed    time.Time // guarded by lifecycleMu
	lifecycleMu sync.Mutex
	active      int
	closing     bool
	stopCtx     context.Context
	stopCancel  context.CancelFunc
	drained     chan struct{}
	ProbeMu     sync.Mutex
	ProbeCache  map[int]*ProbeResult
}

type ProbeStream struct {
	Index       int     `json:"index"` // absolute ffmpeg stream index
	Type        string  `json:"type"`  // video | audio | subtitle
	Codec       string  `json:"codec"`
	Profile     string  `json:"profile,omitempty"`
	PixelFormat string  `json:"pixelFormat,omitempty"`
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	FrameRate   float64 `json:"frameRate,omitempty"`
	Language    string  `json:"language,omitempty"`
	Title       string  `json:"title,omitempty"`
	Channels    int     `json:"channels,omitempty"`
	Default     bool    `json:"default"`
}

type ProbeResult struct {
	Container string        `json:"container"`
	Duration  float64       `json:"duration"`
	Streams   []ProbeStream `json:"streams"`
}

type Settings struct {
	EnableProxy          bool   `json:"enableProxy"`
	ProxyURL             string `json:"proxyUrl"`
	EnableProwlarr       bool   `json:"enableProwlarr"`
	ProwlarrHost         string `json:"prowlarrHost"`
	ProwlarrApiKey       string `json:"prowlarrApiKey"`
	EnableJackett        bool   `json:"enableJackett"`
	JackettHost          string `json:"jackettHost"`
	JackettApiKey        string `json:"jackettApiKey"`
	SearchTimeoutSeconds int    `json:"searchTimeoutSeconds"`
	SkipTLSVerify        bool   `json:"skipTlsVerify"`
	// Nil means an existing configuration uses the default 9 percent.
	StartupCachePercent *int `json:"startupCachePercent"`
}

func searchTimeout() time.Duration {
	settingsMutex.RLock()
	defer settingsMutex.RUnlock()
	if currentSettings.SearchTimeoutSeconds > 0 {
		return time.Duration(currentSettings.SearchTimeoutSeconds) * time.Second
	}
	return 30 * time.Second
}

type ProxySettings struct {
	EnableProxy bool   `json:"enableProxy"`
	ProxyURL    string `json:"proxyUrl"`
}

type ProwlarrSettings struct {
	EnableProwlarr bool   `json:"enableProwlarr"`
	ProwlarrHost   string `json:"prowlarrHost"`
	ProwlarrApiKey string `json:"prowlarrApiKey"`
	// Shared search options, editable from either indexer tab
	SearchTimeoutSeconds int  `json:"searchTimeoutSeconds"`
	SkipTLSVerify        bool `json:"skipTlsVerify"`
}

type JackettSettings struct {
	EnableJackett bool   `json:"enableJackett"`
	JackettHost   string `json:"jackettHost"`
	JackettApiKey string `json:"jackettApiKey"`
	// Shared search options, editable from either indexer tab
	SearchTimeoutSeconds int  `json:"searchTimeoutSeconds"`
	SkipTLSVerify        bool `json:"skipTlsVerify"`
}

var (
	sessions  sync.Map
	usedPorts sync.Map
	portMutex sync.Mutex
)

type ringLogBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func (b *ringLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		b.lines = append(b.lines, line)
	}
	if over := len(b.lines) - b.max; over > 0 {
		b.lines = b.lines[over:]
	}
	return len(p), nil
}

func (b *ringLogBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

var logBuffer = &ringLogBuffer{max: 500}

var (
	serverPort            = 3347
	ffmpegPath            string
	ffprobePath           string
	ffmpegAvailable       bool
	ffmpegVersion         string
	remuxSlots            chan struct{}
	loopbackInternalToken string
)

func checkFFmpeg() {
	maxRemux := 3
	if v, err := strconv.Atoi(os.Getenv("BITPLAY_MAX_REMUX")); err == nil && v > 0 {
		maxRemux = v
	}
	remuxSlots = make(chan struct{}, maxRemux)

	var err error
	ffmpegPath, err = exec.LookPath("ffmpeg")
	if err != nil {
		log.Println("ffmpeg not found in PATH - MKV remuxing and audio track selection disabled")
		return
	}
	ffprobePath, err = exec.LookPath("ffprobe")
	if err != nil {
		log.Println("ffprobe not found in PATH - MKV remuxing and audio track selection disabled")
		return
	}
	if out, verr := exec.Command(ffmpegPath, "-version").Output(); verr == nil {
		fields := strings.Fields(strings.SplitN(string(out), "\n", 2)[0])
		if len(fields) >= 3 {
			ffmpegVersion = fields[2]
		}
	}
	ffmpegAvailable = true
	log.Printf("ffmpeg %s detected - remuxing enabled (max %d concurrent, BITPLAY_MAX_REMUX to change)", ffmpegVersion, maxRemux)
}

func capabilitiesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"ffmpeg":  ffmpegAvailable,
		"version": ffmpegVersion,
	})
}

// ffmpeg reads torrent files through the stream endpoint so the input is
// seekable and partially-downloaded files work
func loopbackStreamURL(sessionID string, fileIndex int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/api/v1/torrent/%s/stream/%d", serverPort, sessionID, fileIndex)
}

type limitedWriter struct {
	buf bytes.Buffer
	max int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.buf.Len() < l.max {
		l.buf.Write(p[:min(len(p), l.max-l.buf.Len())])
	}
	return len(p), nil
}

// Helper function to format file sizes
func formatSize(sizeInBytes float64) string {
	if sizeInBytes < 1024 {
		return fmt.Sprintf("%.0f B", sizeInBytes)
	}

	sizeInKB := sizeInBytes / 1024
	if sizeInKB < 1024 {
		return fmt.Sprintf("%.2f KB", sizeInKB)
	}

	sizeInMB := sizeInKB / 1024
	if sizeInMB < 1024 {
		return fmt.Sprintf("%.2f MB", sizeInMB)
	}

	sizeInGB := sizeInMB / 1024
	return fmt.Sprintf("%.2f GB", sizeInGB)
}

// Basic auth for the whole app when BITPLAY_AUTH_USERNAME and
// BITPLAY_AUTH_PASSWORD are both set; a no-op otherwise
func basicAuthMiddleware(next http.Handler) http.Handler {
	username := os.Getenv("BITPLAY_AUTH_USERNAME")
	password := os.Getenv("BITPLAY_AUTH_PASSWORD")
	if username == "" && password == "" {
		return next
	}
	if username == "" || password == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Authentication configuration incomplete", http.StatusServiceUnavailable)
		})
	}
	var tokenBytes [32]byte
	if _, err := crand.Read(tokenBytes[:]); err != nil {
		log.Fatal("failed to establish internal loopback authentication")
	}
	loopbackInternalToken = hex.EncodeToString(tokenBytes[:])
	userHash := sha256.Sum256([]byte(username))
	passHash := sha256.Sum256([]byte(password))
	log.Println("Basic authentication enabled")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopbackRequest(r) && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-BytePlay-Internal")), []byte(loopbackInternalToken)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if ok {
			uh := sha256.Sum256([]byte(user))
			ph := sha256.Sum256([]byte(pass))
			if subtle.ConstantTimeCompare(userHash[:], uh[:]) == 1 && subtle.ConstantTimeCompare(passHash[:], ph[:]) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="BytePlay", charset="UTF-8"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

// Independent transports prevent proxy-setting races and state bleed.
func createSelectiveProxyClient() *http.Client {
	settingsMutex.RLock()
	snapshot := currentSettings
	settingsMutex.RUnlock()
	timeout := time.Duration(snapshot.SearchTimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > 120*time.Second {
		timeout = 30 * time.Second
	}
	transport := &http.Transport{
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConnsPerHost:   10,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: snapshot.SkipTLSVerify},
	}
	if snapshot.EnableProxy {
		dialer, err := createProxyDialer(snapshot.ProxyURL)
		if err != nil {
			return &http.Client{Timeout: timeout, Transport: errorTransport{}}
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if d, ok := dialer.(proxy.ContextDialer); ok {
				return d.DialContext(ctx, network, addr)
			}
			return dialer.Dial(network, addr)
		}
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || len(via) == 0 || !sameOrigin(req.URL, via[0].URL) {
				return errors.New("cross-origin redirect denied")
			}
			return nil
		},
	}
}

type errorTransport struct{}

func (errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("invalid SOCKS5 proxy configuration")
}

// Create a proxy dialer for SOCKS5
func createProxyDialer(proxyURL string) (proxy.Dialer, error) {
	proxyURLParsed, err := url.Parse(proxyURL)
	if err != nil || proxyURLParsed.Scheme != "socks5" || proxyURLParsed.Host == "" {
		return nil, errors.New("invalid SOCKS5 proxy URL")
	}

	// Extract auth information
	auth := &proxy.Auth{}
	if proxyURLParsed.User != nil {
		auth.User = proxyURLParsed.User.Username()
		if password, ok := proxyURLParsed.User.Password(); ok {
			auth.Password = password
		}
	}

	// Create a SOCKS5 dialer
	return proxy.SOCKS5("tcp", proxyURLParsed.Host, auth, proxy.Direct)
}

// Implement a port allocation function to prevent conflicts
func getAvailablePort() int {
	portMutex.Lock()
	defer portMutex.Unlock()

	// Try up to 50 times to find an unused port
	for i := 0; i < 50; i++ {
		// Generate a random port in the high range
		port := 10000 + rand.Intn(50000)

		// Check if this port is already in use by our app
		if _, exists := usedPorts.Load(port); !exists {
			// Mark this port as used
			usedPorts.Store(port, true)
			return port
		}
	}

	// If we can't find an available port, return a very high random port
	// as a last resort
	return 60000 + rand.Intn(5000)
}

// Release a port when we're done with it
func releasePort(port int) {
	portMutex.Lock()
	defer portMutex.Unlock()
	usedPorts.Delete(port)
}

// Initialize the torrent client with proxy settings
func initTorrentWithProxy() (*torrent.Client, int, error) {
	settingsMutex.RLock()
	enableProxy := currentSettings.EnableProxy
	proxyURL := currentSettings.ProxyURL
	settingsMutex.RUnlock()

	config := torrent.NewDefaultClientConfig()
	config.DefaultStorage = newBudgetTorrentStorage("./torrent-data", activeHLS)
	port := getAvailablePort()
	config.ListenPort = port

	if enableProxy {
		log.Println("Creating torrent client with proxy...")
		_, err := createProxyDialer(proxyURL)
		if err != nil {
			releasePort(port)
			return nil, port, errors.New("invalid SOCKS5 proxy configuration")
		}

		config.HTTPProxy = func(*http.Request) (*url.URL, error) {
			return url.Parse(proxyURL)
		}

		client, err := torrent.NewClient(config)
		if err != nil {
			releasePort(port)
			return nil, port, err
		}

		// This config forwards supported HTTP requests. SOCKS5 peer/tracker
		// isolation has not been established; do not claim VPN-grade egress.
		log.Println("SOCKS5 configured for supported HTTP traffic; torrent peer isolation is not certified")

		return client, port, nil
	}

	log.Println("Creating torrent client without proxy...")
	client, err := torrent.NewClient(config)
	if err != nil {
		releasePort(port)
		return nil, port, err
	}
	return client, port, nil
}

// Override system settings with our proxy
func init() {

	// check if settings.json exists
	if _, err := os.Stat("config/settings.json"); os.IsNotExist(err) {
		log.Println("settings.json not found, creating default settings")
		defaultSettings := Settings{
			EnableProxy:         false,
			ProxyURL:            "",
			EnableProwlarr:      false,
			ProwlarrHost:        "",
			ProwlarrApiKey:      "",
			EnableJackett:       false,
			StartupCachePercent: startupCachePercentPtr(defaultStartupCachePercent),
			JackettHost:         "",
			JackettApiKey:       "",
		}
		// Create the config directory if it doesn't exist
		if err := os.MkdirAll("config", 0700); err != nil {
			log.Fatalf("Failed to create config directory: %v", err)
		}
		settingsFile, err := os.OpenFile("config/settings.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			log.Fatalf("Failed to create settings.json: %v", err)
		}
		defer settingsFile.Close()
		encoder := json.NewEncoder(settingsFile)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(defaultSettings); err != nil {
			log.Fatalf("Failed to encode default settings: %v", err)
		}
		log.Println("Default settings created in settings.json")
	}

	// Load settings from settings.json
	settingsFile, err := os.Open("config/settings.json")
	if err != nil {
		log.Fatalf("Failed to open settings.json: %v", err)
	}
	defer settingsFile.Close()

	var s Settings
	if err := json.NewDecoder(settingsFile).Decode(&s); err != nil {
		log.Fatalf("Failed to decode settings.json: %v", err)
	}

	settingsMutex.Lock()
	currentSettings = s
	settingsMutex.Unlock()
}

func main() {
	log.SetOutput(io.MultiWriter(os.Stderr, logBuffer))

	checkFFmpeg()
	activeHLS.ffmpeg = ffmpegPath
	// Own the cache for the entire process lifetime. Without this lock a
	// second instance could delete an active HLS generation at startup.
	ownership, err := acquireHLSCacheOwnership(activeHLS.root)
	if err != nil {
		log.Fatalf("HLS cache ownership: %v", err)
	}
	defer ownership.Close()
	// No playback worker can exist before serving requests. Purge only
	// verified, internally named HLS outputs from an interrupted prior run.
	if err := activeHLS.recoverAbandonedCache(); err != nil {
		log.Fatalf("HLS startup recovery: %v", err)
	}

	// Set up endpoint handlers
	http.HandleFunc("/api/v1/torrent/add", addTorrentHandler)
	http.HandleFunc("/api/v1/torrent/", torrentHandler)
	http.HandleFunc("/api/v1/settings", settingsHandler)
	http.HandleFunc("/api/v1/settings/playback", playbackSettingsHandler)
	http.HandleFunc("/api/v1/settings/proxy", saveProxySettingsHandler)
	http.HandleFunc("/api/v1/settings/prowlarr", saveProwlarrSettingsHandler)
	http.HandleFunc("/api/v1/settings/jackett", saveJackettSettingsHandler)
	http.HandleFunc("/api/v1/prowlarr/search", searchFromProwlarr)
	http.HandleFunc("/api/v1/jackett/search", searchFromJackett)
	http.HandleFunc("/api/v1/prowlarr/test", testProwlarrConnection)
	http.HandleFunc("/api/v1/jackett/test", testJackettConnection)
	http.HandleFunc("/api/v1/proxy/test", testProxyConnection)
	http.HandleFunc("/api/v1/torrent/convert", convertTorrentToMagnetHandler)
	http.HandleFunc("/api/v1/logs", logsHandler)
	http.HandleFunc("/api/v1/sessions", sessionsHandler)
	http.HandleFunc("/api/v1/cache/purge", purgeCacheHandler)
	http.HandleFunc("/api/v1/capabilities", capabilitiesHandler)
	http.HandleFunc("/healthz", healthzHandler)
	http.HandleFunc("/api/v1/hls/", activeHLS.handler)

	// Set up client file serving
	// no-cache: revalidate on every load so updates aren't stuck behind stale assets
	fileServer := http.FileServer(http.Dir("./client"))
	noCacheFiles := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
	http.Handle("/", noCacheFiles)
	http.Handle("/client/", http.StripPrefix("/client/", noCacheFiles))
	http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./client/favicon.ico")
	})

	host := os.Getenv("BITPLAY_LISTEN_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	if err := validateListenHost(host, os.Getenv("BITPLAY_AUTH_USERNAME"), os.Getenv("BITPLAY_AUTH_PASSWORD"), os.Getenv("BITPLAY_ALLOW_UNAUTHENTICATED_LAN") == "1"); err != nil {
		log.Fatal(err)
	}
	addr := net.JoinHostPort(host, strconv.Itoa(serverPort))
	log.Printf("Attempting to start server on %s", addr)
	if os.Getenv("BITPLAY_ALLOW_UNAUTHENTICATED_LAN") == "1" {
		log.Println("WARNING: unauthenticated LAN access explicitly permitted; do not expose to the internet")
	}

	// Bind synchronously so startup failures are not mistaken for a live server.
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("Failed to bind %s: %v", addr, err)
		return
	}
	server := &http.Server{
		Handler:           basicAuthMiddleware(http.DefaultServeMux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go cleanupSessions(ctx)
	go activeHLS.sweepLoop(ctx)
	log.Printf("BytePlay listening at %s", listener.Addr())
	if err := serveWithShutdown(ctx, server, listener, 15*time.Second); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
	activeHLS.stopAll(10 * time.Second)
	// Stop admitting work, then close all sessions that have drained. If any
	// reader survived forced connection shutdown, retain its cache rather than
	// deleting files under it.
	if retired, ok := drainIdleSessionRegistry(); ok {
		for _, session := range retired {
			closeTorrentClient(session)
		}
		finishSessionPurge()
	} else {
		log.Println("Some torrent operations are still active; cache preserved during shutdown")
	}
}

// Handler to add a torrent using a magnet link
func addTorrentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var request struct{ Magnet string }
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			respondWithJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "Torrent input exceeds 16 KB"})
			return
		}
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}

	magnet := strings.TrimSpace(html.UnescapeString(request.Magnet))
	if magnet == "" {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "No magnet link provided"})
		return
	}

	// Only enabled, configured indexer origins can supply torrent metadata.
	if strings.HasPrefix(magnet, "http://") || strings.HasPrefix(magnet, "https://") {
		settingsMutex.RLock()
		snapshot := currentSettings
		settingsMutex.RUnlock()
		resolved, err := fetchIndexerMagnet(r.Context(), magnet, snapshot, createSelectiveProxyClient())
		if err != nil {
			respondWithJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "Torrent URL is not an allowed or reachable indexer download"})
			return
		}
		magnet = resolved
	}
	normalized, err := normalizeMagnetInput(magnet)
	if err != nil {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid magnet link or infohash"})
		return
	}
	magnet = normalized

	parsed, err := metainfo.ParseMagnetUri(magnet)
	if err != nil {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid magnet link"})
		return
	}
	sessionID := parsed.InfoHash.HexString()
	_, err = getOrCreateTorrentSession(r.Context(), sessionID, func(ctx context.Context) (*TorrentSession, error) {
		client, port, err := initTorrentWithProxy()
		if err != nil {
			return nil, &sessionAddError{http.StatusInternalServerError, "Failed to initialize torrent client"}
		}
		defer func() {
			if client != nil {
				releasePort(port)
				client.Close()
			}
		}()
		t, err := client.AddMagnet(magnet)
		if err != nil {
			return nil, &sessionAddError{http.StatusBadRequest, "Invalid magnet link"}
		}
		timer := time.NewTimer(3 * time.Minute)
		defer timer.Stop()
		select {
		case <-t.GotInfo():
		case <-ctx.Done():
			return nil, &sessionAddError{http.StatusRequestTimeout, "Torrent request cancelled"}
		case <-timer.C:
			return nil, &sessionAddError{http.StatusGatewayTimeout, "Timeout fetching torrent metadata"}
		}
		created := &TorrentSession{
			Client:     client,
			Torrent:    t,
			Port:       port,
			LastUsed:   time.Now(),
			ProbeCache: map[int]*ProbeResult{},
		}
		client = nil // the registry now owns this client
		return created, nil
	})
	if err != nil {
		var addErr *sessionAddError
		if errors.As(err, &addErr) {
			respondWithJSON(w, addErr.status, map[string]string{"error": addErr.message})
		} else {
			respondWithJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Torrent session unavailable"})
		}
		return
	}
	respondWithJSON(w, http.StatusOK, map[string]string{"sessionId": sessionID})
}

// Torrent handler to serve torrent files and stream content
func torrentHandler(w http.ResponseWriter, r *http.Request) {

	// Extract sessionId and possibly fileIndex from the URL
	parts := strings.Split(r.URL.Path, "/")

	// The URL structure is /api/v1/torrent/[sessionId]/...
	if len(parts) < 5 { // Changed from 4 to 5
		log.Printf("Invalid path: not enough parts")
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid path"})
		return
	}

	// The session ID is at position 4, not 3 (because array is 0-indexed and path starts with /)
	sessionID := parts[4] // Changed from parts[3] to parts[4]

	// Acquire and retain atomically with cleanup; a live request cannot be reaped.
	session, ok := retainTorrentSession(sessionID, true)
	if !ok {
		respondWithJSON(w, http.StatusNotFound, map[string]string{"error": "Session not found"})
		return
	}
	defer releaseTorrentSession(session)
	ownedCtx, stopOwned := sessionRequestContext(session, r.Context())
	defer stopOwned()
	r = r.WithContext(ownedCtx)

	// Files() panics if metadata never arrived
	if session.Torrent.Info() == nil {
		respondWithJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Torrent metadata not available yet",
		})
		return
	}

	if len(parts) > 6 && parts[5] == "probe" {
		handleProbe(w, r, session, sessionID, parts[6])
		return
	}
	if len(parts) > 6 && parts[5] == "remux" {
		handleRemux(w, r, session, sessionID, parts[6])
		return
	}

	// If there's a streaming request, handle it
	if len(parts) > 5 && parts[5] == "stream" { // Changed from parts[4] to parts[5]
		if len(parts) < 7 { // Changed from 6 to 7
			http.Error(w, "Invalid stream path", http.StatusBadRequest)
			return
		}

		fileIndexString := parts[6]
		// remove .vtt from fileIndex if it exists
		fileIndexString = strings.TrimSuffix(fileIndexString, ".vtt")

		fileIndex, err := strconv.Atoi(fileIndexString)

		if err != nil {
			http.Error(w, "Invalid file index", http.StatusBadRequest)
			return
		}

		if fileIndex < 0 || fileIndex >= len(session.Torrent.Files()) {
			http.Error(w, "File index out of range", http.StatusBadRequest)
			return
		}

		file := session.Torrent.Files()[fileIndex]

		// Set appropriate Content-Type based on file extension
		fileName := file.DisplayPath()
		extension := strings.ToLower(filepath.Ext(fileName))

		log.Printf("Streaming file: %s (type: %s)", fileName, extension)

		// video.js switches media requests to CORS mode when text tracks are attached
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Accept-Ranges, Content-Length")

		switch extension {
		case ".mp4":
			w.Header().Set("Content-Type", "video/mp4")
		case ".webm":
			w.Header().Set("Content-Type", "video/webm")
		case ".mkv":
			w.Header().Set("Content-Type", "video/x-matroska")
		case ".avi":
			w.Header().Set("Content-Type", "video/x-msvideo")
		case ".srt":
			// For SRT, convert to VTT on-the-fly if requested as VTT
			if r.URL.Query().Get("format") == "vtt" {
				w.Header().Set("Content-Type", "text/vtt")

				// Read the SRT file with size limit
				reader := file.NewReader()
				reader.SetContext(r.Context())
				defer reader.Close()
				// Wrap with limiting reader to prevent memory issues (10MB max)
				limitReader := io.LimitReader(reader, 10*1024*1024) // 10MB limit for subtitles
				srtBytes, err := io.ReadAll(limitReader)
				if err != nil {
					http.Error(w, "Failed to read subtitle file", http.StatusInternalServerError)
					return
				}

				// Convert from SRT to VTT
				vttBytes := convertSRTtoVTT(srtBytes)
				w.Write(vttBytes)
				return
			} else {
				w.Header().Set("Content-Type", "text/plain")
			}
		case ".vtt":
			w.Header().Set("Content-Type", "text/vtt")
		case ".sub":
			w.Header().Set("Content-Type", "text/plain")
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
		}

		// Read only the selected file's needed pieces with 16 MiB readahead.
		source, err := newMediaSource(session.Torrent, fileIndex)
		if err != nil {
			http.Error(w, "Invalid file index", http.StatusBadRequest)
			return
		}
		reader := source.Open(r.Context())
		// ServeContent will close the reader when done but we need to
		// ensure it gets closed if there's a panic or other error
		defer reader.Close()
		http.ServeContent(w, r, fileName, time.Time{}, reader)
		progress := reader.Progress()
		if progress.FirstByteLatencyMillis != nil {
			log.Printf("Media source file_index=%d first_byte_ms=%d bytes_served=%d selected_completed=%d torrent_completed=%d",
				fileIndex, *progress.FirstByteLatencyMillis, progress.BytesServed, progress.FileBytesCompleted, progress.TorrentBytesCompleted)
		}
		return
	}

	// If we get here, just return file list
	var files []map[string]interface{}
	for i, file := range session.Torrent.Files() {
		files = append(files, map[string]interface{}{
			"index":          i,
			"name":           file.DisplayPath(),
			"size":           file.Length(),
			"bytesCompleted": file.BytesCompleted(),
		})
	}

	respondWithJSON(w, http.StatusOK, files)
}

// Add a function to convert SRT to VTT format
func convertSRTtoVTT(srtBytes []byte) []byte {
	// a BOM after the WEBVTT header breaks cue parsing
	srtBytes = bytes.TrimPrefix(srtBytes, []byte{0xEF, 0xBB, 0xBF})
	srtContent := strings.ReplaceAll(string(srtBytes), "\r\n", "\n")

	var vtt strings.Builder
	vtt.WriteString("WEBVTT\n\n")

	for _, block := range strings.Split(srtContent, "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 || lines[0] == "" {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(lines[0])); err == nil {
			lines = lines[1:]
		}
		if len(lines) == 0 || !strings.Contains(lines[0], "-->") {
			continue
		}
		// SRT: 00:00:20,000 --> 00:00:24,400
		// VTT: 00:00:20.000 --> 00:00:24.400
		vtt.WriteString(strings.ReplaceAll(lines[0], ",", "."))
		vtt.WriteString("\n")
		for _, textLine := range lines[1:] {
			vtt.WriteString(textLine)
			vtt.WriteString("\n")
		}
		vtt.WriteString("\n")
	}

	return []byte(vtt.String())
}

// Helper function to respond with JSON
func respondWithJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// Retire only sessions that have been idle for the complete TTL and have no
// retained requests. The registry lock prevents a concurrent retain/close race.
func cleanupSessions(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reapInactiveSessions(time.Now(), 15*time.Minute)
			runtime.GC()
		}
	}
}

// reads block until the header pieces arrive, hence the generous timeout
func runFFprobe(ctx context.Context, streamURL string) (*ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	args := []string{"-v", "error", "-of", "json", "-show_format", "-show_streams",
		"-probesize", "16M", "-analyzeduration", "20M"}
	if loopbackInternalToken != "" {
		args = append(args, "-headers", "X-BytePlay-Internal: "+loopbackInternalToken+"\r\n")
	}
	args = append(args, streamURL)

	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	var stdout bytes.Buffer
	stderr := &limitedWriter{max: 4096}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ffprobe: %v (%s)", err, strings.TrimSpace(stderr.buf.String()))
	}

	var raw struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Index        int               `json:"index"`
			CodecType    string            `json:"codec_type"`
			CodecName    string            `json:"codec_name"`
			Profile      string            `json:"profile"`
			PixelFormat  string            `json:"pix_fmt"`
			Width        int               `json:"width"`
			Height       int               `json:"height"`
			AvgFrameRate string            `json:"avg_frame_rate"`
			Channels     int               `json:"channels"`
			Tags         map[string]string `json:"tags"`
			Disposition  struct {
				Default int `json:"default"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return nil, fmt.Errorf("ffprobe output parse: %v", err)
	}

	result := &ProbeResult{Container: raw.Format.FormatName}
	result.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	for _, s := range raw.Streams {
		ps := ProbeStream{
			Index:       s.Index,
			Type:        s.CodecType,
			Codec:       s.CodecName,
			Profile:     s.Profile,
			PixelFormat: s.PixelFormat,
			Width:       s.Width,
			Height:      s.Height,
			FrameRate:   parseMediaFrameRate(s.AvgFrameRate),
			Channels:    s.Channels,
			Default:     s.Disposition.Default == 1,
		}
		if s.Tags != nil {
			ps.Language = s.Tags["language"]
			ps.Title = s.Tags["title"]
		}
		result.Streams = append(result.Streams, ps)
	}
	return result, nil
}

func handleProbe(w http.ResponseWriter, r *http.Request, session *TorrentSession, sessionID, indexStr string) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if !ffmpegAvailable {
		respondWithJSON(w, http.StatusNotImplemented, map[string]string{"error": "ffmpeg is not installed on the server"})
		return
	}

	fileIndex, err := strconv.Atoi(indexStr)
	if err != nil || fileIndex < 0 || fileIndex >= len(session.Torrent.Files()) {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid file index"})
		return
	}

	session.ProbeMu.Lock()
	cached := session.ProbeCache[fileIndex]
	session.ProbeMu.Unlock()
	if cached != nil {
		respondWithJSON(w, http.StatusOK, cached)
		return
	}

	result, err := runFFprobe(r.Context(), loopbackStreamURL(sessionID, fileIndex))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			respondWithJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "Timed out reading media metadata - the torrent may still be fetching its first pieces, retry shortly"})
			return
		}
		log.Printf("Probe failed for session %s file %d: %v", sessionID, fileIndex, err)
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to analyze media file"})
		return
	}

	session.ProbeMu.Lock()
	if session.ProbeCache == nil {
		session.ProbeCache = map[int]*ProbeResult{}
	}
	session.ProbeCache[fileIndex] = result
	session.ProbeMu.Unlock()

	respondWithJSON(w, http.StatusOK, result)
}

// Stream-copy remux into progressive fragmented MP4. ?audio=N picks an
// audio track, ?t=S restarts from an offset.
func handleRemux(w http.ResponseWriter, r *http.Request, session *TorrentSession, sessionID, indexStr string) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if !ffmpegAvailable {
		respondWithJSON(w, http.StatusNotImplemented, map[string]string{"error": "ffmpeg is not installed on the server"})
		return
	}

	fileIndex, err := strconv.Atoi(indexStr)
	if err != nil || fileIndex < 0 || fileIndex >= len(session.Torrent.Files()) {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid file index"})
		return
	}

	// params end up in the ffmpeg argv, numeric only
	audioStream := -1
	if a := r.URL.Query().Get("audio"); a != "" {
		audioStream, err = strconv.Atoi(a)
		if err != nil || audioStream < 0 || audioStream > 512 {
			respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid audio stream index"})
			return
		}
	}
	startSeconds := 0.0
	if t := r.URL.Query().Get("t"); t != "" {
		startSeconds, err = strconv.ParseFloat(t, 64)
		if err != nil || startSeconds < 0 || startSeconds > 360000 {
			respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid start time"})
			return
		}
	}

	// Legacy remux and HLS share the bounded FFmpeg process budget.
	releaseFFmpeg, admitted := reserveLegacyRemuxSlot(remuxSlots, activeHLS.processSlots)
	if !admitted {
		respondWithJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Too many active streams - try again shortly"})
		return
	}
	defer releaseFFmpeg()

	videoCodec := ""
	session.ProbeMu.Lock()
	if probe := session.ProbeCache[fileIndex]; probe != nil {
		for _, s := range probe.Streams {
			if s.Type == "video" {
				videoCodec = s.Codec
				break
			}
		}
	}
	session.ProbeMu.Unlock()

	args := []string{"-v", "error"}
	if loopbackInternalToken != "" {
		args = append(args, "-headers", "X-BytePlay-Internal: "+loopbackInternalToken+"\r\n")
	}
	if startSeconds > 0 {
		args = append(args, "-ss", strconv.FormatFloat(startSeconds, 'f', 3, 64))
	}
	args = append(args, "-i", loopbackStreamURL(sessionID, fileIndex), "-map", "0:v:0")
	if audioStream >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:%d", audioStream))
	} else {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args, "-c", "copy", "-sn")
	if videoCodec == "hevc" {
		args = append(args, "-tag:v", "hvc1") // Safari rejects the default hev1 tag
	}
	args = append(args,
		"-avoid_negative_ts", "make_zero",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", "pipe:1",
	)

	// client disconnect cancels the context and kills ffmpeg
	cmd := exec.CommandContext(r.Context(), ffmpegPath, args...)
	cmd.WaitDelay = 3 * time.Second
	stderr := &limitedWriter{max: 4096}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to start remux"})
		return
	}
	if err := cmd.Start(); err != nil {
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to start ffmpeg"})
		return
	}
	log.Printf("Remux started for session %s file %d (audio=%d t=%.1f)", sessionID, fileIndex, audioStream, startSeconds)

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Cache-Control", "no-store")
	io.Copy(w, stdout)

	if err := cmd.Wait(); err != nil && r.Context().Err() == nil {
		log.Printf("ffmpeg remux for session %s file %d exited: %v (%s)", sessionID, fileIndex, err, strings.TrimSpace(stderr.buf.String()))
	}
}

func logsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	respondWithJSON(w, http.StatusOK, map[string]interface{}{"lines": logBuffer.Lines()})
}

func sessionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	list := []map[string]interface{}{}
	sessions.Range(func(key, value interface{}) bool {
		id := key.(string)
		session, ok := retainTorrentSession(id, false)
		if !ok {
			return true
		}
		entry := map[string]interface{}{
			"id": id, "name": "(fetching metadata)",
			"size": int64(0), "complete": int64(0), "peers": 0,
		}
		if t := session.Torrent; t != nil && t.Info() != nil {
			stats := t.Stats()
			entry["name"] = t.Name()
			entry["size"] = t.Length()
			entry["complete"] = t.BytesCompleted()
			entry["peers"] = stats.ActivePeers
		}
		releaseTorrentSession(session)
		list = append(list, entry)
		return true
	})
	respondWithJSON(w, http.StatusOK, list)
}

func dirSize(path string) int64 {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// stops every session and wipes torrent-data; nothing else ever deletes it
func purgeCacheHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Cancel owned media operations and wait for every retained reader.
	// Never delete cached files beneath a live stream or FFmpeg child.
	retired, drained, ok := beginCancellingSessionPurge()
	if !ok {
		respondWithJSON(w, http.StatusConflict, map[string]string{"error": "Torrent metadata fetch or another purge is active"})
		return
	}
	finishOnReturn := true
	defer func() {
		if finishOnReturn {
			finishSessionPurge()
		}
	}()
	waitCtx, cancelWait := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancelWait()
	if err := waitForDrainedSessions(waitCtx, drained); err != nil {
		// Preserve cache. Hold the purge gate until all remaining readers
		// release and the associated torrent clients can close safely.
		finishOnReturn = false
		go func() {
			_ = waitForDrainedSessions(context.Background(), drained)
			for _, session := range retired {
				closeTorrentClient(session)
			}
			finishSessionPurge()
		}()
		respondWithJSON(w, http.StatusConflict, map[string]string{"error": "Torrent sessions still draining; cache preserved"})
		return
	}
	for _, session := range retired {
		closeTorrentClient(session)
	}

	const dataDir = "./torrent-data"
	freed := dirSize(dataDir)

	entries, err := os.ReadDir(dataDir)
	if err != nil && !os.IsNotExist(err) {
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to read cache directory: " + err.Error()})
		return
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dataDir, entry.Name())); err != nil {
			respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to delete " + entry.Name() + ": " + err.Error()})
			return
		}
	}
	// Every torrent writer was drained by the purge gate. Discard projected
	// mmap file lengths only after all persistent data was removed.
	activeHLS.resetTorrentProjections()

	log.Printf("Cache purged, freed %s", formatSize(float64(freed)))
	respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"message":    "Cache purged",
		"freedBytes": freed,
		"freed":      formatSize(float64(freed)),
	})
}

// Test the proxy connection
func testProwlarrConnection(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// Handle preflight requests
	if r.Method == "OPTIONS" {
		return
	}

	var settings ProwlarrSettings
	if !decodeLimitedSettingsBody(w, r, &settings) {
		return
	}

	prowlarrHost := settings.ProwlarrHost
	prowlarrApiKey := settings.ProwlarrApiKey

	// The UI never sees stored keys, so an empty key means "test with the stored one"
	if prowlarrApiKey == "" {
		settingsMutex.RLock()
		prowlarrApiKey = currentSettings.ProwlarrApiKey
		settingsMutex.RUnlock()
	}

	if prowlarrHost == "" || prowlarrApiKey == "" {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Prowlarr host or API key not set"})
		return
	}

	client := createSelectiveProxyClient()
	testURL := fmt.Sprintf("%s/api/v1/system/status", prowlarrHost)

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to prepare Prowlarr test request"})
		return
	}

	req.Header.Set("X-Api-Key", prowlarrApiKey)
	resp, err := client.Do(req)
	if err != nil {
		log.Println("Prowlarr connection test failed")
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to connect to Prowlarr"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respondWithJSON(w, resp.StatusCode, map[string]string{"error": fmt.Sprintf("Prowlarr returned status %d", resp.StatusCode)})
		return
	}

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(responseBody) > 1<<20 {
		respondWithJSON(w, http.StatusBadGateway, map[string]string{"error": "Prowlarr status response unavailable or too large"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(responseBody)
}

// Search from Prowlarr
func searchFromProwlarr(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Prowlarr-Host, X-Api-Key")

	// Handle preflight requests
	if r.Method == "OPTIONS" {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "No search query provided"})
		return
	}

	// search movies in prowlarr
	settingsMutex.RLock()
	prowlarrHost := currentSettings.ProwlarrHost
	prowlarrApiKey := currentSettings.ProwlarrApiKey
	settingsMutex.RUnlock()

	if prowlarrHost == "" || prowlarrApiKey == "" {
		http.Error(w, "Prowlarr host or API key not set", http.StatusBadRequest)
		return
	}

	// Use the client that bypasses proxy for Prowlarr
	client := createSelectiveProxyClient()

	// Prowlarr search endpoint - looking for movie torrents
	searchURL := fmt.Sprintf("%s/api/v1/search?query=%s&limit=10", prowlarrHost, url.QueryEscape(query))

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		log.Printf("Error creating request: %v", err)
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	req.Header.Set("X-Api-Key", prowlarrApiKey)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error making request to Prowlarr: %v", err)
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to connect to Prowlarr: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading response: %v", err)
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to read Prowlarr response"})
		return
	}

	if resp.StatusCode != http.StatusOK {
		respondWithJSON(w, resp.StatusCode, map[string]string{"error": fmt.Sprintf("Prowlarr returned status %d: %s", resp.StatusCode, string(body))})
		return
	}

	// Parse the JSON response and process the results
	var results []map[string]interface{}
	if err := json.Unmarshal(body, &results); err != nil {
		log.Printf("Error parsing JSON: %v", err)
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to parse Prowlarr response"})
		return
	}

	// Process the results to make them more usable by the frontend
	var processedResults []map[string]interface{}
	for _, result := range results {
		// Get title and download URL
		title, hasTitle := result["title"].(string)
		downloadUrl, hasDownloadUrl := result["downloadUrl"].(string)

		// Magnet URL might be present in some results
		magnetUrl, hasMagnet := result["magnetUrl"].(string)

		if !hasTitle || title == "" {
			// Skip results without titles
			continue
		}

		// We need at least one of download URL or magnet URL
		if (!hasDownloadUrl || downloadUrl == "") && (!hasMagnet || magnetUrl == "") {
			continue
		}

		// Create a simplified result object with just what we need
		processedResult := map[string]interface{}{
			"title": title,
		}

		// Prefer magnet URLs if available directly
		if hasMagnet && magnetUrl != "" {
			processedResult["magnetUrl"] = magnetUrl
			processedResult["directMagnet"] = true
		} else if hasDownloadUrl && downloadUrl != "" {
			processedResult["downloadUrl"] = downloadUrl
			processedResult["directMagnet"] = false
		}

		// Include optional fields if they exist
		if size, ok := result["size"].(float64); ok {
			processedResult["size"] = formatSize(size)
		}

		if seeders, ok := result["seeders"].(float64); ok {
			processedResult["seeders"] = seeders
		}

		if leechers, ok := result["leechers"].(float64); ok {
			processedResult["leechers"] = leechers
		}

		if indexer, ok := result["indexer"].(string); ok {
			processedResult["indexer"] = indexer
		}

		if publishDate, ok := result["publishDate"].(string); ok {
			processedResult["publishDate"] = publishDate
		}

		if category, ok := result["category"].(string); ok {
			processedResult["category"] = category
		}

		processedResults = append(processedResults, processedResult)
	}

	respondWithJSON(w, http.StatusOK, processedResults)
}

// Test Jackett Connection Handler
func testJackettConnection(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	// Handle preflight requests
	if r.Method == "OPTIONS" {
		return
	}

	var settings JackettSettings
	if !decodeLimitedSettingsBody(w, r, &settings) {
		return
	}

	jackettHost := settings.JackettHost
	jackettApiKey := settings.JackettApiKey

	// The UI never sees stored keys, so an empty key means "test with the stored one"
	if jackettApiKey == "" {
		settingsMutex.RLock()
		jackettApiKey = currentSettings.JackettApiKey
		settingsMutex.RUnlock()
	}

	if jackettHost == "" || jackettApiKey == "" {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Jackett host or API key not set"})
		return
	}

	client := createSelectiveProxyClient()
	testURL := fmt.Sprintf("%s/api/v2.0/indexers/all/results?apikey=%s", jackettHost, jackettApiKey)
	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		log.Printf("Error creating request: %v", err)
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Println("Jackett connection test failed")
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to connect to Jackett"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respondWithJSON(w, resp.StatusCode, map[string]string{"error": fmt.Sprintf("Jackett returned status %d", resp.StatusCode)})
		return
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading response: %v", err)
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to read Jackett response"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(responseBody)
}

// Search from Jackett
func searchFromJackett(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// Handle preflight requests
	if r.Method == "OPTIONS" {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "No search query provided"})
		return
	}

	// search movies in jackett
	settingsMutex.RLock()
	jackettHost := currentSettings.JackettHost
	jackettApiKey := currentSettings.JackettApiKey
	settingsMutex.RUnlock()

	if jackettHost == "" || jackettApiKey == "" {
		http.Error(w, "Jackett host or API key not set", http.StatusBadRequest)
		return
	}

	// Use the client that bypasses proxy for Jackett
	client := createSelectiveProxyClient()

	// Jackett search endpoint - looking for movie torrents
	searchURL := fmt.Sprintf("%s/api/v2.0/indexers/all/results?Query=%s&apikey=%s", jackettHost, url.QueryEscape(query), jackettApiKey)

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		log.Printf("Error creating request: %v", err)
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error making request to Jackett: %v", err)
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to connect to Jackett: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading response: %v", err)
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to read Jackett response"})
		return
	}

	if resp.StatusCode != http.StatusOK {
		respondWithJSON(w, resp.StatusCode, map[string]string{"error": fmt.Sprintf("Jackett returned status %d: %s", resp.StatusCode, string(body))})
		return
	}

	var jacketResponse struct {
		Results []map[string]interface{} `json:"Results"`
	}

	// Parse the JSON response and process the results
	if err := json.Unmarshal(body, &jacketResponse); err != nil {
		log.Printf("Error parsing JSON: %v", err)
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to parse Jackett response"})
		return
	}

	// Process the results to make them more usable by the frontend
	var processedResults []map[string]interface{}
	for _, result := range jacketResponse.Results {
		// Get title and download URL
		title, hasTitle := result["Title"].(string)
		downloadUrl, hasDownloadUrl := result["Link"].(string)

		// Magnet URL might be present in some results
		magnetUrl, hasMagnet := result["MagnetUri"].(string)

		if !hasTitle || title == "" {
			// Skip results without titles
			continue
		}

		// We need at least one of download URL or magnet URL
		if (!hasDownloadUrl || downloadUrl == "") && (!hasMagnet || magnetUrl == "") {
			continue
		}

		// Create a simplified result object with just what we need
		processedResult := map[string]interface{}{
			"title": title,
		}

		// Prefer magnet URLs if available directly
		if hasMagnet && magnetUrl != "" && strings.HasPrefix(magnetUrl, "magnet:") {
			processedResult["magnetUrl"] = magnetUrl
			processedResult["directMagnet"] = true
		} else if hasDownloadUrl && downloadUrl != "" {
			processedResult["downloadUrl"] = downloadUrl
			processedResult["directMagnet"] = false
		}

		// Include optional fields if they exist
		if size, ok := result["Size"].(float64); ok {
			processedResult["size"] = formatSize(size)
		}

		if seeders, ok := result["Seeders"].(float64); ok {
			processedResult["seeders"] = seeders
		}

		if leechers, ok := result["Peers"].(float64); ok {
			processedResult["leechers"] = leechers
		}

		if indexer, ok := result["Tracker"].(string); ok {
			processedResult["indexer"] = indexer
		}

		if publishDate, ok := result["PublishDate"].(string); ok {
			processedResult["publishDate"] = publishDate
		}

		if category, ok := result["category"].(string); ok {
			processedResult["category"] = category
		}

		processedResults = append(processedResults, processedResult)
	}

	respondWithJSON(w, http.StatusOK, processedResults)
}

// Test Proxy Connection Handler
func testProxyConnection(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// Handle preflight requests
	if r.Method == "OPTIONS" {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var settings ProxySettings
	if !decodeLimitedSettingsBody(w, r, &settings) {
		return
	}

	proxyURL := settings.ProxyURL

	if proxyURL == "" {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Proxy URL not set"})
		return
	}

	// Proxy credentials are never reflected into diagnostics.
	parsedProxyURL, err := url.Parse(proxyURL)
	if err != nil || parsedProxyURL == nil {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid SOCKS5 proxy configuration"})
		return
	}
	if _, err := createProxyDialer(proxyURL); err != nil {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid SOCKS5 proxy configuration"})
		return
	}

	// Create a transport that uses the proxy
	transport := &http.Transport{
		Proxy: http.ProxyURL(parsedProxyURL),
	}

	// Create client with custom transport and timeout
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second, // Adjust timeout as needed
	}

	testURL := "https://httpbin.org/ip"
	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to prepare proxy test request"})
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Println("Proxy connection test failed")
		respondWithJSON(w, http.StatusBadGateway, map[string]string{"error": "Proxy connection test failed"})
		return
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(responseBody) > 65536 {
		respondWithJSON(w, http.StatusBadGateway, map[string]string{"error": "Proxy response unavailable or too large"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(responseBody)
}

// Helper function to save settings to file (assumes mutex is already locked)
var settingsWriteMutex sync.Mutex

func saveSettingsToFile() error {
	settingsWriteMutex.Lock()
	defer settingsWriteMutex.Unlock()
	if err := os.MkdirAll("config", 0700); err != nil {
		return err
	}
	settingsMutex.RLock()
	snapshot := currentSettings
	settingsMutex.RUnlock()
	file, err := os.CreateTemp("config", ".settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), "config/settings.json")
}

// Proxy Settings Save Handler
func saveProxySettingsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var newSettings ProxySettings
	if !decodeLimitedSettingsBody(w, r, &newSettings) {
		return
	}

	settingsMutex.Lock()
	currentSettings.EnableProxy = newSettings.EnableProxy
	currentSettings.ProxyURL = newSettings.ProxyURL
	settingsMutex.Unlock()

	if err := saveSettingsToFile(); err != nil {
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to save settings: " + err.Error()})
		return
	}
	println("Proxy settings saved successfully")

	respondWithJSON(w, http.StatusOK, map[string]string{"message": "Proxy settings saved successfully"})
}

// Prowlarr Settings Save Handler
func saveProwlarrSettingsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var newSettings ProwlarrSettings
	if !decodeLimitedSettingsBody(w, r, &newSettings) {
		return
	}

	settingsMutex.Lock()
	currentSettings.EnableProwlarr = newSettings.EnableProwlarr
	currentSettings.ProwlarrHost = newSettings.ProwlarrHost
	// Empty key means "keep the stored one" (the UI never sees the real key)
	if newSettings.ProwlarrApiKey != "" {
		currentSettings.ProwlarrApiKey = newSettings.ProwlarrApiKey
	}
	currentSettings.SearchTimeoutSeconds = newSettings.SearchTimeoutSeconds
	currentSettings.SkipTLSVerify = newSettings.SkipTLSVerify
	settingsMutex.Unlock()

	if err := saveSettingsToFile(); err != nil {
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to save settings: " + err.Error()})
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]string{"message": "Prowlarr settings saved successfully"})
}

// Jackett Settings Save Handler
func saveJackettSettingsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var newSettings JackettSettings
	if !decodeLimitedSettingsBody(w, r, &newSettings) {
		return
	}

	settingsMutex.Lock()
	currentSettings.EnableJackett = newSettings.EnableJackett
	currentSettings.JackettHost = newSettings.JackettHost
	// Empty key means "keep the stored one" (the UI never sees the real key)
	if newSettings.JackettApiKey != "" {
		currentSettings.JackettApiKey = newSettings.JackettApiKey
	}
	currentSettings.SearchTimeoutSeconds = newSettings.SearchTimeoutSeconds
	currentSettings.SkipTLSVerify = newSettings.SkipTLSVerify
	settingsMutex.Unlock()

	if err := saveSettingsToFile(); err != nil {
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to save settings: " + err.Error()})
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]string{"message": "Jackett settings saved successfully"})
}

// Convert Torrent to Magnet Handler
func convertTorrentToMagnetHandler(w http.ResponseWriter, r *http.Request) {
	// Set CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Bound the entire multipart request, not merely memory buffering.
	const maxUploadSize = 10 << 20
	if r.ContentLength > maxUploadSize {
		respondWithJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "Torrent metadata request exceeds 10 MB"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			respondWithJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "Torrent metadata request exceeds 10 MB"})
			return
		}
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Failed to parse multipart form"})
		return
	}

	// Get the torrent file from the form data
	file, header, err := r.FormFile("torrent")
	if err != nil {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Missing torrent file"})
		return
	}
	defer file.Close()

	// Check file size
	if header.Size > maxUploadSize {
		respondWithJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "File too large"})
		return
	}

	// Read the torrent file content
	fileBytes, err := io.ReadAll(file)
	if err != nil {
		respondWithJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to read file"})
		return
	}

	// Parse torrent file
	mi, err := metainfo.Load(bytes.NewReader(fileBytes))
	if err != nil {
		respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid torrent file: " + err.Error()})
		return
	}

	// Get info hash
	infoHash := mi.HashInfoBytes().String()

	// Build magnet URL components
	magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s", infoHash)

	// Add display name
	info, err := mi.UnmarshalInfo()
	if err == nil {
		magnet += fmt.Sprintf("&dn=%s", url.QueryEscape(info.Name))
	}

	// Add trackers
	for _, tier := range mi.AnnounceList {
		for _, tracker := range tier {
			magnet += fmt.Sprintf("&tr=%s", url.QueryEscape(tracker))
		}
	}

	respondWithJSON(w, http.StatusOK, map[string]string{
		"magnet": magnet,
	})
}
