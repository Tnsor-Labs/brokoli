package secrets

import (
	"context"
	"fmt"
	"strings"
)

// Resolver resolves a credential reference URI to its plaintext value.
// Implementations handle specific URI schemes (env://, encrypted://, k8s://, vault://).
type Resolver interface {
	Scheme() string
	Resolve(ctx context.Context, ref string) (string, error)
}

// Chain dispatches to the Resolver whose Scheme matches the ref prefix.
// A ref without a recognized scheme is returned as-is (backward compat
// with legacy encrypted blobs that pre-date the ref system).
type Chain struct {
	resolvers map[string]Resolver
	fallback  Resolver // handles legacy values with no scheme
}

// NewChain builds a resolver chain from the given backends.
// If a fallback is provided it handles refs that don't match any scheme.
func NewChain(fallback Resolver, backends ...Resolver) *Chain {
	m := make(map[string]Resolver, len(backends))
	for _, b := range backends {
		m[b.Scheme()] = b
	}
	return &Chain{resolvers: m, fallback: fallback}
}

// Resolve parses the scheme from ref and delegates to the matching backend.
func (c *Chain) Resolve(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}

	scheme, _, ok := parseRef(ref)
	if !ok {
		if c.fallback != nil {
			return c.fallback.Resolve(ctx, ref)
		}
		return ref, nil
	}

	r, exists := c.resolvers[scheme]
	if !exists {
		return "", fmt.Errorf("secrets: unsupported scheme %q", scheme)
	}
	return r.Resolve(ctx, ref)
}

// HasScheme returns true if the chain has a resolver for the given scheme.
func (c *Chain) HasScheme(scheme string) bool {
	_, ok := c.resolvers[scheme]
	return ok
}

// parseRef splits "scheme://body" and returns (scheme, body, true).
// Returns ("", ref, false) if no scheme is present.
func parseRef(ref string) (string, string, bool) {
	idx := strings.Index(ref, "://")
	if idx < 1 {
		return "", ref, false
	}
	return ref[:idx], ref[idx+3:], true
}
