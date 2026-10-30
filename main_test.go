package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmptyMagnetReturnsOneJSONError(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/torrent/add", strings.NewReader(`{"magnet":""}`))
	w := httptest.NewRecorder()
	addTorrentHandler(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want 400", w.Code)
	}
	var got map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if err := dec.Decode(&got); err == nil {
		t.Fatal("more than one JSON document returned")
	}
}

func TestRawInfohashNormalization(t *testing.T) {
	input := strings.Repeat("a", 40)
	got, err := normalizeMagnetInput(input)
	if err != nil || got != "magnet:?xt=urn:btih:"+input {
		t.Fatalf("normalized=%q, err=%v", got, err)
	}
	for _, invalid := range []string{"", strings.Repeat("a", 64), "https://example.com", "magnet:"} {
		if _, err := normalizeMagnetInput(invalid); err == nil {
			t.Errorf("%q unexpectedly accepted", invalid)
		}
	}
}

func TestListenPolicy(t *testing.T) {
	tests := []struct {
		host, user, pass string
		allow, ok        bool
	}{
		{"127.0.0.1", "", "", false, true},
		{"::1", "", "", false, true},
		{"0.0.0.0", "", "", false, false},
		{"0.0.0.0", "user", "pass", false, true},
		{"0.0.0.0", "", "", true, true},
		{"127.0.0.1", "user", "", false, false},
	}
	for _, tt := range tests {
		err := validateListenHost(tt.host, tt.user, tt.pass, tt.allow)
		if (err == nil) != tt.ok {
			t.Errorf("%q %q: got %v, want allow=%v", tt.host, tt.user, err, tt.ok)
		}
	}
}

func TestRedactedSettings(t *testing.T) {
	settingsMutex.Lock()
	previous := currentSettings
	currentSettings = Settings{
		EnableProxy: true, ProxyURL: "socks5://alice:mySecret@localhost:1080",
		ProwlarrApiKey: "indexerSecret", JackettApiKey: "jackettSecret",
	}
	settingsMutex.Unlock()
	t.Cleanup(func() {
		settingsMutex.Lock()
		currentSettings = previous
		settingsMutex.Unlock()
	})
	w := httptest.NewRecorder()
	settingsHandler(w, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	got := w.Body.String()
	for _, secret := range []string{"mySecret", "indexerSecret", "jackettSecret", "alice"} {
		if strings.Contains(got, secret) {
			t.Errorf("settings leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"proxyCredentialsSet":true`) {
		t.Errorf("missing credential-presence field: %s", got)
	}
}

func TestIndexerOriginPolicy(t *testing.T) {
	s := Settings{EnableProwlarr: true, ProwlarrHost: "http://127.0.0.1:9696"}
	for _, tc := range []struct {
		raw     string
		allowed bool
	}{
		{"http://127.0.0.1:9696/api/v1/download?id=3", true},
		{"http://127.0.0.1:9697/api/v1/download", false},
		{"https://127.0.0.1:9696/api/v1/download", false},
		{"http://user:pass@127.0.0.1:9696/api", false},
		{"file:///etc/passwd", false},
		{"http://127.0.0.1.evil.test:9696/api", false},
	} {
		if got := allowedIndexerURL(tc.raw, s); got != tc.allowed {
			t.Errorf("%q: got %v want %v", tc.raw, got, tc.allowed)
		}
	}
}

func TestTorrentUploadRejectsOversizedRequest(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/torrent/convert", strings.NewReader(strings.Repeat("x", (10<<20)+500)))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=example")
	w := httptest.NewRecorder()
	convertTorrentToMagnetHandler(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got status %d, want 413; body=%s", w.Code, w.Body.String())
	}
}

func TestBasicAuthAndLoopbackMediaToken(t *testing.T) {
	t.Setenv("BITPLAY_AUTH_USERNAME", "alice")
	t.Setenv("BITPLAY_AUTH_PASSWORD", "test-pass")
	protected := basicAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	anonymous := httptest.NewRequest(http.MethodGet, "/api/v1/torrent/test", nil)
	w := httptest.NewRecorder()
	protected.ServeHTTP(w, anonymous)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", w.Code)
	}
	authenticated := httptest.NewRequest(http.MethodGet, "/api/v1/torrent/test", nil)
	authenticated.SetBasicAuth("alice", "test-pass")
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, authenticated)
	if w.Code != http.StatusNoContent {
		t.Fatalf("Basic Auth status=%d", w.Code)
	}
	remote := httptest.NewRequest(http.MethodGet, "/api/v1/torrent/test", nil)
	remote.RemoteAddr = "198.51.100.4:12345"
	remote.Header.Set("X-BytePlay-Internal", loopbackInternalToken)
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, remote)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("remote internal-token bypass: status=%d", w.Code)
	}
	loopback := httptest.NewRequest(http.MethodGet, "/api/v1/torrent/test", nil)
	loopback.RemoteAddr = "127.0.0.1:12222"
	loopback.Header.Set("X-BytePlay-Internal", loopbackInternalToken)
	w = httptest.NewRecorder()
	protected.ServeHTTP(w, loopback)
	if w.Code != http.StatusNoContent {
		t.Fatalf("loopback internal-token status=%d", w.Code)
	}
}

func TestIndexerMagnetRedirectAndCrossOriginDenial(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:" + strings.Repeat("a", 40)
	authorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, magnet, http.StatusFound)
	}))
	defer authorized.Close()
	s := Settings{EnableProwlarr: true, ProwlarrHost: authorized.URL}
	got, err := fetchIndexerMagnet(context.Background(), authorized.URL+"/get", s, authorized.Client())
	if err != nil || got != magnet {
		t.Fatalf("allowed redirect: got %q, err=%v", got, err)
	}
	called := false
	offOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer offOrigin.Close()
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, offOrigin.URL+"/private", http.StatusFound)
	}))
	defer denied.Close()
	s.ProwlarrHost = denied.URL
	_, err = fetchIndexerMagnet(context.Background(), denied.URL+"/get", s, denied.Client())
	if err == nil || called {
		t.Fatalf("cross-origin redirect allowed: err=%v, called=%v", err, called)
	}
}

func TestProxyTestErrorNeverExposesCredentials(t *testing.T) {
	for _, proxyURL := range []string{
		"socks5://alice:private-secret@127.0.0.1:1",
		"http://alice:private-secret@127.0.0.1:1",
	} {
		body, _ := json.Marshal(map[string]interface{}{"proxyUrl": proxyURL})
		request := httptest.NewRequest(http.MethodPost, "/api/v1/proxy/test", bytes.NewReader(body))
		w := httptest.NewRecorder()
		testProxyConnection(w, request)
		if strings.Contains(w.Body.String(), "private-secret") || strings.Contains(w.Body.String(), "alice") {
			t.Fatalf("proxy credentials leaked in error response: %s", w.Body.String())
		}
		if w.Code == 200 {
			t.Fatal("unreachable or unsupported proxy unexpectedly succeeded")
		}
	}
}

func TestSettingsEndpointsRejectOversizedBodies(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"proxy-test", testProxyConnection},
		{"prowlarr-test", testProwlarrConnection},
		{"jackett-test", testJackettConnection},
		{"proxy-save", saveProxySettingsHandler},
		{"prowlarr-save", saveProwlarrSettingsHandler},
		{"jackett-save", saveJackettSettingsHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.NewReader(`{"proxyUrl":"` + strings.Repeat("x", 66000) + `"}`)
			w := httptest.NewRecorder()
			tc.handler(w, httptest.NewRequest(http.MethodPost, "/api/v1/settings", body))
			if w.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("unbounded settings body returned %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHealthzRequiresMediaPrerequisites(t *testing.T) {
	oldAvailable, oldProbe := ffmpegAvailable, ffprobePath
	defer func() {
		ffmpegAvailable, ffprobePath = oldAvailable, oldProbe
	}()
	check := func(method string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		healthzHandler(w, httptest.NewRequest(method, "/healthz", nil))
		if w.Code != want {
			t.Fatalf("%s /healthz returned %d, want %d", method, w.Code, want)
		}
	}
	ffmpegAvailable, ffprobePath = false, ""
	check("GET", http.StatusServiceUnavailable)
	ffmpegAvailable, ffprobePath = true, "ffprobe"
	check("GET", http.StatusOK)
	check("POST", http.StatusMethodNotAllowed)
}
