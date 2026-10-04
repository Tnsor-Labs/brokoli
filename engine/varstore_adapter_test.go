package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A secret variable is reported as secret with or without a key: with one
// it is decrypted here; without one (a worker whose control plane decrypts
// for it) it arrives as the value. A plain variable is never secret.
func TestVarStoreAdapterReportsSecretsAsSecret(t *testing.T) {
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "vars.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := testKey(6)
	sealed, _ := key.Encrypt("s3cret-value")
	now := time.Now().UTC()
	for _, v := range []models.Variable{
		{Key: "sealed", Value: sealed, Type: models.VarTypeSecret, WorkspaceID: "ws", CreatedAt: now, UpdatedAt: now},
		{Key: "delivered", Value: "already-plain", Type: models.VarTypeSecret, WorkspaceID: "ws", CreatedAt: now, UpdatedAt: now},
		{Key: "plain", Value: "hello", Type: models.VarTypeString, WorkspaceID: "ws", CreatedAt: now, UpdatedAt: now},
	} {
		v := v
		if err := st.SetVariable(&v); err != nil {
			t.Fatal(err)
		}
	}
	withKey := NewVarStoreAdapter(st, key)
	if v, secret, err := withKey.GetVariableValue("ws", "sealed"); err != nil || v != "s3cret-value" || !secret {
		t.Fatalf("with a key: %q %v %v", v, secret, err)
	}
	noKey := NewVarStoreAdapter(st, nil)
	if v, secret, err := noKey.GetVariableValue("ws", "delivered"); err != nil || v != "already-plain" || !secret {
		t.Fatalf("without a key, a delivered secret must stay secret: %q %v %v", v, secret, err)
	}
	if _, secret, _ := noKey.GetVariableValue("ws", "plain"); secret {
		t.Fatal("a plain variable reported as secret")
	}
}
