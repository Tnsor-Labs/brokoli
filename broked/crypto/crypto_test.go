package crypto

import (
	"os"
	"path/filepath"
	"testing"
)

func testConfig() *Config {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return &Config{Key: key}
}

func TestEncryptDecrypt(t *testing.T) {
	c := testConfig()

	plaintext := "super-secret-password-123"
	encrypted, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if encrypted == plaintext {
		t.Error("encrypted should differ from plaintext")
	}

	decrypted, err := c.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("got %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptEmpty(t *testing.T) {
	c := testConfig()
	enc, err := c.Encrypt("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	if enc != "" {
		t.Error("encrypting empty string should return empty")
	}
}

func TestDecryptEmpty(t *testing.T) {
	c := testConfig()
	dec, err := c.Decrypt("")
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}
	if dec != "" {
		t.Error("decrypting empty string should return empty")
	}
}

func TestDecryptWrongKey(t *testing.T) {
	c1 := testConfig()
	c2 := &Config{Key: make([]byte, 32)}
	for i := range c2.Key {
		c2.Key[i] = byte(255 - i) // different key
	}

	encrypted, err := c1.Encrypt("test")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	_, err = c2.Decrypt(encrypted)
	if err == nil {
		t.Error("expected error when decrypting with wrong key")
	}
}

func TestEncryptDecryptUnicode(t *testing.T) {
	c := testConfig()

	plaintext := "日本語テスト 🔒 special chars: <>&\""
	encrypted, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	decrypted, err := c.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("got %q, want %q", decrypted, plaintext)
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test.key")

	// Should create a new key
	key1, err := LoadOrCreateKey(keyPath)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if len(key1) != 32 {
		t.Errorf("key length = %d, want 32", len(key1))
	}

	// File should exist
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Error("key file should exist")
	}

	// Should load the same key
	key2, err := LoadOrCreateKey(keyPath)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if string(key1) != string(key2) {
		t.Error("second load should return same key")
	}
}

func TestInvalidKeyLength(t *testing.T) {
	c := &Config{Key: []byte("short")}
	_, err := c.Encrypt("test")
	if err == nil {
		t.Error("expected error for short key")
	}
}
