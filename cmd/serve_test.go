package cmd

import (
	"os"
	"testing"
)

func TestDefaultDatabasePath(t *testing.T) {
	t.Setenv("BROKOLI_DB_URL", "postgres://example.invalid/brokoli")
	if got := defaultDatabasePath(); got != "postgres://example.invalid/brokoli" {
		t.Fatalf("defaultDatabasePath() = %q", got)
	}

	t.Setenv("BROKOLI_DB_URL", "")
	if got := defaultDatabasePath(); got != "./brokoli.db" {
		t.Fatalf("defaultDatabasePath() = %q", got)
	}
}

func TestEncryptionKeyPathDoesNotContainDatabaseCredentials(t *testing.T) {
	if got := encryptionKeyPath("postgres://user:secret@db/brokoli"); got != "./brokoli.db.key" {
		t.Fatalf("encryptionKeyPath(Postgres) = %q", got)
	}
	if got := encryptionKeyPath("/data/brokoli.db"); got != "/data/brokoli.db.key" {
		t.Fatalf("encryptionKeyPath(SQLite) = %q", got)
	}
}

func TestSyncPortEnvForSelfReferences_SetsPortWhenNeitherConfigured(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("BROKOLI_SERVER_URL", "")
	syncPortEnvForSelfReferences(9091)
	if got := os.Getenv("PORT"); got != "9091" {
		t.Fatalf("PORT = %q, want 9091", got)
	}
}

func TestSyncPortEnvForSelfReferences_DoesNotOverrideExplicitPort(t *testing.T) {
	t.Setenv("PORT", "7000")
	t.Setenv("BROKOLI_SERVER_URL", "")
	syncPortEnvForSelfReferences(9091)
	if got := os.Getenv("PORT"); got != "7000" {
		t.Fatalf("PORT = %q, want unchanged 7000", got)
	}
}

func TestSyncPortEnvForSelfReferences_DoesNotOverrideExplicitServerURL(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("BROKOLI_SERVER_URL", "https://brokoli.example.com")
	syncPortEnvForSelfReferences(9091)
	if got := os.Getenv("PORT"); got != "" {
		t.Fatalf("PORT = %q, want left unset when BROKOLI_SERVER_URL is already configured", got)
	}
}

func TestPlatformServicesRunOnceInDistributedMode(t *testing.T) {
	for _, mode := range []string{"all", "scheduler"} {
		if !shouldStartPlatformServices(mode) {
			t.Fatalf("platform services disabled in %s mode", mode)
		}
	}
	for _, mode := range []string{"api", "worker"} {
		if shouldStartPlatformServices(mode) {
			t.Fatalf("platform services enabled in %s mode", mode)
		}
	}
}
