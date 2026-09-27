// Package gcpfederation turns a Brokoli OIDC token into Google Cloud
// credentials through workload identity federation, so a backend such as
// BigQuery (ADR-042) reaches a customer's project without a stored
// service-account key.
//
// Brokoli builds the external-account configuration itself, from a pool
// provider and an optional service account. It never reads one from a
// customer: an external-account file can point Google's library at any URL
// to fetch a subject token (credential_source.url) or make it run a local
// command (credential_source.executable), and neither is acceptable from
// configuration a workspace controls.
package gcpfederation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials/externalaccount"

	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// DefaultScope is the scope requested when a caller names none.
const DefaultScope = "https://www.googleapis.com/auth/cloud-platform"

const (
	subjectTokenType = "urn:ietf:params:oauth:token-type:jwt"
	impersonationFmt = "%s/v1/projects/-/serviceAccounts/%s:generateAccessToken"
	impersonationAPI = "https://iamcredentials.googleapis.com"
)

var (
	providerPattern       = regexp.MustCompile(`^//iam\.googleapis\.com/projects/[0-9]+/locations/global/workloadIdentityPools/[a-z0-9-]+/providers/[a-z0-9-]+$`)
	serviceAccountPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*@[a-z0-9][a-z0-9.-]*\.gserviceaccount\.com$`)
)

// Config names what to federate with.
type Config struct {
	// Provider is the workload identity pool provider's resource name:
	// //iam.googleapis.com/projects/<number>/locations/global/workloadIdentityPools/<pool>/providers/<provider>.
	// It is the audience of Google's token exchange.
	Provider string
	// TokenAudience is the audience of the token Brokoli presents. Empty
	// means Google's default for an OIDC provider with no allowed audiences
	// configured: the provider's name with an https: scheme.
	TokenAudience string
	// ServiceAccount, when set, is impersonated after the exchange, so the
	// customer grants roles to a service account rather than to the pool
	// principal directly. Its email address.
	ServiceAccount string

	// TokenURL and ImpersonationURL replace Google's endpoints. For tests
	// against a fake Google, and for private or sovereign endpoints; both
	// are reached through the outbound policy like everything else.
	TokenURL         string
	ImpersonationURL string
}

// Validate checks the configuration without touching the network.
func (c Config) Validate() error {
	if !providerPattern.MatchString(c.Provider) {
		return fmt.Errorf("gcpfederation: provider %q is not a workload identity pool provider name "+
			"(//iam.googleapis.com/projects/<number>/locations/global/workloadIdentityPools/<pool>/providers/<provider>)", c.Provider)
	}
	if c.ServiceAccount != "" && !serviceAccountPattern.MatchString(c.ServiceAccount) {
		return fmt.Errorf("gcpfederation: %q is not a service account email address", c.ServiceAccount)
	}
	return nil
}

func (c Config) tokenAudience() string {
	if c.TokenAudience != "" {
		return c.TokenAudience
	}
	return "https:" + c.Provider
}

// Credentials returns Google credentials that exchange a token from src for
// a Google access token on every refresh.
//
// req says whose work the token is for; its Audience is replaced with the
// one Google expects. The token is fetched through identity.Token, so a
// machine's token is refused where ambient identity is denied. client
// carries the exchange and impersonation calls; nil means the deployment's
// outbound policy.
func Credentials(src identity.TokenSource, req identity.TokenRequest, cfg Config, client *http.Client, scopes ...string) (*auth.Credentials, error) {
	if src == nil {
		return nil, identity.ErrNoTokenSource
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = netguard.Outbound().Client(30 * time.Second)
	}
	if len(scopes) == 0 {
		scopes = []string{DefaultScope}
	}
	req.Audience = cfg.tokenAudience()

	opts := &externalaccount.Options{
		Audience:             cfg.Provider,
		SubjectTokenType:     subjectTokenType,
		TokenURL:             cfg.TokenURL,
		Scopes:               scopes,
		SubjectTokenProvider: subjectTokens{src: src, req: req},
		Client:               client,
	}
	if cfg.ServiceAccount != "" {
		base := cfg.ImpersonationURL
		if base == "" {
			base = impersonationAPI
		}
		opts.ServiceAccountImpersonationURL = fmt.Sprintf(impersonationFmt, strings.TrimRight(base, "/"), cfg.ServiceAccount)
	}
	creds, err := externalaccount.NewCredentials(opts)
	if err != nil {
		return nil, fmt.Errorf("gcpfederation: %w", err)
	}
	return creds, nil
}

// subjectTokens hands Google's library a token for each exchange.
type subjectTokens struct {
	src identity.TokenSource
	req identity.TokenRequest
}

func (s subjectTokens) SubjectToken(ctx context.Context, _ *externalaccount.RequestOptions) (string, error) {
	token, err := identity.Token(ctx, s.src, s.req)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", errors.New("gcpfederation: the token source returned an empty token")
	}
	return token, nil
}
