// Package auth handles API key generation and hashing. Plaintext keys are
// shown once at creation time and never stored.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// KeyPrefix prefixes every generated tenant key so keys are recognizable in
// logs and secret scanners.
const KeyPrefix = "amk_"

// Hash returns the hex-encoded SHA-256 of a key. Store hashes only.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// GenerateKey creates a new random API key. It returns the plaintext
// (shown to the operator exactly once) and the hash to persist.
func GenerateKey() (plain, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plain = KeyPrefix + hex.EncodeToString(buf)
	return plain, Hash(plain), nil
}

// RandomSecret returns a hex token suitable for webhook signing.
func RandomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
