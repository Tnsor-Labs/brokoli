package datacap

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func newTestIssuer(t *testing.T) *Issuer {
	t.Helper()
	i, err := NewIssuer(testKey)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return i
}

// validCapability and its matching request are the "everything agrees"
// baseline. Each denial test below mutates exactly ONE field of the
// request, so a passing test proves that field is what did the denying
// -- not some other mismatch introduced along the way.
func validCapability() Capability {
	return Capability{
		OrgID:             "org-1",
		RunID:             "run-1",
		NodeID:            "node-1",
		Attempt:           2,
		FencingGeneration: 7,
		Direction:         DirectionRead,
		Namespace:         "run-1",
		ObjectID:          "obj-abc",
		NotAfter:          time.Now().Add(time.Hour),
	}
}

func validRequest() Request {
	return Request{
		OrgID:             "org-1",
		RunID:             "run-1",
		NodeID:            "node-1",
		Attempt:           2,
		FencingGeneration: 7,
		Direction:         DirectionRead,
		Namespace:         "run-1",
		ObjectID:          "obj-abc",
	}
}

func TestRoundTrip(t *testing.T) {
	i := newTestIssuer(t)
	token, err := i.Issue(validCapability())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	got, err := i.Verify(token, validRequest())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.ObjectID != "obj-abc" || got.Direction != DirectionRead {
		t.Errorf("verified capability lost its bindings: %+v", got)
	}
}

// Every binding ADR-033 section 6 names must actually deny. A binding
// that is carried but never compared is decorative, and this table is
// what makes "bound to tenant, run, attempt, fencing generation,
// direction, object" a checkable claim rather than a comment.
func TestEveryBindingDenies(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Request)
	}{
		{"another tenant", func(r *Request) { r.OrgID = "org-2" }},
		{"another run", func(r *Request) { r.RunID = "run-2" }},
		{"another node", func(r *Request) { r.NodeID = "node-2" }},
		{"another attempt", func(r *Request) { r.Attempt = 3 }},
		{"a superseded fencing generation", func(r *Request) { r.FencingGeneration = 8 }},
		{"the opposite direction", func(r *Request) { r.Direction = DirectionWrite }},
		{"another namespace", func(r *Request) { r.Namespace = "run-2" }},
		{"another object", func(r *Request) { r.ObjectID = "obj-xyz" }},
	}
	i := newTestIssuer(t)
	token, err := i.Issue(validCapability())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validRequest()
			tc.mutate(&req)
			if _, err := i.Verify(token, req); !errors.Is(err, ErrMismatch) {
				t.Errorf("Verify accepted %s (or failed for the wrong reason): %v", tc.name, err)
			}
		})
	}
}

// A read grant must not authorize a write. Called out separately from
// the table because it is the binding most likely to be "simplified"
// into a single access grant later.
func TestReadCapabilityCannotWrite(t *testing.T) {
	i := newTestIssuer(t)
	cap := validCapability()
	cap.Direction = DirectionRead
	token, err := i.Issue(cap)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	req := validRequest()
	req.Direction = DirectionWrite
	if _, err := i.Verify(token, req); !errors.Is(err, ErrMismatch) {
		t.Fatalf("a read capability authorized a write: %v", err)
	}
}

func TestExpiredCapabilityIsRefused(t *testing.T) {
	i := newTestIssuer(t)
	cap := validCapability()
	cap.NotAfter = time.Now().Add(time.Minute)
	token, err := i.Issue(cap)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Move the issuer's clock past the expiry rather than sleeping.
	i.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := i.Verify(token, validRequest()); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired capability was accepted: %v", err)
	}
}

// The instant of expiry is already too late. Pinned because "not
// before" and "not after" differ by exactly this case, and an
// off-by-one here silently widens every grant.
func TestExpiryBoundaryIsExclusive(t *testing.T) {
	i := newTestIssuer(t)
	cap := validCapability()
	exp := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	cap.NotAfter = exp
	token, err := i.Issue(cap)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	i.now = func() time.Time { return exp }
	if _, err := i.Verify(token, validRequest()); !errors.Is(err, ErrExpired) {
		t.Fatalf("capability was still valid at its own expiry instant: %v", err)
	}
	i.now = func() time.Time { return exp.Add(-time.Nanosecond) }
	if _, err := i.Verify(token, validRequest()); err != nil {
		t.Fatalf("capability was refused a moment before expiry: %v", err)
	}
}

// A bearer must not be able to edit any binding. This is the property
// the signature exists for, so it is tested by actually rewriting the
// payload rather than by asserting a signature is present.
func TestEditedPayloadIsRefused(t *testing.T) {
	i := newTestIssuer(t)
	token, err := i.Issue(validCapability())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	forged, err := i.Issue(Capability{
		OrgID: "org-2", RunID: "run-1", NodeID: "node-1",
		Attempt: 2, FencingGeneration: 7, Direction: DirectionRead,
		Namespace: "run-1", ObjectID: "obj-abc",
		NotAfter: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Splice the other capability's body onto this token's signature:
	// the exact move a bearer holding two valid tokens would try.
	_, body, _, _ := split(forged)
	_, _, mac, _ := split(token)
	spliced := tokenVersion + "." + body + "." + mac
	if _, err := i.Verify(spliced, validRequest()); !errors.Is(err, ErrSignature) {
		t.Fatalf("a spliced payload verified: %v", err)
	}
}

func TestAnotherKeyDoesNotVerify(t *testing.T) {
	i := newTestIssuer(t)
	token, err := i.Issue(validCapability())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	other, err := NewIssuer([]byte("ffffffffffffffffffffffffffffffff"))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	if _, err := other.Verify(token, validRequest()); !errors.Is(err, ErrSignature) {
		t.Fatalf("a token verified under a different key: %v", err)
	}
}

func TestMalformedTokens(t *testing.T) {
	i := newTestIssuer(t)
	for _, token := range []string{
		"", "a", "a.b", "a.b.c.d", "..", "bdc1..sig", "bdc1.body.",
		"bdc2.body.sig", // a future version must be refused, not reinterpreted
	} {
		if _, err := i.Verify(token, validRequest()); !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrSignature) {
			t.Errorf("Verify(%q) = %v, want malformed or signature failure", token, err)
		}
	}
}

// An undecodable body must be refused only AFTER the signature check --
// otherwise the error itself tells an attacker their forgery parsed.
func TestSignatureIsCheckedBeforeDecoding(t *testing.T) {
	i := newTestIssuer(t)
	if _, err := i.Verify(tokenVersion+".!!!notbase64!!!.sig", validRequest()); !errors.Is(err, ErrSignature) {
		t.Fatalf("undecodable body reported %v; a decode error here leaks that the signature was not what rejected it", err)
	}
}

func TestIssueRequiresEveryBinding(t *testing.T) {
	i := newTestIssuer(t)
	cases := map[string]func(*Capability){
		"no org":       func(c *Capability) { c.OrgID = "" },
		"no run":       func(c *Capability) { c.RunID = "" },
		"no node":      func(c *Capability) { c.NodeID = "" },
		"no namespace": func(c *Capability) { c.Namespace = "" },
		"no object":    func(c *Capability) { c.ObjectID = "" },
		"no direction": func(c *Capability) { c.Direction = "" },
		"bad direction": func(c *Capability) {
			c.Direction = Direction("readwrite")
		},
		"no expiry": func(c *Capability) { c.NotAfter = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cap := validCapability()
			mutate(&cap)
			if _, err := i.Issue(cap); err == nil {
				t.Errorf("Issue accepted a capability with %s", name)
			}
		})
	}
}

func TestIssueRefusesUnboundedTTL(t *testing.T) {
	i := newTestIssuer(t)
	cap := validCapability()
	cap.NotAfter = time.Now().Add(MaxTTL + time.Hour)
	_, err := i.Issue(cap)
	if err == nil {
		t.Fatal("Issue accepted a capability outliving MaxTTL")
	}
	if !strings.Contains(err.Error(), "maximum") {
		t.Errorf("error does not explain the limit: %v", err)
	}
}

func TestShortKeyIsRefused(t *testing.T) {
	if _, err := NewIssuer([]byte("tooshort")); err == nil {
		t.Fatal("NewIssuer accepted a key shorter than 32 bytes")
	}
}

// The issuer must not be mutable through the slice a caller passed in.
func TestKeyIsCopied(t *testing.T) {
	key := make([]byte, 32)
	copy(key, testKey)
	i, err := NewIssuer(key)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	token, err := i.Issue(validCapability())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	for j := range key {
		key[j] = 0
	}
	if _, err := i.Verify(token, validRequest()); err != nil {
		t.Fatalf("zeroing the caller's key slice broke verification: %v", err)
	}
}

// A token minted for a write of not-yet-known content legitimately
// carries no checksum; that must not be confused with "no binding".
func TestWriteCapabilityWithoutChecksum(t *testing.T) {
	i := newTestIssuer(t)
	cap := validCapability()
	cap.Direction = DirectionWrite
	cap.Checksum = ""
	token, err := i.Issue(cap)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	req := validRequest()
	req.Direction = DirectionWrite
	got, err := i.Verify(token, req)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Checksum != "" {
		t.Errorf("checksum appeared from nowhere: %q", got.Checksum)
	}
	// ...but it is still bound to everything else.
	req.ObjectID = "obj-other"
	if _, err := i.Verify(token, req); !errors.Is(err, ErrMismatch) {
		t.Errorf("a checksum-less write capability was not object-bound: %v", err)
	}
}
