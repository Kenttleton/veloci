package handlers

import (
	"crypto/sha256"
	"encoding/hex"
)

// hashToken returns the hex-encoded SHA-256 hash of a raw token string.
// Only the hash is stored — the raw invite token is never persisted.
func hashToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}
