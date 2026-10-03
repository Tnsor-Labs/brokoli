package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/pkg/secretstore"
	"github.com/Tnsor-Labs/brokoli/store"
)

// SecretStoreResolver resolves secret://<store>/<path>[?version=][#field]
// references (ADR-041 sections 2-5) as one scheme of the secrets chain.
//
// It resolves only in a workspace: the store is looked up by name among
// the stores of the scope's workspace, never across workspaces. It runs on
// the machine that resolves the connection, which for a run is the machine
// that runs the node, so a worker inside the customer's network reads the
// customer's store with its own identity and the value never crosses the
// server.
//
// Values are cached for the lifetime of one run, keyed by store, path and
// version, so forty nodes on one connection make one request; the cache
// is dropped when the run ends (dropRunSecretState). A value is never
// written anywhere, and every value it returns for a run joins the run's
// redaction set through the connection resolver.
type SecretStoreResolver struct {
	stores    store.SecretStoreStore
	providers *secretstore.Registry
	// creds resolves a store's own credential reference (encrypted://) for
	// auth_method "token".
	creds  *secrets.Chain
	mu     sync.RWMutex
	tokens identity.TokenSource
}

// NewSecretStoreResolver returns a resolver over the stores in st, for the
// providers in reg. creds resolves stored store credentials.
func NewSecretStoreResolver(st store.SecretStoreStore, reg *secretstore.Registry, creds *secrets.Chain) *SecretStoreResolver {
	return &SecretStoreResolver{stores: st, providers: reg, creds: creds}
}

// SetTokenSource sets the token source auth_method "oidc" uses.
func (r *SecretStoreResolver) SetTokenSource(src identity.TokenSource) {
	r.mu.Lock()
	r.tokens = src
	r.mu.Unlock()
}

func (r *SecretStoreResolver) tokenSource() identity.TokenSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tokens
}

// Providers returns the provider registry.
func (r *SecretStoreResolver) Providers() *secretstore.Registry { return r.providers }

// Scheme implements secrets.Resolver.
func (*SecretStoreResolver) Scheme() string { return secretstore.Scheme }

// runSecretCache holds the values one run has fetched.
var runSecretCaches sync.Map // run ID -> *sync.Map (cache key -> string)

// dropRunSecretState forgets everything a run resolved: its redaction set
// and its fetched values.
func dropRunSecretState(runID string) {
	dropRunRedactions(runID)
	if runID != "" {
		runSecretCaches.Delete(runID)
	}
}

// Resolve implements secrets.Resolver.
func (r *SecretStoreResolver) Resolve(ctx context.Context, scope secrets.Scope, raw string) (string, error) {
	if scope.WorkspaceID == "" {
		return "", fmt.Errorf("%s: a secret:// reference resolves only for a workspace's run or connection test, and none was given", raw)
	}
	ref, err := secretstore.ParseRef(raw)
	if err != nil {
		return "", err
	}
	st, err := r.stores.GetSecretStoreByName(scope.WorkspaceID, ref.Store)
	if errors.Is(err, store.ErrSecretStoreNotFound) {
		return "", fmt.Errorf("%s: no secret store named %q in this workspace", raw, ref.Store)
	}
	if err != nil {
		return "", fmt.Errorf("%s: look up secret store %q: %w", raw, ref.Store, err)
	}
	provider, ok := r.providers.Get(st.Provider)
	if !ok {
		return "", fmt.Errorf("%s: secret store %q uses provider %q, which this build does not have", raw, st.Name, st.Provider)
	}
	if err := secretstore.CheckShape(ref, provider.Shape()); err != nil {
		return "", err
	}

	var cache *sync.Map
	key := st.ID + "\x00" + ref.Path + "\x00" + ref.Version + "\x00" + ref.Field
	if scope.RunID != "" {
		v, _ := runSecretCaches.LoadOrStore(scope.RunID, &sync.Map{})
		cache = v.(*sync.Map)
		if hit, ok := cache.Load(key); ok {
			return hit.(string), nil
		}
	}

	value, err := r.fetch(ctx, scope, st, provider, ref)
	if err != nil {
		return "", fmt.Errorf("%s: %w", raw, err)
	}
	if cache != nil {
		cache.Store(key, value)
	}
	return value, nil
}

// Fetch reads one reference for a store's test, without the run cache.
// It returns the secret's version and, for a map-shaped secret read
// without a field, its field names; never the value.
func (r *SecretStoreResolver) Fetch(ctx context.Context, scope secrets.Scope, st *models.SecretStore, path, version string) (secretstore.Secret, error) {
	provider, ok := r.providers.Get(st.Provider)
	if !ok {
		return secretstore.Secret{}, fmt.Errorf("provider %q is not available in this build", st.Provider)
	}
	id, err := r.identityFor(ctx, scope, st, provider)
	if err != nil {
		return secretstore.Secret{}, err
	}
	client, err := provider.Open(ctx, st.Settings, id)
	if err != nil {
		return secretstore.Secret{}, fmt.Errorf("secret store %q (%s): %w", st.Name, st.Provider, err)
	}
	defer client.Close() //nolint:errcheck
	secret, err := client.Get(ctx, path, version)
	if err != nil {
		return secretstore.Secret{}, fmt.Errorf("secret store %q (%s): %w", st.Name, st.Provider, err)
	}
	return secret, nil
}

func (r *SecretStoreResolver) fetch(ctx context.Context, scope secrets.Scope, st *models.SecretStore, provider secretstore.Provider, ref secretstore.Ref) (string, error) {
	secret, err := r.Fetch(ctx, scope, st, ref.Path, ref.Version)
	if err != nil {
		return "", err
	}
	value, err := secretstore.Pick(ref, secret)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

// identityFor builds what the provider authenticates with (ADR-041
// section 4).
func (r *SecretStoreResolver) identityFor(ctx context.Context, scope secrets.Scope, st *models.SecretStore, provider secretstore.Provider) (secretstore.Identity, error) {
	id := secretstore.Identity{Method: secretstore.AuthMethod(st.AuthMethod), Settings: st.AuthSettings,
		Session: secretstore.Session{StoreID: st.ID, RunID: scope.RunID, NodeID: scope.NodeID}}
	switch id.Method {
	case secretstore.AuthAmbient:
		// Refused by name where the operator denies it, rather than
		// letting the cloud answer with the operator's own permissions or
		// a permission error that hides the reason.
		if !identity.AmbientAllowed() {
			return id, fmt.Errorf("secret store %q uses the machine's own identity, and %w", st.Name, identity.ErrAmbientDenied)
		}
	case secretstore.AuthOIDC:
		tok, err := identity.Token(ctx, r.tokenSource(), identity.TokenRequest{
			Audience:    provider.Audience(st.Settings, st.AuthSettings),
			WorkspaceID: scope.WorkspaceID,
			SubjectKind: "store",
			SubjectID:   st.ID,
			RunID:       scope.RunID,
			NodeID:      scope.NodeID,
		})
		if err != nil {
			return id, fmt.Errorf("secret store %q: %w", st.Name, err)
		}
		id.Token = tok
	case secretstore.AuthToken:
		if st.CredentialRef == "" {
			return id, fmt.Errorf("secret store %q uses a stored token, and none is stored", st.Name)
		}
		if r.creds == nil {
			return id, fmt.Errorf("secret store %q uses a stored token, and this machine cannot decrypt it", st.Name)
		}
		tok, err := r.creds.ResolveIn(ctx, scope, st.CredentialRef)
		if err != nil {
			return id, fmt.Errorf("secret store %q: could not read its stored token: %w", st.Name, err)
		}
		id.Token = tok
	default:
		return id, fmt.Errorf("secret store %q has unknown auth method %q", st.Name, st.AuthMethod)
	}
	return id, nil
}
