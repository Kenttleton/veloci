package handlers

import (
	"encoding/json"
	"net/http"
)

// Health responds with {"status":"ok"} and a 200 status code.
func Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
