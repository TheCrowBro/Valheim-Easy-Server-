package config

import (
	"crypto/rand"
	"encoding/base64"
)

// RandomToken returns n random bytes as URL-safe text.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported systems
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
