package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

func base64urlEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

type PKCE struct {
	Verifier  string
	Challenge string
}

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
