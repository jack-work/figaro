package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCE holds a PKCE verifier/challenge pair (RFC 7636).
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
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	return PKCE{Verifier: verifier, Challenge: challenge}, nil
}
