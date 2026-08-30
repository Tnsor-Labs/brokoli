package codeexec

import (
	"os"
	"strings"
)

// workerEnv is the environment a code worker starts with: an allowlist,
// not the host environment. The legacy spawn path passed os.Environ()
// wholesale, which handed every user script whatever credentials the
// engine process held — the ADR-029 secrets fix is this filter. The
// escape hatches, for deployments whose scripts legitimately read host
// env: BROKOLI_CODE_PASS_ENV as a comma-separated list of extra
// variable names, or "*" to restore the old everything behavior.
func workerEnv() []string {
	pass := strings.TrimSpace(os.Getenv("BROKOLI_CODE_PASS_ENV"))
	if pass == "*" {
		return os.Environ()
	}
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "LANG": true, "TZ": true,
		"TMPDIR": true, "PYTHONIOENCODING": true,
	}
	for _, name := range strings.Split(pass, ",") {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = true
		}
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if allowed[name] || strings.HasPrefix(name, "LC_") {
			env = append(env, kv)
		}
	}
	return env
}
