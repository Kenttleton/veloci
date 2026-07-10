package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

// hashToken returns the hex-encoded SHA-256 hash of a raw token string.
// This is used to look up invite tokens — only the hash is stored, never the raw value.
func hashToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}
