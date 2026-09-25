package engine

import (
	"strings"
	"testing"
	"time"
)

// NewVariableContext copies the WHOLE server environment into vc.Env, so
// before the allowlist ${env.NAME} returned any of it. Authoring a
// pipeline is not supposed to be a way to read the control plane's own
// secrets, and it was: the encryption key decrypts every stored
// credential and the signing secret mints any session.

func resolveEnvRef(t *testing.T, name string) string {
	t.Helper()
	vc := NewVariableContext(nil, "run-1", time.Now())
	return vc.Resolve("${env." + name + "}")
}

func TestPipelineCannotReadTheControlPlanesSecrets(t *testing.T) {
	for _, secret := range []string{
		"BROKOLI_ENCRYPTION_KEY",
		"BROKOLI_JWT_SECRET",
		"BROKOLI_DB_URL",
		"BROKOLI_LICENSE_SIGNING_SECRET",
	} {
		t.Run(secret, func(t *testing.T) {
			t.Setenv(secret, "the-actual-secret-value")
			// Even listed explicitly: a typo in the allowlist must not be
			// what hands over the signing key.
			t.Setenv(pipelineEnvAllowEnv, secret)

			got := resolveEnvRef(t, secret)
			if strings.Contains(got, "the-actual-secret-value") {
				t.Fatalf("${env.%s} resolved to the secret", secret)
			}
			if got != "${env."+secret+"}" {
				t.Errorf("refusal should stay visible, got %q", got)
			}
		})
	}
}

func TestUnlistedEnvIsRefusedAndStaysVisible(t *testing.T) {
	t.Setenv("SOME_HOST_SECRET", "should-not-appear")
	t.Setenv(pipelineEnvAllowEnv, "")

	got := resolveEnvRef(t, "SOME_HOST_SECRET")
	if strings.Contains(got, "should-not-appear") {
		t.Fatalf("an unlisted variable resolved: %q", got)
	}
	// Visible, not empty: an empty string is the silent kind of wrong.
	if got != "${env.SOME_HOST_SECRET}" {
		t.Errorf("got %q, want the reference left visible", got)
	}
}

func TestAllowlistedEnvStillResolves(t *testing.T) {
	t.Setenv("DATA_REGION", "eu-west-1")
	t.Setenv(pipelineEnvAllowEnv, "OTHER_ONE, DATA_REGION ,THIRD")

	if got := resolveEnvRef(t, "DATA_REGION"); got != "eu-west-1" {
		t.Errorf("an allowlisted variable did not resolve: %q", got)
	}
}

// An allowlisted name that is simply unset keeps resolving to empty,
// which is what it always did. Only a REFUSAL is made visible.
func TestAllowlistedButUnsetIsStillEmpty(t *testing.T) {
	t.Setenv(pipelineEnvAllowEnv, "NOT_SET_ANYWHERE")
	if got := resolveEnvRef(t, "NOT_SET_ANYWHERE"); got != "" {
		t.Errorf("got %q, want empty for an allowed-but-unset variable", got)
	}
}

// Prefix and substring matches must not sneak through.
func TestAllowlistMatchesExactNamesOnly(t *testing.T) {
	t.Setenv("DATA_REGION_SECRET", "nope")
	t.Setenv("DATA_REGION", "fine")
	t.Setenv(pipelineEnvAllowEnv, "DATA_REGION")

	if got := resolveEnvRef(t, "DATA_REGION_SECRET"); strings.Contains(got, "nope") {
		t.Errorf("a longer name matched a shorter allowlist entry: %q", got)
	}
}

// The ${secret.*} mechanism reads the same map through its own prefix and
// must keep working: it is an operator-scoped convention, not arbitrary
// host access.
func TestSecretPrefixStillWorks(t *testing.T) {
	t.Setenv("BROKED_SECRET_API_TOKEN", "tok-123")
	t.Setenv(pipelineEnvAllowEnv, "")

	vc := NewVariableContext(nil, "run-1", time.Now())
	if got := vc.Resolve("${secret.api_token}"); got != "tok-123" {
		t.Errorf("got %q, want the secret-prefixed lookup to keep working", got)
	}
}
