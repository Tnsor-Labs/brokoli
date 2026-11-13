package codeexec

import (
	"os"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
)

// WorkerEnv is the environment user code starts with: an allowlist, not
// the host environment. The legacy spawn path passed os.Environ()
// wholesale, which handed every user script whatever credentials the
// engine process held — the ADR-029 secrets fix is this filter. The
// escape hatches, for deployments whose scripts legitimately read host
// env: BROKOLI_CODE_PASS_ENV as a comma-separated list of extra
// variable names, or "*" to restore the old everything behavior.
//
// Exported because ADR-033's task harnesses need the same policy and
// deserve the same one, not a second copy of it: a task runs user code
// exactly as a code node does, so "which environment may user code
// see" has one answer here rather than one per execution path.
func WorkerEnv() []string {
	return AllowlistedEnv("BROKOLI_CODE_PASS_ENV", "PYTHONIOENCODING")
}

// AllowlistedEnv is the host environment filtered to a baseline every
// subprocess needs (PATH, HOME, LANG, TZ, TMPDIR, LC_*), the extra names
// given, and whatever the operator lists in passVar: a comma-separated set
// of further names, or "*" for the whole host environment.
//
// Never the names secrets.AlwaysDeniedEnvName refuses, whatever passVar
// says, "*" included: the encryption key, the session signing secret and
// the database URL would hand the code every tenant's credentials,
// sessions and rows. ${env.*} and env:// references already share that
// floor; the environment a child inherits did not, so "*" -- the
// documented way to give scripts the host environment -- gave them these
// too.
//
// One implementation for every child Brokoli starts with untrusted or
// third-party code in it -- code nodes, task harnesses, native drivers --
// each with its own passVar, so widening one does not widen the others.
func AllowlistedEnv(passVar string, extra ...string) []string {
	pass := strings.TrimSpace(os.Getenv(passVar))
	all := pass == "*"
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "LANG": true, "TZ": true, "TMPDIR": true,
	}
	for _, name := range extra {
		allowed[name] = true
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
		if secrets.AlwaysDeniedEnvName(name) {
			continue
		}
		if all || allowed[name] || strings.HasPrefix(name, "LC_") {
			env = append(env, kv)
		}
	}
	return env
}
