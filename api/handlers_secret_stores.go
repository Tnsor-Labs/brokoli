package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/pkg/secretstore"
	"github.com/Tnsor-Labs/brokoli/store"
)

// SecretStoreHandler serves a workspace's secret stores (ADR-041 section
// 1 and 8). A store holds how to reach a secret manager, never a value
// from it; its token, for auth_method "token", is encrypted at rest and
// never returned.
//
// Stores are part of connection configuration, so they use the connection
// permissions: viewing, creating, editing, deleting and testing a store
// take the same permission as doing that to a connection.
type SecretStoreHandler struct {
	store    store.Store
	stores   store.SecretStoreStore
	crypto   *crypto.Config
	resolver *engine.SecretStoreResolver
}

// secretStoreView is a store as the API returns it.
type secretStoreView struct {
	models.SecretStore
	// HasCredential says a token is stored, without returning it.
	HasCredential bool `json:"has_credential"`
}

func viewOf(s models.SecretStore) secretStoreView {
	has := s.CredentialRef != ""
	s.Credential, s.CredentialRef = "", ""
	return secretStoreView{SecretStore: s, HasCredential: has}
}

// storeWorkspace is the workspace a request's stores belong to: the user's
// own workspace in an organization, else the request's.
func storeWorkspace(r *http.Request) string {
	if GetOrgIDFromRequest(r) != "" && UserWorkspaceResolverFunc != nil {
		if claims, ok := r.Context().Value("claims").(*jwt.MapClaims); ok {
			if sub, ok := (*claims)["sub"].(string); ok {
				if ws := UserWorkspaceResolverFunc(sub); len(ws) > 0 {
					return ws[0]
				}
			}
		}
	}
	return GetWorkspaceID(r)
}

func (h *SecretStoreHandler) available(w http.ResponseWriter) bool {
	if h.stores == nil || h.resolver == nil {
		writeError(w, http.StatusNotImplemented, "secret stores are not available on this server's metadata store")
		return false
	}
	return true
}

// Providers lists the providers this server offers, with their shapes and
// auth methods, for the form.
func (h *SecretStoreHandler) Providers(w http.ResponseWriter, r *http.Request) {
	out := []map[string]interface{}{}
	if h.resolver != nil {
		for _, name := range h.resolver.Providers().Names() {
			p, _ := h.resolver.Providers().Get(name)
			shape := map[secretstore.Shape]string{secretstore.ShapeString: "string", secretstore.ShapeMap: "map", secretstore.ShapeEither: "either"}[p.Shape()]
			out = append(out, map[string]interface{}{"name": name, "shape": shape, "auth_methods": p.AuthMethods()})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *SecretStoreHandler) List(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	list, err := h.stores.ListSecretStores(storeWorkspace(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]secretStoreView, 0, len(list))
	for _, s := range list {
		out = append(out, viewOf(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *SecretStoreHandler) load(w http.ResponseWriter, r *http.Request) (*models.SecretStore, bool) {
	s, err := h.stores.GetSecretStore(storeWorkspace(r), chi.URLParam(r, "storeId"))
	if errors.Is(err, store.ErrSecretStoreNotFound) {
		writeError(w, http.StatusNotFound, "secret store not found")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return s, true
}

func (h *SecretStoreHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	if s, ok := h.load(w, r); ok {
		writeJSON(w, http.StatusOK, viewOf(*s))
	}
}

// validate checks a store before it is saved, without touching the network.
func (h *SecretStoreHandler) validate(s *models.SecretStore) error {
	if !secretstore.ValidStoreName(s.Name) {
		return fmt.Errorf("name %q is invalid: lowercase letters, digits and '-', not starting or ending with '-', at most 63 characters", s.Name)
	}
	p, ok := h.resolver.Providers().Get(s.Provider)
	if !ok {
		return fmt.Errorf("provider %q is not available on this server (available: %s)", s.Provider, strings.Join(h.resolver.Providers().Names(), ", "))
	}
	method := secretstore.AuthMethod(s.AuthMethod)
	supported := false
	for _, m := range p.AuthMethods() {
		supported = supported || m == method
	}
	if !supported {
		return fmt.Errorf("auth_method %q is not supported by provider %q (supported: %v)", s.AuthMethod, s.Provider, p.AuthMethods())
	}
	if s.Settings == nil {
		s.Settings = map[string]string{}
	}
	if s.AuthSettings == nil {
		s.AuthSettings = map[string]string{}
	}
	if err := p.ValidateSettings(s.Settings, method, s.AuthSettings); err != nil {
		return err
	}
	if method == secretstore.AuthToken && s.Credential == "" && s.CredentialRef == "" {
		return errors.New(`auth_method "token" needs a credential`)
	}
	if method != secretstore.AuthToken && s.Credential != "" {
		return fmt.Errorf("a credential is stored only for auth_method \"token\", not %q", s.AuthMethod)
	}
	return nil
}

// sealCredential encrypts a submitted credential into the store's reference.
func (h *SecretStoreHandler) sealCredential(s *models.SecretStore) error {
	if secretstore.AuthMethod(s.AuthMethod) != secretstore.AuthToken {
		s.Credential, s.CredentialRef = "", ""
		return nil
	}
	if s.Credential == "" {
		return nil // unchanged
	}
	enc, err := h.crypto.Encrypt(s.Credential)
	if err != nil {
		return fmt.Errorf("encrypt the credential: %w", err)
	}
	s.Credential, s.CredentialRef = "", "encrypted://"+enc
	return nil
}

func (h *SecretStoreHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	var s models.SecretStore
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	s.ID = common.NewID()
	s.WorkspaceID = storeWorkspace(r)
	s.OrgID = GetOrgIDFromRequest(r)
	s.CreatedAt = time.Now().UTC()
	s.UpdatedAt = s.CreatedAt
	s.CredentialRef = ""
	if err := h.validate(&s); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.sealCredential(&s); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.stores.CreateSecretStore(&s); err != nil {
		if errors.Is(err, store.ErrSecretStoreNameTaken) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	AuditLog(r, "create", "secret_store", s.Name, nil, map[string]interface{}{"provider": s.Provider, "auth_method": s.AuthMethod})
	writeJSON(w, http.StatusCreated, viewOf(s))
}

func (h *SecretStoreHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	existing, ok := h.load(w, r)
	if !ok {
		return
	}
	var s models.SecretStore
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	s.ID, s.WorkspaceID, s.OrgID, s.CreatedAt = existing.ID, existing.WorkspaceID, existing.OrgID, existing.CreatedAt
	s.UpdatedAt = time.Now().UTC()
	// An omitted credential is unchanged, as long as the store still uses
	// a token and still talks to the same secret manager: a token entered
	// for one Vault must not follow the store to another address.
	sameTarget := s.Provider == existing.Provider && fmt.Sprint(s.Settings) == fmt.Sprint(existing.Settings)
	if s.Credential == "" && s.AuthMethod == existing.AuthMethod && sameTarget {
		s.CredentialRef = existing.CredentialRef
	}
	if err := h.validate(&s); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Name != existing.Name {
		if users := h.referencingConnections(existing.WorkspaceID, existing.Name); len(users) > 0 {
			writeError(w, http.StatusConflict, fmt.Sprintf("secret store %q is referenced by connection(s) %s; renaming it would break their references",
				existing.Name, strings.Join(users, ", ")))
			return
		}
	}
	if err := h.sealCredential(&s); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.stores.UpdateSecretStore(&s); err != nil {
		if errors.Is(err, store.ErrSecretStoreNameTaken) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	AuditLog(r, "update", "secret_store", s.Name, nil, map[string]interface{}{"provider": s.Provider, "auth_method": s.AuthMethod})
	writeJSON(w, http.StatusOK, viewOf(s))
}

func (h *SecretStoreHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	existing, ok := h.load(w, r)
	if !ok {
		return
	}
	if users := h.referencingConnections(existing.WorkspaceID, existing.Name); len(users) > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf("secret store %q is referenced by connection(s) %s; change them first",
			existing.Name, strings.Join(users, ", ")))
		return
	}
	if err := h.stores.DeleteSecretStore(existing.WorkspaceID, existing.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	AuditLog(r, "delete", "secret_store", existing.Name, nil, nil)
	w.WriteHeader(http.StatusNoContent)
}

// Test authenticates and reads one path the user names (ADR-041 section
// 8). It reports success, the version and, for a map, the field names it
// saw; never the value. It runs with this server's identity, which a
// worker in the customer's network may not share, and says so.
func (h *SecretStoreHandler) Test(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	s, ok := h.load(w, r)
	if !ok {
		return
	}
	var req struct {
		Path    string `json:"path"`
		Version string `json:"version"`
		Field   string `json:"field"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.Path) == "" {
		writeError(w, http.StatusBadRequest, "name a path to read")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out := map[string]interface{}{
		"note": "tested from this server, with its identity; a worker resolves with its own, which may differ",
	}
	secret, err := h.resolver.Fetch(ctx, secrets.Scope{WorkspaceID: s.WorkspaceID}, s, req.Path, req.Version)
	if err != nil {
		out["success"], out["error"] = false, err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["success"], out["version"] = true, secret.Version
	if secret.Fields != nil {
		names := make([]string, 0, len(secret.Fields))
		for n := range secret.Fields {
			names = append(names, n)
		}
		sort.Strings(names)
		out["shape"], out["fields"] = "map", names
		if req.Field != "" {
			if _, ok := secret.Fields[req.Field]; !ok {
				out["success"], out["error"] = false, fmt.Sprintf("the secret has no field %q", req.Field)
			}
		}
	} else {
		out["shape"] = "string"
		if req.Field != "" {
			out["success"], out["error"] = false, "the secret is a single value; a field cannot be selected"
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// referencingConnections lists the connections in a workspace that refer to
// store name: in password_ref, extra_ref, or any string inside extra.
func (h *SecretStoreHandler) referencingConnections(workspaceID, name string) []string {
	conns, err := h.store.ListConnectionsByWorkspace(workspaceID)
	if err != nil {
		return nil
	}
	prefix := secretstore.Scheme + "://" + name + "/"
	var out []string
	for _, c := range conns {
		extra := c.Extra
		if strings.HasPrefix(c.ExtraRef, "encrypted://") && h.crypto != nil {
			if plain, err := h.crypto.Decrypt(strings.TrimPrefix(c.ExtraRef, "encrypted://")); err == nil {
				extra = plain
			}
		}
		if strings.HasPrefix(c.PasswordRef, prefix) || strings.HasPrefix(c.ExtraRef, prefix) || strings.Contains(extra, prefix) {
			out = append(out, c.ConnID)
		}
	}
	return out
}
