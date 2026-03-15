// Package auth handles OAuth token management with encrypted storage via hush.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// base64url encodes bytes without padding per RFC 7636.
func base64urlEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// PKCE holds a PKCE verifier/challenge pair.
type PKCE struct {
	Verifier  string
	Challenge string
}

// GeneratePKCE creates a new PKCE verifier and S256 challenge.
func GeneratePKCE() (PKCE, error) {
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return PKCE{}, err
	}
	verifier := base64urlEncode(verifierBytes)
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64urlEncode(hash[:])
	return PKCE{Verifier: verifier, Challenge: challenge}, nil
}
