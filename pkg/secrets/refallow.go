package secrets

import (
	"os"
	"strings"
)

// vault:// and k8s:// references read from stores the server can reach
// with its own credentials: a Vault token and a Kubernetes service
// account. Those reach far more than any one connection should, the
// server's own secrets included, and a connection is something a
// workspace editor can create and point at a server of their choosing.
// So, like env:// (envallow.go), each resolves only what the operator has
// listed.

// VaultRefAllowEnv names the allowlist for vault:// references:
// comma-separated path prefixes, matched on whole path segments.
const VaultRefAllowEnv = "BROKOLI_SECRET_VAULT_ALLOW"

// K8sRefAllowEnv names the allowlist for k8s:// references:
// comma-separated namespace/secret names.
const K8sRefAllowEnv = "BROKOLI_SECRET_K8S_ALLOW"

// VaultRefAllowed reports whether a vault:// reference may read path.
// "secret/data/brokoli" allows "secret/data/brokoli/warehouse" but not
// "secret/data/brokoli-old/warehouse".
func VaultRefAllowed(path string) bool {
	path = strings.Trim(path, "/")
	if path == "" {
		return false
	}
	for _, p := range strings.Split(os.Getenv(VaultRefAllowEnv), ",") {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p != "" && (path == p || strings.HasPrefix(path, p+"/")) {
			return true
		}
	}
	return false
}

// K8sRefAllowed reports whether a k8s:// reference may read this secret.
func K8sRefAllowed(namespace, secret string) bool {
	want := namespace + "/" + secret
	for _, a := range strings.Split(os.Getenv(K8sRefAllowEnv), ",") {
		if strings.TrimSpace(a) == want {
			return true
		}
	}
	return false
}
