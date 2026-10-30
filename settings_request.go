package main

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Legacy settings endpoints keep their JSON shapes while enforcing a common
// input cap, including requests without Content-Length.
func decodeLimitedSettingsBody(w http.ResponseWriter, r *http.Request, into interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			respondWithJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "Settings request exceeds 64 KiB"})
		} else {
			respondWithJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		}
		return false
	}
	return true
}
