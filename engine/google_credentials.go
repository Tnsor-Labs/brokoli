package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	googleauth "cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"

	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/identity/gcpfederation"
)

// googleAuth is how one call to a Google API authenticates, for every
// connection type that talks to Google (BigQuery, Cloud Storage): the
// connection's settings, plus, for auth_method "oidc", the deployment's
// token source and the request that says whose work the token is for.
type googleAuth struct {
	settings string
	tokens   identity.TokenSource
	request  identity.TokenRequest
}

// googleFederationEndpoints replaces Google's token exchange and
// impersonation endpoints. Tests only: a connection cannot set them, because
// whoever controls the token URL receives the token Brokoli signs (ADR-042
// section 2: Brokoli builds the federation configuration itself).
var googleFederationEndpoints struct{ tokenURL, impersonationURL string }

// googleOIDCCredentials builds Google credentials for a connection whose
// settings say auth_method "oidc", from the deployment's token source.
func googleOIDCCredentials(label string, settings map[string]interface{}, auth googleAuth, httpClient *http.Client) (*googleauth.Credentials, error) {
	provider, _ := settings["provider"].(string)
	if provider == "" {
		return nil, fmt.Errorf("%s OIDC settings require provider", label)
	}
	cfg := gcpfederation.Config{
		Provider:         provider,
		TokenURL:         googleFederationEndpoints.tokenURL,
		ImpersonationURL: googleFederationEndpoints.impersonationURL,
	}
	cfg.TokenAudience, _ = settings["token_audience"].(string)
	cfg.ServiceAccount, _ = settings["service_account"].(string)
	creds, err := gcpfederation.Credentials(auth.tokens, auth.request, cfg, httpClient)
	if err != nil {
		return nil, fmt.Errorf("%s OIDC credentials: %w", label, err)
	}
	return creds, nil
}

// googleCredentials returns the Google credentials a connection's settings
// call for, named after the connection type (label) in every message. The
// settings are the connection's extra document:
//
//   - auth_method "oidc": workload identity federation (ADR-042 section 2);
//     any other auth_method is refused by name.
//   - a "credentials" field: that service-account key.
//   - a top-level "type" field: the whole document is the key.
//   - none of these, or no settings: the machine's own identity, where
//     ambient identity is allowed.
//
// A key must be a service-account key. Anything else -- an external-account
// file can make Google's library fetch any URL or run a command -- is
// refused, never ignored. httpClient carries any token fetch.
func googleCredentials(label string, auth googleAuth, httpClient *http.Client, scopes ...string) (*googleauth.Credentials, error) {
	var raw map[string]interface{}
	if strings.TrimSpace(auth.settings) != "" {
		if err := json.Unmarshal([]byte(auth.settings), &raw); err != nil {
			return nil, fmt.Errorf("%s credentials/config are not valid JSON: %w", label, err)
		}
	}
	switch method, _ := raw["auth_method"].(string); strings.ToLower(strings.TrimSpace(method)) {
	case "oidc":
		return googleOIDCCredentials(label, raw, auth, httpClient)
	case "":
	default:
		// Refused by name: falling through would read the settings as a key
		// and fail with a message about key types instead.
		return nil, fmt.Errorf("%s auth_method %q is not supported; use \"oidc\", or omit auth_method for a service-account key", label, method)
	}

	var key []byte
	if nested, ok := raw["credentials"].(string); ok && strings.TrimSpace(nested) != "" {
		key = []byte(nested)
	} else if _, hasType := raw["type"]; hasType {
		key = []byte(auth.settings)
	}
	if key == nil {
		if !identity.AmbientAllowed() {
			return nil, fmt.Errorf("%s connection has no service-account key, and %w", label, identity.ErrAmbientDenied)
		}
		creds, err := credentials.DetectDefault(&credentials.DetectOptions{Scopes: scopes, Client: httpClient})
		if err != nil {
			return nil, fmt.Errorf("%s ambient credentials: %w", label, err)
		}
		return creds, nil
	}

	var keyType struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(key, &keyType); err != nil {
		return nil, fmt.Errorf("%s credentials are not valid JSON: %w", label, err)
	}
	if keyType.Type != "service_account" {
		return nil, fmt.Errorf("%s credential type must be service_account in this build", label)
	}
	// NewCredentialsFromJSON with an explicit type refuses any other kind of
	// credential file too, a second guard behind the check above.
	creds, err := credentials.NewCredentialsFromJSON(credentials.ServiceAccount, key,
		&credentials.DetectOptions{Scopes: scopes, Client: httpClient})
	if err != nil {
		return nil, fmt.Errorf("%s service-account key: %w", label, err)
	}
	return creds, nil
}
