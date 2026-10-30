package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

// Local-by-default policy; external listening requires an explicit decision.
func validateListenHost(host, username, password string, allowUnauthenticatedLAN bool) error {
	if (username == "") != (password == "") {
		return errors.New("BITPLAY_AUTH_USERNAME and BITPLAY_AUTH_PASSWORD must both be configured")
	}
	if ip := net.ParseIP(host); (ip != nil && ip.IsLoopback()) || strings.EqualFold(host, "localhost") {
		return nil
	}
	if username != "" && password != "" || allowUnauthenticatedLAN {
		return nil
	}
	return errors.New("refusing non-loopback listener without authentication")
}

func redactedProxyURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", strings.Contains(raw, "@")
	}
	hasCredentials := u.User != nil
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	return u.String(), hasCredentials
}

func settingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	settingsMutex.RLock()
	snapshot := currentSettings
	settingsMutex.RUnlock()
	snapshot.StartupCachePercent = startupCachePercentPtr(configuredStartupCachePercent(snapshot))
	urlWithoutCredentials, credentialsSet := redactedProxyURL(snapshot.ProxyURL)
	out := struct {
		Settings
		ProwlarrApiKeySet   bool `json:"prowlarrApiKeySet"`
		JackettApiKeySet    bool `json:"jackettApiKeySet"`
		ProxyCredentialsSet bool `json:"proxyCredentialsSet"`
	}{
		Settings:            snapshot,
		ProwlarrApiKeySet:   snapshot.ProwlarrApiKey != "",
		JackettApiKeySet:    snapshot.JackettApiKey != "",
		ProxyCredentialsSet: credentialsSet,
	}
	out.ProwlarrApiKey = ""
	out.JackettApiKey = ""
	out.ProxyURL = urlWithoutCredentials
	respondWithJSON(w, http.StatusOK, out)
}

func normalizeMagnetInput(input string) (string, error) {
	s := strings.TrimSpace(input)
	if len(s) == 40 {
		if _, err := hex.DecodeString(s); err == nil {
			s = "magnet:?xt=urn:btih:" + strings.ToLower(s)
		}
	}
	if _, err := metainfo.ParseMagnetUri(s); err != nil {
		return "", errors.New("invalid magnet URI or v1 infohash")
	}
	return s, nil
}

func sameOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func allowedIndexerURL(raw string, s Settings) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	for _, provider := range []struct {
		enabled bool
		host    string
	}{
		{s.EnableProwlarr, s.ProwlarrHost},
		{s.EnableJackett, s.JackettHost},
	} {
		if !provider.enabled || provider.host == "" {
			continue
		}
		p, err := url.Parse(provider.host)
		if err == nil && sameOrigin(u, p) {
			return true
		}
	}
	return false
}

// Fetch a provider-origin torrent or magnet redirect without crossing origins.
func fetchIndexerMagnet(ctx context.Context, raw string, settings Settings, client *http.Client) (string, error) {
	if !allowedIndexerURL(raw, settings) {
		return "", errors.New("unapproved indexer origin")
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || !allowedIndexerURL(req.URL.String(), settings) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("indexer request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc, err := resp.Location()
		if err == nil && strings.HasPrefix(loc.String(), "magnet:") {
			return normalizeMagnetInput(loc.String())
		}
		return "", errors.New("disallowed indexer redirect")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("indexer returned status %d", resp.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, (10<<20)+1))
	if err != nil || len(payload) > 10<<20 {
		return "", errors.New("torrent metadata too large or invalid")
	}
	mi, err := metainfo.Load(bytes.NewReader(payload))
	if err != nil {
		return "", errors.New("indexer did not return torrent metadata")
	}
	magnet := "magnet:?xt=urn:btih:" + mi.HashInfoBytes().String()
	if info, err := mi.UnmarshalInfo(); err == nil {
		magnet += "&dn=" + url.QueryEscape(info.Name)
	}
	for _, tier := range mi.AnnounceList {
		for _, tracker := range tier {
			magnet += "&tr=" + url.QueryEscape(tracker)
		}
	}
	return normalizeMagnetInput(magnet)
}

// Never accept the internal media token from a non-loopback TCP peer.
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
