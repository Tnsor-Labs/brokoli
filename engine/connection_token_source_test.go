package engine

import (
	"context"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/identity"
)

type stubTokenSource struct{}

func (stubTokenSource) Token(context.Context, identity.TokenRequest) (string, error) { return "t", nil }

// A backend authenticating by OIDC reaches the deployment's token source
// through the connection resolver. A nil source, including a typed nil a
// constructor returned, reads back as none rather than as a source whose
// every call panics.
func TestConnectionResolverCarriesTheTokenSource(t *testing.T) {
	cr := NewConnectionResolver(nil, nil)
	if cr.TokenSource() != nil {
		t.Fatal("a new resolver has a token source")
	}

	var none *identity.FileTokenSource
	cr.SetTokenSource(none)
	if cr.TokenSource() != nil {
		t.Fatal("a typed nil source was stored as a source")
	}

	cr.SetTokenSource(stubTokenSource{})
	if _, ok := cr.TokenSource().(stubTokenSource); !ok {
		t.Fatalf("TokenSource() = %T, want the source that was set", cr.TokenSource())
	}
	cr.SetTokenSource(nil)
	if cr.TokenSource() != nil {
		t.Fatal("setting nil did not clear the source")
	}

	var noResolver *ConnectionResolver
	if noResolver.TokenSource() != nil {
		t.Fatal("a nil resolver reported a token source")
	}
}
