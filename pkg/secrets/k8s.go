package secrets

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var k8sNameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// K8sResolver resolves k8s://namespace/secret-name/key references
// by reading the Kubernetes Secret via kubectl. The worker's
// ServiceAccount must have RBAC read access to the target Secret.
//
// Ref format: k8s://[namespace/]secret-name/key
// If namespace is omitted, BROKOLI_K8S_NAMESPACE or "default" is used.
//
// For multi-tenant safety, only secrets in AllowedNamespaces can be
// read. When AllowedNamespaces is empty, only the default namespace
// is permitted.
type K8sResolver struct {
	defaultNS         string
	kubeconfig        string
	AllowedNamespaces map[string]bool
}

// NewK8sResolver creates a resolver that reads Kubernetes Secrets.
// By default, only the pod's own namespace is allowed.
func NewK8sResolver() *K8sResolver {
	ns := os.Getenv("BROKOLI_K8S_NAMESPACE")
	if ns == "" {
		ns = "default"
	}
	return &K8sResolver{
		defaultNS:         ns,
		kubeconfig:        os.Getenv("KUBECONFIG"),
		AllowedNamespaces: map[string]bool{ns: true},
	}
}

func (*K8sResolver) Scheme() string { return "k8s" }

func (k *K8sResolver) Resolve(ctx context.Context, ref string) (string, error) {
	body := strings.TrimPrefix(ref, "k8s://")
	parts := strings.Split(body, "/")

	var namespace, secretName, key string
	switch len(parts) {
	case 2:
		namespace = k.defaultNS
		secretName = parts[0]
		key = parts[1]
	case 3:
		namespace = parts[0]
		secretName = parts[1]
		key = parts[2]
	default:
		return "", fmt.Errorf("secrets/k8s: invalid ref format — expected k8s://[namespace/]secret/key")
	}

	if !k8sNameRE.MatchString(namespace) || !k8sNameRE.MatchString(secretName) || !k8sNameRE.MatchString(key) {
		return "", fmt.Errorf("secrets/k8s: invalid characters in ref components")
	}

	if len(k.AllowedNamespaces) > 0 && !k.AllowedNamespaces[namespace] {
		return "", fmt.Errorf("secrets/k8s: namespace %q not in allowed list", namespace)
	}

	args := []string{
		"get", "secret", secretName,
		"-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.data.%s}", key),
	}
	if k.kubeconfig != "" {
		args = append([]string{"--kubeconfig", k.kubeconfig}, args...)
	}

	cmd := exec.CommandContext(ctx, "kubectl", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("secrets/k8s: kubectl failed: %s", string(exitErr.Stderr))
		}
		return "", fmt.Errorf("secrets/k8s: kubectl failed: %w", err)
	}

	encoded := strings.TrimSpace(string(out))
	if encoded == "" {
		return "", fmt.Errorf("secrets/k8s: key not found in secret %s/%s", namespace, secretName)
	}

	decoded, err := base64Decode(encoded)
	if err != nil {
		return "", fmt.Errorf("secrets/k8s: base64 decode failed: %w", err)
	}

	return decoded, nil
}
