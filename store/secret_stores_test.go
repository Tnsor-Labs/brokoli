package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Both backends: names are unique per workspace, every read is scoped to
// one, and settings round-trip.
func TestSecretStoresPersist(t *testing.T) {
	backends := map[string]func(t *testing.T) SecretStoreStore{
		"sqlite": func(t *testing.T) SecretStoreStore {
			s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ss.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		},
	}
	if url := os.Getenv("BROKOLI_TEST_POSTGRES_URL"); url != "" {
		backends["postgres"] = func(t *testing.T) SecretStoreStore {
			s, err := NewPostgresStore(url)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = s.db.Exec(`DELETE FROM secret_stores WHERE workspace_id LIKE 'ss-test-%'`)
				s.Close()
			})
			return s
		}
	}
	for name, open := range backends {
		t.Run(name, func(t *testing.T) {
			ss := open(t)
			ws, other := "ss-test-a", "ss-test-b"
			now := time.Now().UTC().Truncate(time.Millisecond)
			st := &models.SecretStore{ID: "ss-" + name + "-1", Name: "vault-prod", WorkspaceID: ws, Provider: "vault",
				Settings: map[string]string{"address": "https://vault", "mount": "secret"}, AuthMethod: "token",
				AuthSettings: map[string]string{}, CredentialRef: "encrypted://abc", CreatedAt: now, UpdatedAt: now}
			if err := ss.CreateSecretStore(st); err != nil {
				t.Fatal(err)
			}
			dup := *st
			dup.ID = "ss-" + name + "-2"
			if err := ss.CreateSecretStore(&dup); !errors.Is(err, ErrSecretStoreNameTaken) {
				t.Fatalf("duplicate name in one workspace: %v", err)
			}
			dup.WorkspaceID = other
			if err := ss.CreateSecretStore(&dup); err != nil {
				t.Fatalf("the same name in another workspace: %v", err)
			}
			got, err := ss.GetSecretStoreByName(ws, "vault-prod")
			if err != nil || got.ID != st.ID || got.Settings["mount"] != "secret" || got.CredentialRef != "encrypted://abc" {
				t.Fatalf("get by name: %+v %v", got, err)
			}
			if _, err := ss.GetSecretStore(other, st.ID); !errors.Is(err, ErrSecretStoreNotFound) {
				t.Fatalf("read across workspaces: %v", err)
			}
			if err := ss.DeleteSecretStore(other, st.ID); !errors.Is(err, ErrSecretStoreNotFound) {
				t.Fatalf("delete across workspaces: %v", err)
			}
			got.Name, got.UpdatedAt = "vault-main", now.Add(time.Second)
			if err := ss.UpdateSecretStore(got); err != nil {
				t.Fatal(err)
			}
			list, _ := ss.ListSecretStores(ws)
			if len(list) != 1 || list[0].Name != "vault-main" {
				t.Fatalf("list after rename = %+v", list)
			}
			if err := ss.DeleteSecretStore(ws, st.ID); err != nil {
				t.Fatal(err)
			}
			if list, _ := ss.ListSecretStores(ws); len(list) != 0 {
				t.Fatalf("list after delete = %+v", list)
			}
		})
	}
}
