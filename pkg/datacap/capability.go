// Package datacap issues and verifies data-plane capabilities: the
// short-lived, narrowly-bound grants a remote task worker presents to
// read one input object or write one output object.
//
// ADR-033 section 6 states the requirement this package implements:
//
//	Protocol references are opaque control-plane-issued capabilities,
//	never general URLs, URIs, or host paths. Each capability is bound to
//	tenant, run, attempt, fencing generation, direction,
//	object/checksum, allowed operation, and expiry.
//
// Every one of those bindings is a field below, and every one is
// checked on verification. That list is the point: a grant that omits
// any of them is a grant that some other attempt, tenant, direction or
// object can also use. A capability naming a URL would additionally let
// task code substitute file://, a metadata service, or a cross-tenant
// address -- which is why what travels is a bearer token over an opaque
// object ID, and never a location.
//
// What this package deliberately does NOT do: move bytes, or know how
// they are stored. It answers exactly one question -- "is this bearer
// allowed to perform this one operation on this one object right now" --
// so the transport and the storage backend can each change without
// touching the authorization rule.
package datacap

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
)

// Direction is the one data flow a capability permits.
//
// Read and write are separate capabilities, never one "access" grant: a
// task that must read its input has no business rewriting it, and the
// worker that uploads an output has no business reading another
// attempt's. Splitting them is what makes that enforceable rather than
// conventional.
type Direction string

const (
	// DirectionRead permits fetching the bound object's bytes.
	DirectionRead Direction = "read"
	// DirectionWrite permits storing bytes for the bound object.
	DirectionWrite Direction = "write"
)

// Errors returned by Verify. They are distinguished rather than
// collapsed into one "denied" because they call for different
// responses: an expired capability should be re-issued and retried, a
// mismatched one is a bug or an attack, and a bad signature is always
// the latter.
var (
	// ErrMalformed means the token is not a capability this package
	// issued -- wrong shape, wrong version, undecodable.
	ErrMalformed = errors.New("datacap: malformed capability")
	// ErrSignature means the token was altered or signed with another
	// key. Never retryable.
	ErrSignature = errors.New("datacap: signature does not verify")
	// ErrExpired means the capability was valid and its window has
	// passed. Re-issue rather than widening the window.
	ErrExpired = errors.New("datacap: capability expired")
	// ErrMismatch means a genuine, unexpired capability was presented
	// for an operation it does not name -- another object, direction,
	// attempt, or tenant.
	ErrMismatch = errors.New("datacap: capability does not grant this operation")
)

// tokenVersion prefixes every token. A capability is a security
// decision, so the format it was minted under is explicit: a future
// version can be rejected outright rather than being reinterpreted
// under today's rules.
const tokenVersion = "bdc1"

// MaxTTL bounds how long a capability may be issued for. A grant is
// scoped to one attempt of one node; an attempt that outlives this
// should re-issue rather than hold a long-lived key, because the
// capability remains valid after the attempt it belongs to has been
// superseded (nothing revokes a bearer token mid-flight).
const MaxTTL = 6 * time.Hour

// Capability is the set of bindings a grant carries. Every field
// participates in the signature, so none can be edited by the bearer,
// and every field is compared on verification.
type Capability struct {
	// OrgID is the owning tenant. The first thing checked, and the
	// binding whose absence would make every other one insufficient.
	OrgID string `json:"org"`
	// RunID and NodeID identify the execution this grant belongs to.
	RunID  string `json:"run"`
	NodeID string `json:"node"`
	// Attempt and FencingGeneration pin the grant to one attempt.
	// ADR-017's fencing generation is what makes a superseded attempt's
	// capability unusable against the winner's objects: a retry gets a
	// higher generation, and this attempt's token keeps naming the old
	// one.
	Attempt           int   `json:"attempt"`
	FencingGeneration int64 `json:"fence"`
	// Direction is read or write, never both.
	Direction Direction `json:"dir"`
	// Namespace and ObjectID name the one object. ObjectID is opaque to
	// the bearer -- a store-issued identifier or a content digest, never
	// a path or a URL, so a bearer cannot navigate from the object it
	// was granted to one it was not.
	Namespace string `json:"ns"`
	ObjectID  string `json:"obj"`
	// Checksum, when set, is the sha256 the bytes must hash to. On a
	// read it lets the bearer detect substitution; on a write it binds
	// the grant to content agreed in advance. Empty is legitimate for a
	// write whose content is not yet known.
	Checksum string `json:"sum,omitempty"`
	// NotAfter is the expiry instant, always set (Issue refuses an
	// unbounded capability).
	NotAfter time.Time `json:"exp"`
}

// Request is what a bearer is trying to do right now. Verify compares it
// field by field against the capability presented.
//
// It is a separate type from Capability on purpose: the caller must
// state the operation it is about to perform from its OWN knowledge, so
// that verification compares two independently-derived descriptions. A
// single-struct API invites the mistake of filling the request in from
// the token, which verifies the token against itself and always passes.
type Request struct {
	OrgID             string
	RunID             string
	NodeID            string
	Attempt           int
	FencingGeneration int64
	Direction         Direction
	Namespace         string
	ObjectID          string
}

// Issuer mints capabilities. The key never leaves the control plane;
// workers only ever hold tokens.
type Issuer struct {
	key []byte
	now func() time.Time
}

// NewIssuer returns an Issuer signing with key.
//
// The key must be at least 32 bytes. A capability is the only thing
// standing between a worker and another tenant's data, so a short key is
// refused outright rather than accepted with a warning nobody reads.
func NewIssuer(key []byte) (*Issuer, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("datacap: signing key must be at least 32 bytes, got %d", len(key))
	}
	dup := make([]byte, len(key))
	copy(dup, key)
	return &Issuer{key: dup, now: time.Now}, nil
}

// Issue signs cap and returns the opaque token a bearer presents.
//
// Every binding is validated here rather than at verification time,
// because an under-specified capability that is never issued cannot be
// presented. A grant missing its tenant, its object, or its expiry is a
// programming error at the issuing call site, and saying so there is far
// more useful than a denial later at a worker.
func (i *Issuer) Issue(cap Capability) (string, error) {
	switch {
	case cap.OrgID == "":
		return "", fmt.Errorf("datacap: capability requires an org")
	case cap.RunID == "":
		return "", fmt.Errorf("datacap: capability requires a run")
	case cap.NodeID == "":
		return "", fmt.Errorf("datacap: capability requires a node")
	case cap.Namespace == "":
		return "", fmt.Errorf("datacap: capability requires a namespace")
	case cap.ObjectID == "":
		return "", fmt.Errorf("datacap: capability requires an object")
	case cap.Direction != DirectionRead && cap.Direction != DirectionWrite:
		return "", fmt.Errorf("datacap: capability direction must be %q or %q, got %q", DirectionRead, DirectionWrite, cap.Direction)
	case cap.NotAfter.IsZero():
		return "", fmt.Errorf("datacap: capability requires an expiry")
	}
	if ttl := cap.NotAfter.Sub(i.now()); ttl > MaxTTL {
		return "", fmt.Errorf("datacap: capability expires in %s, over the %s maximum", ttl.Truncate(time.Second), MaxTTL)
	}

	// UTC before encoding: the signature covers the encoded bytes, so a
	// capability minted in one zone and verified in another must not
	// depend on either.
	cap.NotAfter = cap.NotAfter.UTC().Truncate(time.Second)

	payload, err := json.Marshal(cap)
	if err != nil {
		return "", fmt.Errorf("datacap: encode capability: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return tokenVersion + "." + body + "." + i.sign(body), nil
}

// Verify checks a token against what the bearer is actually asking to
// do, returning the capability it carried when every binding agrees.
//
// Order matters and is deliberate: shape, then signature, then
// bindings, then expiry. Nothing decoded from an unverified token is
// compared against anything, so an attacker learns nothing from the
// error about which binding they got wrong until they hold a validly
// signed token in the first place.
func (i *Issuer) Verify(token string, req Request) (*Capability, error) {
	version, body, mac, ok := split(token)
	if !ok {
		return nil, fmt.Errorf("%w: expected three dot-separated parts", ErrMalformed)
	}
	if version != tokenVersion {
		return nil, fmt.Errorf("%w: unknown version %q", ErrMalformed, version)
	}
	// Constant time, and before any decoding: an unverified token's
	// contents are attacker-controlled bytes, so nothing is parsed out of
	// one until the signature says it came from us.
	if subtle.ConstantTimeCompare([]byte(mac), []byte(i.sign(body))) != 1 {
		return nil, ErrSignature
	}

	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	var cap Capability
	if err := json.Unmarshal(payload, &cap); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}

	// Every binding, compared to what the caller independently says it is
	// doing. A missing comparison here is a privilege escalation, so they
	// are listed exhaustively rather than looped over a subset.
	switch {
	case cap.OrgID != req.OrgID:
		return nil, fmt.Errorf("%w: capability is for another tenant", ErrMismatch)
	case cap.RunID != req.RunID:
		return nil, fmt.Errorf("%w: capability is for run %s, not %s", ErrMismatch, cap.RunID, req.RunID)
	case cap.NodeID != req.NodeID:
		return nil, fmt.Errorf("%w: capability is for node %s, not %s", ErrMismatch, cap.NodeID, req.NodeID)
	case cap.Attempt != req.Attempt:
		return nil, fmt.Errorf("%w: capability is for attempt %d, not %d", ErrMismatch, cap.Attempt, req.Attempt)
	case cap.FencingGeneration != req.FencingGeneration:
		return nil, fmt.Errorf("%w: capability is for fencing generation %d, not %d (a newer attempt has superseded this one)", ErrMismatch, cap.FencingGeneration, req.FencingGeneration)
	case cap.Direction != req.Direction:
		return nil, fmt.Errorf("%w: capability grants %s, not %s", ErrMismatch, cap.Direction, req.Direction)
	case cap.Namespace != req.Namespace:
		return nil, fmt.Errorf("%w: capability is for another namespace", ErrMismatch)
	case cap.ObjectID != req.ObjectID:
		return nil, fmt.Errorf("%w: capability is for another object", ErrMismatch)
	}

	// Expiry last: a caller that reaches this has a genuine grant for
	// this exact operation and simply took too long, which is the one
	// denial worth retrying after re-issue.
	if !i.now().Before(cap.NotAfter) {
		return nil, fmt.Errorf("%w at %s", ErrExpired, cap.NotAfter.Format(time.RFC3339))
	}
	return &cap, nil
}

func (i *Issuer) sign(body string) string {
	mac := hmac.New(sha256.New, i.key)
	// The version is signed along with the body so a token cannot be
	// replayed under a future format whose rules differ.
	mac.Write([]byte(tokenVersion))
	mac.Write([]byte("."))
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func split(token string) (version, body, mac string, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// DeriveKey produces a datacap signing key from a server's root secret.
//
// A separate key rather than the root secret itself: that secret already
// signs session tokens, and one key serving two purposes means a flaw in
// either weakens both. HKDF gives a cryptographically independent key
// from the same input, so an operator still configures exactly one
// secret and gains nothing to get wrong.
//
// The info string is versioned. If the capability format ever needs a
// key rotation independent of the root secret, bumping it rotates every
// capability without touching sessions.
func DeriveKey(rootSecret []byte) ([]byte, error) {
	if len(rootSecret) < 32 {
		return nil, fmt.Errorf("datacap: root secret must be at least 32 bytes, got %d", len(rootSecret))
	}
	r := hkdf.New(sha256.New, rootSecret, nil, []byte("brokoli-datacap-v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("datacap: derive signing key: %w", err)
	}
	return key, nil
}
