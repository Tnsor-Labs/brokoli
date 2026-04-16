package secrets

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/crypto"
)

// EncryptedResolver resolves encrypted://CIPHERTEXT references by
// decrypting the AES-256-GCM ciphertext with the server's key.
// Also used as the fallback for legacy values that pre-date the
// ref system (bare base64 blobs stored in password_enc).
type EncryptedResolver struct {
	crypto *crypto.Config
}

// NewEncryptedResolver creates a resolver backed by the given crypto config.
func NewEncryptedResolver(c *crypto.Config) *EncryptedResolver {
	return &EncryptedResolver{crypto: c}
}

func (e *EncryptedResolver) Scheme() string { return "encrypted" }

func (e *EncryptedResolver) Resolve(_ context.Context, ref string) (string, error) {
	if e.crypto == nil {
		return "", fmt.Errorf("secrets/encrypted: no encryption key configured")
	}

	ciphertext := ref
	if strings.HasPrefix(ref, "encrypted://") {
		ciphertext = strings.TrimPrefix(ref, "encrypted://")
	}

	if ciphertext == "" {
		return "", nil
	}

	plain, err := e.crypto.Decrypt(ciphertext)
	if err != nil {
		return "", fmt.Errorf("secrets/encrypted: decrypt failed: %w", err)
	}
	return plain, nil
}
