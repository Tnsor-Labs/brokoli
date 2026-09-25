package secrets

import (
	"context"
	"strings"
	"testing"
)

// env:// resolved any environment variable of the server process, so a
// connection whose password_ref was env://BROKOLI_JWT_SECRET handed back
// the signing secret. A connection is something a workspace editor can
// create.

func TestEnvRefCannotReadTheControlPlanesSecrets(t *testing.T) {
	for _, secret := range []string{
		"BROKOLI_ENCRYPTION_KEY", "BROKOLI_JWT_SECRET",
		"BROKOLI_DB_URL", "BROKOLI_LICENSE_SIGNING_SECRET",
	} {
		t.Run(secret, func(t *testing.T) {
			t.Setenv(secret, "the-actual-secret-value")
			// Listed explicitly: a typo must not be what hands it over.
			t.Setenv(EnvRefAllowEnv, secret)

			got, err := EnvResolver{}.Resolve(context.Background(), "env://"+secret)
			if err == nil {
				t.Fatalf("env://%s resolved", secret)
			}
			if strings.Contains(got, "the-actual-secret-value") ||
				strings.Contains(err.Error(), "the-actual-secret-value") {
				t.Error("the secret's value appeared in the result or the error")
			}
		})
	}
}

func TestEnvRefRefusesAnUnlistedVariable(t *testing.T) {
	t.Setenv("SOME_HOST_VALUE", "should-not-appear")
	t.Setenv(EnvRefAllowEnv, "")

	got, err := EnvResolver{}.Resolve(context.Background(), "env://SOME_HOST_VALUE")
	if err == nil {
		t.Fatal("an unlisted variable resolved")
	}
	if strings.Contains(got, "should-not-appear") {
		t.Error("the value leaked through the refusal")
	}
	// Refused by name, not by returning empty: an empty credential fails
	// against the data source, a long way from the cause.
	for _, want := range []string{"SOME_HOST_VALUE", EnvRefAllowEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

func TestEnvRefResolvesAnAllowedVariable(t *testing.T) {
	t.Setenv("WAREHOUSE_PASSWORD", "pw-123")
	t.Setenv(EnvRefAllowEnv, "OTHER, WAREHOUSE_PASSWORD ,THIRD")

	got, err := EnvResolver{}.Resolve(context.Background(), "env://WAREHOUSE_PASSWORD")
	if err != nil {
		t.Fatalf("an allowed variable was refused: %v", err)
	}
	if got != "pw-123" {
		t.Errorf("got %q, want the value", got)
	}
}

// The two allowlists are deliberately separate: a variable holding a
// database password has no business being interpolatable into node
// config, and vice versa.
func TestTheTwoAllowlistsAreIndependent(t *testing.T) {
	t.Setenv("WAREHOUSE_PASSWORD", "pw-123")
	t.Setenv(EnvRefAllowEnv, "")
	t.Setenv("BROKOLI_PIPELINE_ENV_ALLOW", "WAREHOUSE_PASSWORD")

	if _, err := (EnvResolver{}).Resolve(context.Background(), "env://WAREHOUSE_PASSWORD"); err == nil {
		t.Error("the pipeline-templating allowlist also opened env:// references")
	}
}
