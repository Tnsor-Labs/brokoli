package secrets

import (
	"context"
	"fmt"
	"strings"
)

// Scope says whose work a reference is resolved for (ADR-041 section 5):
// the workspace of the pipeline, and the run and node it is resolved
// during. The operator-level schemes (env, encrypted, k8s, vault) ignore
// it: they read with the server's own credentials, behind the operator's
// allowlists. A workspace-level scheme uses it to choose the workspace's
// store and to request a token naming the run, and refuses a reference
// resolved with no workspace.
//
// The zero Scope is "no particular workspace": what code outside a run
// (an admin tool, a connection-free configuration value) resolves with.
type Scope struct {
	WorkspaceID string
	RunID       string
	NodeID      string
}

// Resolver resolves a credential reference URI to its plaintext value.
// Implementations handle specific URI schemes (env://, encrypted://, k8s://, vault://).
type Resolver interface {
	Scheme() string
	Resolve(ctx context.Context, scope Scope, ref string) (string, error)
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

// Resolve resolves ref with no particular workspace: ResolveIn with the
// zero Scope. For callers outside a run; a run resolves with ResolveIn,
// so a workspace-level reference finds its workspace.
func (c *Chain) Resolve(ctx context.Context, ref string) (string, error) {
	return c.ResolveIn(ctx, Scope{}, ref)
}

// ResolveIn parses the scheme from ref and delegates to the matching
// backend, for the given scope.
func (c *Chain) ResolveIn(ctx context.Context, scope Scope, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}

	scheme, _, ok := parseRef(ref)
	if !ok {
		if c.fallback != nil {
			return c.fallback.Resolve(ctx, scope, ref)
		}
		return ref, nil
	}

	r, exists := c.resolvers[scheme]
	if !exists {
		return "", fmt.Errorf("secrets: unsupported scheme %q", scheme)
	}
	return r.Resolve(ctx, scope, ref)
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
