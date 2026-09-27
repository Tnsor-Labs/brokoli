package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Config holds the encryption key for secret storage.
type Config struct {
	Key []byte // 32 bytes for AES-256
}

// Encrypt encrypts plaintext using AES-256-GCM.
// Returns a base64-encoded string (nonce + ciphertext).
func (c *Config) Encrypt(plaintext string) (string, error) {
	if len(c.Key) != 32 {
		return "", errors.New("encryption key must be 32 bytes")
	}
	if plaintext == "" {
		return "", nil
	}

	block, err := aes.NewCipher(c.Key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a base64-encoded AES-256-GCM ciphertext.
func (c *Config) Decrypt(encoded string) (string, error) {
	if len(c.Key) != 32 {
		return "", errors.New("encryption key must be 32 bytes")
	}
	if encoded == "" {
		return "", nil
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	block, err := aes.NewCipher(c.Key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}

// LoadOrCreateKey returns the key that encrypts stored credentials.
//
// In order: BROKOLI_ENCRYPTION_KEY (base64, 32 bytes), the key file at
// keyPath, or a new key written to keyPath when that file does not exist.
//
// Every failure is an error, never a substitute key. A key that is
// configured but unusable, or a key file that exists but cannot be read
// or is too short, must stop the server: continuing under any other key
// makes every credential saved under the real one unreadable, and every
// credential saved from then on unreadable once the real key is back. An
// existing key file is never overwritten, for the same reason.
func LoadOrCreateKey(keyPath string) ([]byte, error) {
	if envKey := os.Getenv("BROKOLI_ENCRYPTION_KEY"); envKey != "" {
		decoded, err := base64.StdEncoding.DecodeString(envKey)
		if err != nil {
			return nil, fmt.Errorf("BROKOLI_ENCRYPTION_KEY must be base64-encoded: %w", err)
		}
		if len(decoded) != 32 {
			return nil, fmt.Errorf("BROKOLI_ENCRYPTION_KEY must be exactly 32 bytes (got %d)", len(decoded))
		}
		return decoded, nil
	}

	key, err := readKeyFile(keyPath)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return key, err
	}

	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}
	if dir := filepath.Dir(keyPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create directory for encryption key file %s: %w", keyPath, err)
		}
	}
	// O_EXCL: if another process created the file since it was read above
	// (two replicas starting on one volume), use its key rather than
	// replacing it.
	// #nosec G304 -- keyPath is the operator's database path plus ".key", never request input
	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return readKeyFile(keyPath)
	}
	if err != nil {
		return nil, fmt.Errorf("create encryption key file %s: %w; set BROKOLI_ENCRYPTION_KEY or make the directory writable", keyPath, err)
	}
	// On a failed write the partial file is removed, so the next start
	// creates a whole key instead of refusing a short one. The write's
	// error is the one worth reporting; a failed cleanup leaves a file the
	// next start refuses by name.
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		_ = os.Remove(keyPath)
		return nil, fmt.Errorf("write encryption key file %s: %w", keyPath, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(keyPath)
		return nil, fmt.Errorf("write encryption key file %s: %w", keyPath, err)
	}
	return key, nil
}

// readKeyFile reads an existing key file. The error wraps fs.ErrNotExist
// only when there is no file at all.
func readKeyFile(keyPath string) ([]byte, error) {
	key, err := os.ReadFile(keyPath) // #nosec G304 -- the operator's database path plus ".key", never request input
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("read encryption key file %s: %w", keyPath, err)
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("encryption key file %s holds %d bytes, want 32; it is left as it is, because replacing it would make every credential saved under the key it held unreadable", keyPath, len(key))
	}
	return key[:32], nil
}
