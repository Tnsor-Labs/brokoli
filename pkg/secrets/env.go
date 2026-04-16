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
	val, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("secrets/env: variable %q not set", name)
	}
	return val, nil
}
