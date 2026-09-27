package crypto

import (
	"bytes"
	"encoding/base64"
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
	t.Setenv("BROKOLI_ENCRYPTION_KEY", "")
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

// #754: every way of failing to get the real key is an error. None of
// them may produce a key, because any key other than the real one
// encrypts new credentials that the real one cannot read, and cannot read
// the ones it encrypted.

func TestLoadOrCreateKeyRefusesAnInvalidEnvironmentKey(t *testing.T) {
	for name, value := range map[string]string{
		"not base64": "not-base64!!",
		"16 bytes":   base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"33 bytes":   base64.StdEncoding.EncodeToString(make([]byte, 33)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BROKOLI_ENCRYPTION_KEY", value)
			keyPath := filepath.Join(t.TempDir(), "test.key")
			key, err := LoadOrCreateKey(keyPath)
			if err == nil {
				t.Fatalf("an invalid BROKOLI_ENCRYPTION_KEY produced a key: %x", key)
			}
			if _, statErr := os.Stat(keyPath); !os.IsNotExist(statErr) {
				t.Error("an invalid BROKOLI_ENCRYPTION_KEY fell through to the key file")
			}
		})
	}
}

func TestLoadOrCreateKeyLeavesAShortKeyFileAlone(t *testing.T) {
	t.Setenv("BROKOLI_ENCRYPTION_KEY", "")
	keyPath := filepath.Join(t.TempDir(), "test.key")
	damaged := []byte("short")
	if err := os.WriteFile(keyPath, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := LoadOrCreateKey(keyPath)
	if err == nil {
		t.Fatalf("a 5-byte key file produced a key: %x", key)
	}
	got, _ := os.ReadFile(keyPath)
	if !bytes.Equal(got, damaged) {
		t.Errorf("the damaged key file was replaced: now %d bytes", len(got))
	}
}

func TestLoadOrCreateKeyRefusesAnUnreadableKeyFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	t.Setenv("BROKOLI_ENCRYPTION_KEY", "")
	keyPath := filepath.Join(t.TempDir(), "test.key")
	real := bytes.Repeat([]byte{7}, 32)
	if err := os.WriteFile(keyPath, real, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(keyPath, 0o600) })

	key, err := LoadOrCreateKey(keyPath)
	if err == nil {
		t.Fatalf("an unreadable key file produced a key: %x", key)
	}
	os.Chmod(keyPath, 0o600)
	got, _ := os.ReadFile(keyPath)
	if !bytes.Equal(got, real) {
		t.Error("the unreadable key file was replaced")
	}
}

// A key that cannot be saved would be lost at the next restart, taking
// every credential encrypted under it along.
func TestLoadOrCreateKeyFailsWhenANewKeyCannotBeSaved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a directory whatever its mode")
	}
	t.Setenv("BROKOLI_ENCRYPTION_KEY", "")
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	key, err := LoadOrCreateKey(filepath.Join(dir, "test.key"))
	if err == nil {
		t.Fatalf("a key that could not be saved was returned: %x", key)
	}
}

func TestLoadOrCreateKeyWritesAPrivateFile(t *testing.T) {
	t.Setenv("BROKOLI_ENCRYPTION_KEY", "")
	keyPath := filepath.Join(t.TempDir(), "sub", "test.key")
	if _, err := LoadOrCreateKey(keyPath); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("key file mode = %o, want 600", mode)
	}
}

// Key files longer than 32 bytes have always been read as their first 32
// bytes. Changing that would lock deployments out of their credentials.
func TestLoadOrCreateKeyKeepsReadingLongerKeyFiles(t *testing.T) {
	t.Setenv("BROKOLI_ENCRYPTION_KEY", "")
	keyPath := filepath.Join(t.TempDir(), "test.key")
	content := []byte("0123456789abcdef0123456789abcdef-and-more\n")
	if err := os.WriteFile(keyPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := LoadOrCreateKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, content[:32]) {
		t.Errorf("key = %q, want the file's first 32 bytes", key)
	}
}
