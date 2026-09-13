package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// EnvResolver resolves env://VAR_NAME references by reading
// the named environment variable.
type EnvResolver struct{}

func (EnvResolver) Scheme() string { return "env" }

func (EnvResolver) Resolve(_ context.Context, ref string) (string, error) {
	name := strings.TrimPrefix(ref, "env://")
	if name == "" {
		return "", fmt.Errorf("secrets/env: empty variable name in ref %q", ref)
	}
	// Deny by default. This resolved any environment variable of the
	// server process, so a connection whose password_ref was
	// env://BROKOLI_JWT_SECRET handed back the signing secret -- and a
	// connection is something a workspace editor can create.
	//
	// Refused by name rather than by returning empty: a credential that
	// silently resolves to nothing produces an authentication failure
	// against the data source, which is a long way from the cause.
	if !EnvRefAllowed(name) {
		return "", fmt.Errorf(
			"secrets/env: %q is not listed in %s, so an env:// reference may not read it. "+
				"The server's environment holds its own database URL, signing secret and "+
				"encryption key; list the variables a connection may use", name, EnvRefAllowEnv)
	}
	val, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("secrets/env: variable %q not set", name)
	}
	return val, nil
}
