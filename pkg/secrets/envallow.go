package secrets

import (
	"os"
	"strings"
)

// Reading the server's own environment, and who is allowed to.
//
// Three mechanisms could reach it: ${env.*} pipeline templating,
// env://NAME credential references, and the environment a code node's
// subprocess inherits. All three were unrestricted, and the process they
// read is the one holding the deployment's database URL, signing secret
// and encryption key.
//
// They are gated separately because they answer different questions --
// "what may a pipeline author interpolate" is not "what may hold a
// connection credential" -- and conflating them would mean allowing a
// name for one purpose silently allows it for the others. What they
// share is the floor below.

// AlwaysDeniedEnvName reports whether an environment variable may never
// be read through any of those mechanisms, whatever an operator has
// allowed.
//
// Defence against a typo rather than against a determined operator, who
// can edit configuration directly. These are the names where a slip is
// unrecoverable: the encryption key decrypts every stored credential, the
// signing secret mints any session including an administrative one, and
// the database URL is direct access to every tenant's rows.
func AlwaysDeniedEnvName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "BROKOLI_ENCRYPTION_KEY",
		"BROKOLI_JWT_SECRET",
		"BROKOLI_DB_URL",
		"BROKOLI_LICENSE_SIGNING_SECRET":
		return true
	}
	return false
}

// EnvRefAllowEnv names the operator's allowlist for env:// credential
// references.
//
// Separate from the pipeline-templating allowlist on purpose: a variable
// that legitimately holds a database password has no business being
// interpolatable into a node's config, and vice versa.
const EnvRefAllowEnv = "BROKOLI_SECRET_ENV_ALLOW"

// EnvRefAllowed reports whether an env:// reference may resolve this name.
func EnvRefAllowed(name string) bool {
	if name == "" || AlwaysDeniedEnvName(name) {
		return false
	}
	for _, allowed := range strings.Split(os.Getenv(EnvRefAllowEnv), ",") {
		if allowed = strings.TrimSpace(allowed); allowed != "" && allowed == name {
			return true
		}
	}
	return false
}
