// Package id provides short random hex ID generation used by the browser
// and tab managers.
package id

import (
	"crypto/rand"
	"encoding/hex"
)

// Generate produces a short random hex ID (8 hex characters / 4 bytes).
func Generate() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
