package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// VaultResolver resolves vault://path#key references by reading
// from a HashiCorp Vault KV v2 engine.
//
// Ref format: vault://secret/data/my-app#password
//
// Configuration via environment:
//
//	VAULT_ADDR   — Vault server URL (required)
//	VAULT_TOKEN  — Auth token (or uses Kubernetes auth if VAULT_ROLE is set)
//	VAULT_ROLE   — K8s auth role (uses SA token at /var/run/secrets/.../token)
type VaultResolver struct {
	addr   string
	mu     sync.Mutex
	token  string
	client *http.Client
}

// NewVaultResolver creates a resolver if VAULT_ADDR is set.
// Returns nil if Vault is not configured.
func NewVaultResolver() *VaultResolver {
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		return nil
	}
	return &VaultResolver{
		addr:   strings.TrimRight(addr, "/"),
		token:  os.Getenv("VAULT_TOKEN"),
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (*VaultResolver) Scheme() string { return "vault" }

func (v *VaultResolver) Resolve(ctx context.Context, ref string) (string, error) {
	body := strings.TrimPrefix(ref, "vault://")
	parts := strings.SplitN(body, "#", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("secrets/vault: invalid ref format — expected vault://path#key")
	}
	path, key := parts[0], parts[1]

	if strings.Contains(path, "..") {
		return "", fmt.Errorf("secrets/vault: path traversal not allowed")
	}

	token, err := v.getToken(ctx)
	if err != nil {
		return "", fmt.Errorf("secrets/vault: auth failed: %w", err)
	}

	url := fmt.Sprintf("%s/v1/%s", v.addr, path)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Vault-Token", token)

	resp, err := v.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("secrets/vault: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("secrets/vault: HTTP %d for path", resp.StatusCode)
	}

	var result struct {
		Data struct {
			Data map[string]interface{} `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("secrets/vault: decode response: %w", err)
	}

	val, ok := result.Data.Data[key]
	if !ok {
		return "", fmt.Errorf("secrets/vault: key not found at requested path")
	}

	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("secrets/vault: value at requested key is not a string")
	}
	return s, nil
}

func (v *VaultResolver) getToken(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.token != "" {
		return v.token, nil
	}

	role := os.Getenv("VAULT_ROLE")
	if role == "" {
		return "", fmt.Errorf("no VAULT_TOKEN or VAULT_ROLE configured")
	}

	saToken, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return "", fmt.Errorf("read SA token: %w", err)
	}

	payload, err := json.Marshal(map[string]string{
		"role": role,
		"jwt":  string(saToken),
	})
	if err != nil {
		return "", fmt.Errorf("marshal auth payload: %w", err)
	}

	url := fmt.Sprintf("%s/v1/auth/kubernetes/login", v.addr)
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("vault k8s auth failed with HTTP %d", resp.StatusCode)
	}

	var authResp struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return "", err
	}

	v.token = authResp.Auth.ClientToken
	return v.token, nil
}
