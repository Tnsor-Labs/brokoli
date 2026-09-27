// Package identity issues the short-lived tokens a backend exchanges for
// access to a customer's cloud or secret manager, so that nothing that opens
// the customer's resources has to be stored (ADR-041 section 4, ADR-042
// section 2).
//
// A backend that authenticates by OIDC workload identity federation asks a
// TokenSource for a JWT with the audience the customer's side expects, and
// hands it to that side's token exchange (Google STS, AWS
// AssumeRoleWithWebIdentity, Vault's JWT login). What signs the token is the
// TokenSource's business: core ships one that reads tokens a platform writes
// to disk (FileTokenSource), and a distribution can provide one that issues a
// token per run.
package identity

import (
	"context"
	"errors"
	"fmt"
)

// A TokenSource issues an OIDC token for one request.
type TokenSource interface {
	Token(ctx context.Context, req TokenRequest) (string, error)
}

// TokenRequest says which token is needed and on whose behalf.
//
// Audience is required: it is what the relying party checks, and a token
// minted for one audience must never be presented to another. The other
// fields identify the work the token is for. A source that issues a token
// per request puts them in the token's claims, so the customer can bind
// trust to a workspace and see each run in their own audit log. A source
// that only relays a machine's token cannot, which is what makes it the
// machine's identity rather than the workspace's (see Machine).
type TokenRequest struct {
	Audience string
	// WorkspaceID is the workspace the work belongs to.
	WorkspaceID string
	// SubjectKind and SubjectID name the thing the token acts for, such as
	// ("connection", <connection id>) or ("store", <secret store id>).
	// Immutable IDs, never names: renaming must not change what a
	// customer's trust configuration matches.
	SubjectKind string
	SubjectID   string
	RunID       string
	NodeID      string
}

// Validate reports whether the request can be served at all.
func (r TokenRequest) Validate() error {
	if r.Audience == "" {
		return errors.New("identity: a token request needs an audience")
	}
	return nil
}

// Machine is implemented by a TokenSource whose tokens identify the machine
// the process runs on, not the workspace the work belongs to. Such a token is
// the machine's own identity in another form, so it is governed by the same
// rule as any ambient identity (AmbientAllowed): on a server that runs work
// for several teams, it belongs to the operator.
type Machine interface {
	MachineIdentity() bool
}

// IsMachine reports whether src issues the machine's identity.
func IsMachine(src TokenSource) bool {
	m, ok := src.(Machine)
	return ok && m.MachineIdentity()
}

// ErrNoTokenSource is returned when a backend is configured for OIDC and the
// deployment has no token source.
var ErrNoTokenSource = errors.New("identity: this deployment has no OIDC token source; " +
	"configure one (BROKOLI_OIDC_TOKEN_FILES for tokens a platform writes to disk) or use another authentication method")

// Token is the one place backends get a token from: it refuses a missing
// source, an invalid request, and a machine identity where ambient identity
// is denied, before asking the source.
func Token(ctx context.Context, src TokenSource, req TokenRequest) (string, error) {
	if src == nil {
		return "", ErrNoTokenSource
	}
	if err := req.Validate(); err != nil {
		return "", err
	}
	if IsMachine(src) && !AmbientAllowed() {
		return "", fmt.Errorf("%w: the configured token source issues this machine's own identity", ErrAmbientDenied)
	}
	return src.Token(ctx, req)
}
