package cmd

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// The riskier of the two worker shapes was also the quieter one:
// indistinguishable at a glance from a worker that holds nothing, so the
// trade was being made by operators who had never been told they were
// making it.

func captureWorkerAnnouncement(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	announceWorkerTrustAssumptions()
	return buf.String()
}

func TestWorkerAnnouncesEachSecretItHolds(t *testing.T) {
	t.Setenv("BROKOLI_DB_URL", "postgres://user:pw@host/db")
	t.Setenv("BROKOLI_JWT_SECRET", "s3cret")
	t.Setenv("BROKOLI_ENCRYPTION_KEY", "k3y")

	out := captureWorkerAnnouncement(t)
	for _, want := range []string{
		"SHARED-STORE MODE",
		"BROKOLI_DB_URL", "every tenant's data",
		"BROKOLI_JWT_SECRET", "administrative",
		"BROKOLI_ENCRYPTION_KEY", "decrypt",
		"same trust boundary",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("announcement does not mention %q:\n%s", want, out)
		}
	}
	// The value itself must never be logged.
	for _, secret := range []string{"s3cret", "k3y", "pw@host"} {
		if strings.Contains(out, secret) {
			t.Errorf("announcement leaked a secret value %q:\n%s", secret, out)
		}
	}
}

// Only what is actually set. A worker holding just one of them should not
// be told it holds all three.
func TestWorkerAnnouncesOnlyWhatIsSet(t *testing.T) {
	t.Setenv("BROKOLI_DB_URL", "postgres://host/db")
	t.Setenv("BROKOLI_JWT_SECRET", "")
	t.Setenv("BROKOLI_ENCRYPTION_KEY", "")

	out := captureWorkerAnnouncement(t)
	if !strings.Contains(out, "BROKOLI_DB_URL") {
		t.Errorf("did not name the secret it does hold:\n%s", out)
	}
	if strings.Contains(out, "BROKOLI_JWT_SECRET") {
		t.Errorf("named a secret that is not set:\n%s", out)
	}
}

// And an API-only worker should be able to prove it holds nothing, which
// is the state ee#196 is driving toward.
func TestWorkerWithNoSecretsSaysSo(t *testing.T) {
	t.Setenv("BROKOLI_DB_URL", "")
	t.Setenv("BROKOLI_JWT_SECRET", "")
	t.Setenv("BROKOLI_ENCRYPTION_KEY", "")

	out := captureWorkerAnnouncement(t)
	if !strings.Contains(out, "holds no control-plane secrets") {
		t.Errorf("a worker holding nothing should say so:\n%s", out)
	}
	if strings.Contains(out, "SHARED-STORE") {
		t.Errorf("a worker holding nothing was called shared-store:\n%s", out)
	}
}
