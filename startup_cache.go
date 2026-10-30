package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// This is a selected-video byte threshold, not an HLS/torrent-wide ratio.
const defaultStartupCachePercent = 9

func startupCachePercentPtr(v int) *int { return &v }

func configuredStartupCachePercent(s Settings) int {
	if s.StartupCachePercent == nil {
		return defaultStartupCachePercent
	}
	return *s.StartupCachePercent
}
func startupCachePercent() int {
	settingsMutex.RLock()
	defer settingsMutex.RUnlock()
	return configuredStartupCachePercent(currentSettings)
}

// Splitting the product avoids overflow on very large file sizes.
func requiredStartupBytes(total int64, percent int) int64 {
	if total <= 0 || percent <= 0 {
		return 0
	}
	if percent >= 100 {
		return total
	}
	return (total/100)*int64(percent) + ((total%100)*int64(percent)+99)/100
}
func startupCacheReached(total, completed int64, percent int) bool {
	if percent <= 0 {
		return true
	}
	return total > 0 && completed >= requiredStartupBytes(total, percent)
}
func startupPieceRange(offset, total, pieceSize int64, percent int) (int, int, bool) {
	need := requiredStartupBytes(total, percent)
	if need <= 0 || pieceSize <= 0 || offset < 0 {
		return 0, 0, false
	}
	endOffset := offset + need
	if endOffset < offset {
		return 0, 0, false
	}
	first, last := offset/pieceSize, endOffset/pieceSize
	if endOffset%pieceSize != 0 {
		last++
	}
	return int(first), int(last), last > first
}
func prefetchStartupPieces(source MediaSource, percent int) {
	if source.file == nil || source.torrent == nil || source.torrent.Info() == nil {
		return
	}
	begin, end, ok := startupPieceRange(source.file.Offset(), source.file.Length(),
		source.torrent.Info().PieceLength, percent)
	if ok {
		source.torrent.DownloadPieces(begin, end)
	}
}

// Existing /api/v1/settings and provider endpoints remain backward compatible.
func playbackSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeHLSError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST to save playback settings")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var body struct {
		StartupCachePercent *int `json:"startupCachePercent"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			writeHLSError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Playback settings exceed 8 KiB")
		} else {
			writeHLSError(w, http.StatusBadRequest, "invalid_request", "Invalid playback settings")
		}
		return
	}
	if body.StartupCachePercent == nil || *body.StartupCachePercent < 0 || *body.StartupCachePercent > 100 {
		writeHLSError(w, http.StatusBadRequest, "invalid_startup_cache_percent", "Expected an integer from 0 to 100")
		return
	}
	if err := decoder.Decode(new(interface{})); !errors.Is(err, io.EOF) {
		writeHLSError(w, http.StatusBadRequest, "invalid_request", "Unexpected content after playback settings")
		return
	}
	settingsMutex.Lock()
	old := currentSettings.StartupCachePercent
	currentSettings.StartupCachePercent = startupCachePercentPtr(*body.StartupCachePercent)
	settingsMutex.Unlock()
	if err := saveSettingsToFile(); err != nil {
		settingsMutex.Lock()
		currentSettings.StartupCachePercent = old
		settingsMutex.Unlock()
		writeHLSError(w, http.StatusInternalServerError, "settings_save_failed", "Unable to save playback settings")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respondWithJSON(w, http.StatusOK, map[string]int{"startupCachePercent": *body.StartupCachePercent})
}
