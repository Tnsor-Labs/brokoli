// Package secretstore reads credentials from a customer's secret manager
// (ADR-041). A secret store is a named, workspace-scoped connection to one
// secret manager; a credential anywhere in a connection can be a reference
// into it, secret://<store>/<path>[?version=<v>][#<field>], fetched on the
// machine that runs the node, when the node runs, and never written down.
//
// A Provider knows one secret manager's API and nothing else: its settings,
// one login mapping, and one read. Authentication is not the provider's
// business. The caller decides how a store authenticates (Identity) and
// hands the provider what it needs.
package secretstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Settings are a store's non-secret provider settings: a Vault address and
// mount, an AWS region. Strings only, so they are stored and shown as is.
type Settings map[string]string

// AuthMethod is how a store authenticates (ADR-041 section 4).
type AuthMethod string

const (
	// AuthAmbient uses the identity of the machine that runs the node: the
	// AWS default chain, a Kubernetes service account. Refused where the
	// operator denies ambient identity (BROKOLI_SECRET_STORE_AMBIENT=deny).
	AuthAmbient AuthMethod = "ambient"
	// AuthOIDC exchanges a short-lived token from the deployment's token
	// source, naming the store and the run, for the provider's session.
	AuthOIDC AuthMethod = "oidc"
	// AuthToken presents a static token stored, encrypted, on the store.
	// The least safe method, and never the default.
	AuthToken AuthMethod = "token"
)

// AuthMethods lists the methods, for messages.
var AuthMethods = []AuthMethod{AuthAmbient, AuthOIDC, AuthToken}

// Identity is what a provider authenticates with, decided by the caller.
type Identity struct {
	Method AuthMethod
	// Settings are the method's non-secret settings: a Vault role, an AWS
	// role ARN, an audience.
	Settings Settings
	// Token is the OIDC token to exchange (AuthOIDC) or the static token to
	// present (AuthToken). Empty for AuthAmbient.
	Token string
	// Session names the work the read is for -- the store, the run and the
	// node -- for a provider that records a session name in the secret
	// manager's own audit log (AWS's RoleSessionName). Identifiers only,
	// never a secret.
	Session Session
}

// Session identifies whose work a read is for.
type Session struct {
	StoreID string
	RunID   string
	NodeID  string
}

// Shape is the shape of the secrets a provider returns.
type Shape int

const (
	// ShapeString: every secret is one string (SSM, Azure Key Vault). A
	// #field is refused.
	ShapeString Shape = iota
	// ShapeMap: every secret is a map of named fields (Vault KV). A #field
	// is required.
	ShapeMap
	// ShapeEither: a secret may be either (AWS Secrets Manager, where a
	// secret holds a string or a JSON object). Decided per secret.
	ShapeEither
)

// Secret is a single string or a map of named fields, never both.
type Secret struct {
	Value   []byte
	Fields  map[string][]byte
	Version string
}

// A Store reads secrets from one configured secret manager.
type Store interface {
	Get(ctx context.Context, path, version string) (Secret, error)
	Close() error
}

// A Provider knows one secret manager's API.
type Provider interface {
	// Name is the provider value a store names: "vault", "aws_ssm", ...
	Name() string
	// Shape says whether its secrets are strings, maps, or either.
	Shape() Shape
	// AuthMethods are the methods it supports.
	AuthMethods() []AuthMethod
	// ValidateSettings checks a store's settings and its auth settings when
	// it is saved. It does not touch the network.
	ValidateSettings(s Settings, auth AuthMethod, authSettings Settings) error
	// Audience is the OIDC audience a token for this store must carry,
	// from its settings. Only called for AuthOIDC.
	Audience(s Settings, authSettings Settings) string
	// Open returns a client for one store, authenticated with id. The
	// HTTP client a provider makes requests with must go through the
	// outbound policy (ADR-022).
	Open(ctx context.Context, s Settings, id Identity) (Store, error)
}

// ErrNotFound is returned by a Store when the secret does not exist.
var ErrNotFound = errors.New("secret not found")

// ErrPermission is returned by a Store when the identity may not read it.
var ErrPermission = errors.New("permission denied")

// Registry holds the providers a deployment offers. Core builds one with
// its own; a distribution appends its own (extensions.Registry).
type Registry struct {
	providers map[string]Provider
}

// NewRegistry returns a registry holding providers. A later provider with
// the same name replaces an earlier one.
func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{providers: map[string]Provider{}}
	for _, p := range providers {
		r.Add(p)
	}
	return r
}

// Add registers p.
func (r *Registry) Add(p Provider) {
	if p != nil {
		r.providers[p.Name()] = p
	}
}

// Get returns the provider named name.
func (r *Registry) Get(name string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.providers[name]
	return p, ok
}

// Names lists the registered providers, sorted.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Scheme is the reference scheme.
const Scheme = "secret"

// Ref is a parsed secret://<store>/<path>[?version=<v>][#<field>].
type Ref struct {
	Store   string
	Path    string
	Version string
	Field   string
}

func (r Ref) String() string {
	s := Scheme + "://" + r.Store + "/" + r.Path
	if r.Version != "" {
		s += "?version=" + url.QueryEscape(r.Version)
	}
	if r.Field != "" {
		s += "#" + r.Field
	}
	return s
}

var storeName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidStoreName reports whether name is a valid store name: lowercase
// letters, digits and '-', not starting or ending with '-', at most 63.
func ValidStoreName(name string) bool { return storeName.MatchString(name) }

// IsRef reports whether v is a secret:// reference. A value is a reference
// only when it starts with the scheme (ADR-041 section 2).
func IsRef(v string) bool { return strings.HasPrefix(v, Scheme+"://") }

// ParseRef parses a secret:// reference. The path is kept as the provider
// spells it, so it can be copied from the secret manager's console.
func ParseRef(ref string) (Ref, error) {
	if !IsRef(ref) {
		return Ref{}, fmt.Errorf("%q is not a secret:// reference", ref)
	}
	body := strings.TrimPrefix(ref, Scheme+"://")
	var r Ref
	if i := strings.IndexByte(body, '#'); i >= 0 {
		r.Field, body = body[i+1:], body[:i]
		if r.Field == "" {
			return Ref{}, fmt.Errorf("%s: an empty #field; remove the '#' or name the field", ref)
		}
	}
	if i := strings.IndexByte(body, '?'); i >= 0 {
		q, err := url.ParseQuery(body[i+1:])
		if err != nil {
			return Ref{}, fmt.Errorf("%s: %v", ref, err)
		}
		for k := range q {
			if k != "version" {
				return Ref{}, fmt.Errorf("%s: unknown option %q (only ?version= is supported)", ref, k)
			}
		}
		r.Version, body = q.Get("version"), body[:i]
	}
	store, path, ok := strings.Cut(body, "/")
	if !ok || strings.Trim(path, "/") == "" {
		return Ref{}, fmt.Errorf("%s: expected secret://<store>/<path>", ref)
	}
	if !ValidStoreName(store) {
		return Ref{}, fmt.Errorf("%s: %q is not a valid store name (lowercase letters, digits and '-')", ref, store)
	}
	if strings.Contains(path, "..") {
		return Ref{}, fmt.Errorf("%s: the path may not contain '..'", ref)
	}
	r.Store, r.Path = store, path
	return r, nil
}

// CheckShape reports whether a reference's #field suits the provider's
// shape, before anything is fetched: required for map-shaped secrets,
// refused for string-shaped ones.
func CheckShape(r Ref, shape Shape) error {
	switch {
	case shape == ShapeMap && r.Field == "":
		return fmt.Errorf("%s: this provider's secrets are maps of fields; name one with #<field>", r)
	case shape == ShapeString && r.Field != "":
		return fmt.Errorf("%s: this provider's secrets are single values; remove #%s", r, r.Field)
	}
	return nil
}

// Pick returns the value a reference selects from a fetched secret, under
// the same rule as CheckShape, so a reference never silently yields a
// whole document where a password was expected.
func Pick(r Ref, s Secret) ([]byte, error) {
	if s.Fields != nil {
		if r.Field == "" {
			names := make([]string, 0, len(s.Fields))
			for n := range s.Fields {
				names = append(names, n)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("%s: the secret has fields (%s); name one with #<field>", r, strings.Join(names, ", "))
		}
		v, ok := s.Fields[r.Field]
		if !ok {
			return nil, fmt.Errorf("%s: the secret has no field %q", r, r.Field)
		}
		return v, nil
	}
	if r.Field != "" {
		return nil, fmt.Errorf("%s: the secret is a single value; remove #%s", r, r.Field)
	}
	return s.Value, nil
}
