package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Saving a secret without re-entering its value restored the stored
// ciphertext and then encrypted it AGAIN, because the encrypt step could
// not tell restored ciphertext from a freshly typed secret: neither is
// empty and neither is the mask. A pipeline decrypted once and got
// ciphertext back, and the only way out was to retype the secret.

func secretHandler(t *testing.T) (*VariableHandler, *crypto.Config, store.Store) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "vars.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	c := &crypto.Config{Key: key}
	return NewVariableHandler(s, c), c, s
}

func saveVariable(t *testing.T, h *VariableHandler, v map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(v)
	req := httptest.NewRequest(http.MethodPost, "/api/variables", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	return rec
}

func TestSecretSurvivesASaveThatDoesNotRetypeIt(t *testing.T) {
	h, c, s := secretHandler(t)

	if rec := saveVariable(t, h, map[string]interface{}{
		"key": "api_token", "value": "the-real-secret", "type": string(models.VarTypeSecret),
	}); rec.Code >= 400 {
		t.Fatalf("first save: %d %s", rec.Code, rec.Body.String())
	}

	// Save again the way an editor does when only the description changed:
	// the value comes back masked because that is what the read returned.
	if rec := saveVariable(t, h, map[string]interface{}{
		"key": "api_token", "value": "********", "type": string(models.VarTypeSecret),
		"description": "now with a description",
	}); rec.Code >= 400 {
		t.Fatalf("second save: %d %s", rec.Code, rec.Body.String())
	}

	stored, err := s.GetVariable("api_token")
	if err != nil {
		t.Fatalf("GetVariable: %v", err)
	}
	got, err := c.Decrypt(stored.Value)
	if err != nil {
		t.Fatalf("stored value did not decrypt: %v", err)
	}
	if got != "the-real-secret" {
		// A double encryption decrypts to ciphertext, not to the secret.
		t.Errorf("one decrypt gave %q, want the original secret; the value was encrypted twice", got)
	}
}

// The empty-value path is the same bug by another route: an editor that
// omits the field entirely.
func TestSecretSurvivesASaveWithNoValueField(t *testing.T) {
	h, c, s := secretHandler(t)
	saveVariable(t, h, map[string]interface{}{
		"key": "db_password", "value": "hunter2", "type": string(models.VarTypeSecret),
	})
	saveVariable(t, h, map[string]interface{}{
		"key": "db_password", "type": string(models.VarTypeSecret), "description": "d",
	})

	stored, _ := s.GetVariable("db_password")
	got, err := c.Decrypt(stored.Value)
	if err != nil || got != "hunter2" {
		t.Errorf("one decrypt gave %q (err %v), want the original secret", got, err)
	}
}

// And a genuinely new value must still be encrypted, or the fix has
// traded a double encryption for a plaintext secret at rest.
func TestARetypedSecretIsStillEncrypted(t *testing.T) {
	h, c, s := secretHandler(t)
	saveVariable(t, h, map[string]interface{}{
		"key": "rotating", "value": "first", "type": string(models.VarTypeSecret),
	})
	saveVariable(t, h, map[string]interface{}{
		"key": "rotating", "value": "second", "type": string(models.VarTypeSecret),
	})

	stored, _ := s.GetVariable("rotating")
	if stored.Value == "second" {
		t.Fatal("the new secret was stored in plaintext")
	}
	got, err := c.Decrypt(stored.Value)
	if err != nil {
		t.Fatalf("stored value did not decrypt: %v", err)
	}
	if got != "second" {
		t.Errorf("decrypted to %q, want the new value", got)
	}
}
