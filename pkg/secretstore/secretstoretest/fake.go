// Package secretstoretest provides an in-memory secret-manager provider for
// tests. It records how each store was opened and every read, so a test can
// assert what identity a provider was handed and how many requests a run
// made.
package secretstoretest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Tnsor-Labs/brokoli/pkg/secretstore"
)

// Provider is a fake provider. Secrets are keyed by path; a value of type
// map[string]string is map-shaped, a string is a single value.
type Provider struct {
	NameValue string
	ShapeVal  secretstore.Shape
	Secrets   map[string]interface{}

	mu     sync.Mutex
	opened []secretstore.Identity
	reads  []string
}

// New returns a fake provider called name with the given shape.
func New(name string, shape secretstore.Shape, secrets map[string]interface{}) *Provider {
	return &Provider{NameValue: name, ShapeVal: shape, Secrets: secrets}
}

func (p *Provider) Name() string             { return p.NameValue }
func (p *Provider) Shape() secretstore.Shape { return p.ShapeVal }
func (p *Provider) AuthMethods() []secretstore.AuthMethod {
	return secretstore.AuthMethods
}

// ValidateSettings requires an "address" setting, and a "role" for oidc.
func (p *Provider) ValidateSettings(s secretstore.Settings, auth secretstore.AuthMethod, a secretstore.Settings) error {
	if s["address"] == "" {
		return errors.New("settings.address is required")
	}
	if auth == secretstore.AuthOIDC && a["role"] == "" {
		return errors.New("auth_settings.role is required for oidc")
	}
	return nil
}

func (p *Provider) Audience(s, a secretstore.Settings) string { return "fake:" + s["address"] }

func (p *Provider) Open(_ context.Context, _ secretstore.Settings, id secretstore.Identity) (secretstore.Store, error) {
	p.mu.Lock()
	p.opened = append(p.opened, id)
	p.mu.Unlock()
	if id.Method == secretstore.AuthToken && id.Token != "good-token" {
		return nil, fmt.Errorf("%w: the token was refused", secretstore.ErrPermission)
	}
	return &store{p: p}, nil
}

// Opened returns the identities stores were opened with.
func (p *Provider) Opened() []secretstore.Identity {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]secretstore.Identity(nil), p.opened...)
}

// Reads returns the paths read.
func (p *Provider) Reads() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.reads...)
}

type store struct{ p *Provider }

func (s *store) Get(_ context.Context, path, version string) (secretstore.Secret, error) {
	s.p.mu.Lock()
	s.p.reads = append(s.p.reads, path)
	v, ok := s.p.Secrets[path]
	s.p.mu.Unlock()
	if !ok {
		return secretstore.Secret{}, fmt.Errorf("%w: %s", secretstore.ErrNotFound, path)
	}
	ver := version
	if ver == "" {
		ver = "1"
	}
	switch t := v.(type) {
	case map[string]string:
		f := map[string][]byte{}
		for k, val := range t {
			f[k] = []byte(val)
		}
		return secretstore.Secret{Fields: f, Version: ver}, nil
	case string:
		return secretstore.Secret{Value: []byte(t), Version: ver}, nil
	}
	return secretstore.Secret{}, fmt.Errorf("bad fake secret at %s", path)
}

func (s *store) Close() error { return nil }
