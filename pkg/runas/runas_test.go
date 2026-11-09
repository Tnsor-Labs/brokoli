package runas

import (
	"context"
	"strings"
	"testing"
)

func TestUnsetIsTheServersOwnUser(t *testing.T) {
	t.Setenv(EnvUIDs, "")
	id, err := For("org/ws")
	if err != nil || id.Enabled() {
		t.Fatalf("unset: got %v, %v; want the server's own user", id, err)
	}
	if Configured() {
		t.Fatal("Configured with the setting unset")
	}
	env := []string{"HOME=/root", "PATH=/bin"}
	if got := id.Env(env, "/x"); strings.Join(got, " ") != "HOME=/root PATH=/bin" {
		t.Fatalf("Env changed the environment while disabled: %v", got)
	}
}

func TestMalformedRangesAreRefused(t *testing.T) {
	for _, raw := range []string{"1000000000", "a-b", "100-200000", "2000000-1000000", "1000000000-4294967295"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(EnvUIDs, raw)
			if _, err := For("t"); err == nil {
				t.Fatalf("%q accepted", raw)
			}
			if Configured() {
				t.Fatalf("%q counted as configured", raw)
			}
			if err := Check(); err == nil {
				t.Fatalf("Check accepted %q", raw)
			}
		})
	}
}

func TestTenantsGetTheirOwnStableUser(t *testing.T) {
	t.Setenv(EnvUIDs, "1000000000-1999999999")
	a1, err := For("orgA/ws1")
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := For("orgA/ws1")
	b, _ := For("orgB/ws1")
	none, _ := For("")
	if !a1.Enabled() || a1.UID() != a2.UID() {
		t.Fatalf("one tenant got %v then %v", a1, a2)
	}
	if a1.UID() == b.UID() || a1.UID() == none.UID() {
		t.Fatalf("tenants share a user: A=%v B=%v none=%v", a1, b, none)
	}
	for _, id := range []Identity{a1, b, none} {
		if id.UID() < 1000000000 || id.UID() > 1999999999 {
			t.Fatalf("%v outside the range", id)
		}
	}
	ctxID, _ := ForContext(WithTenant(context.Background(), "orgA/ws1"))
	if ctxID != a1 {
		t.Fatalf("ForContext = %v, want %v", ctxID, a1)
	}

	t.Setenv(EnvUIDs, "1500000000-1500000000")
	one, _ := For("anything")
	if one.UID() != 1500000000 {
		t.Fatalf("a one-ID range gave %v", one)
	}
}

func TestEnvMovesHomeAndTempToTheChildsFolder(t *testing.T) {
	t.Setenv(EnvUIDs, "1000000000-1999999999")
	id, _ := For("t")
	got := id.Env([]string{"HOME=/root", "TMPDIR=/tmp", "PATH=/bin"}, "/scratch")
	if strings.Join(got, " ") != "PATH=/bin HOME=/scratch TMPDIR=/scratch" {
		t.Fatalf("Env = %v", got)
	}
}
