package main

import "net/http"

// A readiness-aware liveness route for authenticated single-instance
// deployments. Unlike static pages, media readiness needs both executables.
func healthzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !ffmpegAvailable || ffprobePath == "" {
		respondWithJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "media_unavailable"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
