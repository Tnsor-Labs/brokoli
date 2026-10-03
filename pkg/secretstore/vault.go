package secretstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// Vault reads KV version 2 secrets from HashiCorp Vault or OpenBao, over
// Vault's HTTP API. It does not import Vault's Go client: the API it needs
// is three requests (a login, a read, a token revoke), and the client
// module would add its dependency tree to every binary for them.
//
// Settings:
//
//	address     the server, https://vault.example.com:8200 (http only for loopback)
//	namespace   optional Vault Enterprise / OpenBao namespace (X-Vault-Namespace)
//	mount       the KV v2 mount, default "secret"
//
// Auth settings:
//
//	role        the auth role, for oidc (JWT auth) and ambient (Kubernetes auth)
//	auth_mount  the auth method's mount, default "jwt" for oidc and
//	            "kubernetes" for ambient
//	audience    the OIDC audience the JWT is minted for, default "vault"
//
// Secrets are maps of fields (ShapeMap), so a reference names one:
// secret://vault-prod/warehouse/loader#password reads field "password" of
// <mount>/data/warehouse/loader.
func Vault() Provider { return vaultProvider{} }

type vaultProvider struct{}

// vaultServiceAccountJWTPath is the file holding the Kubernetes service-account
// token ambient auth presents. A variable for tests; never a setting: a
// store whose author chose the path could have the worker send any file it
// can read to an address the author also chose.
var vaultServiceAccountJWTPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// vaultMaxResponse bounds a Vault response body.
const vaultMaxResponse = 1 << 20

func (vaultProvider) Name() string { return "vault" }
func (vaultProvider) Shape() Shape { return ShapeMap }
func (vaultProvider) AuthMethods() []AuthMethod {
	return []AuthMethod{AuthAmbient, AuthOIDC, AuthToken}
}

func (vaultProvider) ValidateSettings(s Settings, auth AuthMethod, a Settings) error {
	if _, err := vaultAddress(s["address"]); err != nil {
		return err
	}
	if m := s["mount"]; m != "" && !vaultPathSegment(m) {
		return fmt.Errorf("settings.mount %q is invalid: letters, digits, '-', '_', '/', no '..'", m)
	}
	if ns := s["namespace"]; ns != "" && !vaultPathSegment(ns) {
		return fmt.Errorf("settings.namespace %q is invalid", ns)
	}
	if m := a["auth_mount"]; m != "" && !vaultPathSegment(m) {
		return fmt.Errorf("auth_settings.auth_mount %q is invalid", m)
	}
	switch auth {
	case AuthOIDC, AuthAmbient:
		if strings.TrimSpace(a["role"]) == "" {
			return fmt.Errorf("auth_settings.role is required for auth_method %q: the Vault role to log in as", auth)
		}
	}
	return nil
}

func (vaultProvider) Audience(_ Settings, a Settings) string {
	if aud := strings.TrimSpace(a["audience"]); aud != "" {
		return aud
	}
	return "vault"
}

// vaultAddress parses and checks the server address: https, or http only
// to a loopback host (a dev server); no credentials, query or fragment.
func vaultAddress(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("settings.address is required: the Vault server, https://vault.example.com:8200")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("settings.address %q is not a URL", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("settings.address %q may not carry credentials, a query or a fragment", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopbackHost(u.Hostname()) {
			return nil, fmt.Errorf("settings.address %q: plain http is allowed only for a loopback dev server; use https", raw)
		}
	default:
		return nil, fmt.Errorf("settings.address %q: the scheme must be https", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func vaultPathSegment(p string) bool {
	p = strings.Trim(p, "/")
	if p == "" || strings.Contains(p, "..") {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '/', r == '.':
		default:
			return false
		}
	}
	return true
}

func (p vaultProvider) Open(ctx context.Context, s Settings, id Identity) (Store, error) {
	// The settings were checked when the store was saved; they are checked
	// again here because a stored row is not proof of that (an older build,
	// a direct database edit), and a mount reaches the request path.
	if err := p.ValidateSettings(s, id.Method, id.Settings); err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	addr, err := vaultAddress(s["address"])
	if err != nil {
		return nil, err
	}
	c := &vaultClient{
		http:      vaultHTTPClient(addr),
		addr:      addr,
		namespace: strings.Trim(s["namespace"], "/"),
		mount:     strings.Trim(s["mount"], "/"),
	}
	if c.mount == "" {
		c.mount = "secret"
	}
	switch id.Method {
	case AuthToken:
		if id.Token == "" {
			return nil, errors.New("vault: no token to present")
		}
		c.token = id.Token
	case AuthOIDC:
		if err := c.login(ctx, authMount(id.Settings, "jwt"), id.Settings["role"], id.Token, "oidc"); err != nil {
			return nil, err
		}
		c.revoke = true
	case AuthAmbient:
		jwt, err := os.ReadFile(vaultServiceAccountJWTPath)
		if err != nil {
			return nil, fmt.Errorf("vault: ambient auth reads this machine's Kubernetes service-account token, and there is none (%v)", err)
		}
		if err := c.login(ctx, authMount(id.Settings, "kubernetes"), id.Settings["role"], strings.TrimSpace(string(jwt)), "kubernetes"); err != nil {
			return nil, err
		}
		c.revoke = true
	default:
		return nil, fmt.Errorf("vault: unsupported auth method %q", id.Method)
	}
	return c, nil
}

// vaultHTTPClient is the outbound-policy client (ADR-022) with one more
// rule: a redirect may not leave the configured server. Go copies custom
// headers such as X-Vault-Token to every redirect target, whatever its
// host, so a redirect elsewhere would hand the token to that host. Vault
// itself never needs one: an HA standby forwards requests to the active
// node rather than redirecting.
func vaultHTTPClient(addr *url.URL) *http.Client {
	c := netguard.Outbound().Client(30 * time.Second)
	policy := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != addr.Scheme || req.URL.Host != addr.Host {
			return fmt.Errorf("vault: refusing a redirect away from %s://%s", addr.Scheme, addr.Host)
		}
		return policy(req, via)
	}
	return c
}

func authMount(a Settings, def string) string {
	if m := strings.Trim(a["auth_mount"], "/"); m != "" {
		return m
	}
	return def
}

type vaultClient struct {
	http      *http.Client
	addr      *url.URL
	namespace string
	mount     string
	token     string
	// revoke: the token came from a login here, and is revoked on Close so
	// a session does not outlive the run that needed it.
	revoke bool
}

// vaultError is Vault's error body: {"errors": ["..."]}. Vault does not
// echo tokens or secret values in it.
type vaultError struct {
	Errors []string `json:"errors"`
}

func (c *vaultClient) do(ctx context.Context, method, path string, query url.Values, body interface{}) (int, []byte, error) {
	u := *c.addr
	u.Path = c.addr.Path + "/v1/" + path
	u.RawQuery = query.Encode()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, nil, err
	}
	if c.token != "" {
		req.Header.Set("X-Vault-Token", c.token)
	}
	if c.namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.namespace)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// *url.Error repeats the method and URL this message already names.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, nil, fmt.Errorf("vault: %s %s: %w", method, "/v1/"+path, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	b, err := io.ReadAll(io.LimitReader(resp.Body, vaultMaxResponse))
	return resp.StatusCode, b, err
}

const vaultNoReason = "no reason given"

// vaultReason is the server's own reason for a refusal, on one line.
// Vault wraps reasons in a multierror ("1 error occurred:\n\t* permission
// denied\n\n"); only the reasons are kept.
func vaultReason(b []byte) string {
	var e vaultError
	if json.Unmarshal(b, &e) != nil {
		return vaultNoReason
	}
	var reasons []string
	for _, msg := range e.Errors {
		for _, line := range strings.Split(msg, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
			if line == "" || strings.HasSuffix(line, "occurred:") {
				continue
			}
			reasons = append(reasons, line)
		}
	}
	if len(reasons) == 0 {
		return vaultNoReason
	}
	return strings.Join(reasons, "; ")
}

func (c *vaultClient) login(ctx context.Context, mount, role, jwt, kind string) error {
	if jwt == "" {
		return fmt.Errorf("vault: %s login has no token to present", kind)
	}
	status, b, err := c.do(ctx, http.MethodPost, "auth/"+mount+"/login", nil, map[string]string{"role": role, "jwt": jwt})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		// Vault answers a refused login with 400 (a JWT it cannot verify,
		// a claim the role does not accept, an unknown role) or 403.
		if status == http.StatusBadRequest || status == http.StatusForbidden || status == http.StatusUnauthorized {
			return fmt.Errorf("vault: %s login at auth/%s as role %q: %w (status %d: %s)", kind, mount, role, ErrPermission, status, vaultReason(b))
		}
		return fmt.Errorf("vault: %s login at auth/%s as role %q failed with status %d: %s", kind, mount, role, status, vaultReason(b))
	}
	var out struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Auth.ClientToken == "" {
		return fmt.Errorf("vault: %s login (auth/%s) returned no client token", kind, mount)
	}
	c.token = out.Auth.ClientToken
	return nil
}

// Get reads <mount>/data/<path> and returns its fields.
func (c *vaultClient) Get(ctx context.Context, path, version string) (Secret, error) {
	path = strings.Trim(path, "/")
	if path == "" || strings.Contains(path, "..") {
		return Secret{}, fmt.Errorf("vault: invalid secret path %q", path)
	}
	q := url.Values{}
	if version != "" {
		if n, err := strconv.Atoi(version); err != nil || n < 1 {
			return Secret{}, fmt.Errorf("vault: version %q is not a positive whole number", version)
		}
		q.Set("version", version)
	}
	full := c.mount + "/data/" + path
	status, b, err := c.do(ctx, http.MethodGet, full, q, nil)
	if err != nil {
		return Secret{}, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		// Also a soft-deleted or destroyed version: KV v2 answers those
		// with 404 and the version's deletion metadata.
		// A mount that does not exist is a 404 too, with a reason ("no
		// handler for route"); a missing secret has none.
		reason := vaultReason(b)
		if reason == vaultNoReason {
			reason = "no such secret, or this version is deleted"
		}
		return Secret{}, fmt.Errorf("%w: %s (vault: %s)", ErrNotFound, full, reason)
	case http.StatusForbidden:
		// Vault answers 403 both for a policy that does not grant read on
		// the path and for a token it does not recognise (expired,
		// revoked, from another server or namespace).
		return Secret{}, fmt.Errorf("%w on %s (vault: %s; the token's policies do not allow reading it, or the token is invalid or expired)", ErrPermission, full, vaultReason(b))
	default:
		return Secret{}, fmt.Errorf("vault: read %s: status %d: %s", full, status, vaultReason(b))
	}
	var out struct {
		Data struct {
			Data     map[string]interface{} `json:"data"`
			Metadata struct {
				Version int `json:"version"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return Secret{}, fmt.Errorf("vault: read %s: not a KV version 2 response", full)
	}
	if out.Data.Data == nil {
		// A deleted or destroyed version answers 200 with null data.
		return Secret{}, fmt.Errorf("%w: %s has no data at this version (deleted or destroyed)", ErrNotFound, full)
	}
	fields := make(map[string][]byte, len(out.Data.Data))
	for k, v := range out.Data.Data {
		if s, ok := v.(string); ok {
			fields[k] = []byte(s)
			continue
		}
		enc, _ := json.Marshal(v)
		fields[k] = enc
	}
	return Secret{Fields: fields, Version: strconv.Itoa(out.Data.Metadata.Version)}, nil
}

// Close revokes a token this client logged in for. Best effort.
func (c *vaultClient) Close() error {
	if !c.revoke || c.token == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = c.do(ctx, http.MethodPost, "auth/token/revoke-self", nil, nil)
	c.token = ""
	return nil
}
