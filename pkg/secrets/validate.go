package secrets

import (
	"fmt"
	"regexp"
	"strings"
)

// Validating a reference when a connection is saved (#781).
//
// A reference with an unknown scheme, a typo or a malformed body used to
// be accepted and found only on the first run, as a failed node.
// ValidateRef checks the reference's shape without resolving it: it reads
// no secret, no environment variable's value, and makes no request.
//
// It deliberately does not apply the operator allowlists
// (BROKOLI_SECRET_ENV_ALLOW, BROKOLI_SECRET_VAULT_ALLOW,
// BROKOLI_SECRET_K8S_ALLOW) or the server's Vault and Kubernetes
// configuration. A reference is resolved on the machine that runs the node,
// and a remote worker has its own environment, allowlists and Vault: a
// reference this server would refuse may be exactly what a worker in the
// customer's network resolves. The run (#758) and Test connection (#759)
// apply them where it matters, and say so by name. What is refused here is
// refused on every machine: an unknown scheme, a malformed body, and an
// environment variable no allowlist can ever grant.

// refValidators holds the schemes a client may store a reference with,
// and how each checks its body. ADR-041 Phase 2 adds "secret" here, for
// references to a workspace's own secret store.
var refValidators = map[string]func(body string) error{
	"env":   validateEnvRef,
	"vault": validateVaultRef,
	"k8s":   validateK8sRef,
}

// RefSchemes lists the schemes ValidateRef accepts, for messages.
func RefSchemes() []string {
	return []string{"env://", "vault://", "k8s://"}
}

// ValidateRef reports why ref cannot be stored as a credential reference,
// or nil. An encrypted:// reference is refused: the server creates those
// itself when a credential is entered, and a client-supplied one would be
// ciphertext the server decrypts with its own key. The caller decides
// whether an unchanged, already stored reference needs checking at all.
func ValidateRef(ref string) error {
	scheme, body, ok := parseRef(ref)
	if !ok {
		return fmt.Errorf("%q is not a reference: a reference starts with %s", ref, strings.Join(RefSchemes(), ", "))
	}
	if scheme == "encrypted" {
		return fmt.Errorf("encrypted:// references are created by the server when a credential is entered; " +
			"send the credential itself instead")
	}
	validate, known := refValidators[scheme]
	if !known {
		return fmt.Errorf("scheme %q is not supported; use %s", scheme+"://", strings.Join(RefSchemes(), ", "))
	}
	return validate(body)
}

var envRefName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validateEnvRef(name string) error {
	if !envRefName.MatchString(name) {
		return fmt.Errorf("env://%s: the variable name must be letters, digits and underscores, not starting with a digit", name)
	}
	if AlwaysDeniedEnvName(name) {
		return fmt.Errorf("env://%s: this variable holds the server's own secret and can never be read by a connection", name)
	}
	return nil
}

func validateVaultRef(body string) error {
	path, key, ok := strings.Cut(body, "#")
	if !ok || strings.Trim(path, "/") == "" || key == "" {
		return fmt.Errorf("vault://%s: expected vault://path#key", body)
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("vault://%s: the path may not contain '..'", body)
	}
	if strings.ContainsAny(path, "%\\?") {
		return fmt.Errorf("vault://%s: the path may not contain '%%', '?' or a backslash", body)
	}
	return nil
}

func validateK8sRef(body string) error {
	parts := strings.Split(body, "/")
	if len(parts) != 2 && len(parts) != 3 {
		return fmt.Errorf("k8s://%s: expected k8s://[namespace/]secret/key", body)
	}
	for _, p := range parts {
		if !k8sNameRE.MatchString(p) || strings.HasPrefix(p, "-") {
			return fmt.Errorf("k8s://%s: %q is not a valid name (letters, digits, '.', '_' and '-', not starting with '-')", body, p)
		}
	}
	return nil
}
