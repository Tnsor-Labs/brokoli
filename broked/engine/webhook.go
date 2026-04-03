package engine

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
)

// GenerateWebhookToken creates a secure random token for pipeline webhook triggers.
// The token is prefixed with "whk_" for easy identification.
func GenerateWebhookToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "whk_" + hex.EncodeToString(b), nil
}

// ValidateWebhookToken performs a constant-time comparison of the provided
// token against the stored token, preventing timing attacks.
func ValidateWebhookToken(provided, stored string) bool {
	return subtle.ConstantTimeCompare([]byte(provided), []byte(stored)) == 1
}
