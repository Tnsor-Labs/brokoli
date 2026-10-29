package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

type VariableHandler struct {
	store  store.Store
	crypto *crypto.Config
}

func NewVariableHandler(s store.Store, c *crypto.Config) *VariableHandler {
	return &VariableHandler{store: s, crypto: c}
}

// variableWorkspace returns the workspace this caller may reach the key
// in, and whether they may reach it at all.
//
// The access check already had to find that workspace in order to answer
// yes or no; it just threw it away, and the store was then read by key
// alone -- which returned whichever workspace had written that name last.
// Returning it is what lets the read be scoped to the same workspace the
// permission was granted for.
//
// An empty workspace with ok=true means single-tenant operation, where
// the store's own "default" applies.
func (h *VariableHandler) variableWorkspace(r *http.Request, key string) (string, bool) {
	orgID := GetOrgIDFromRequest(r)
	if orgID == "" {
		return "", true
	}
	var userWSIDs []string
	if UserWorkspaceResolverFunc != nil {
		if claims, ok := r.Context().Value("claims").(*jwt.MapClaims); ok {
			if sub, ok := (*claims)["sub"].(string); ok {
				userWSIDs = UserWorkspaceResolverFunc(sub)
			}
		}
	}
	if len(userWSIDs) == 0 {
		return "", false
	}
	for _, wsID := range userWSIDs {
		vars, _ := h.store.ListVariablesByWorkspace(wsID)
		for _, v := range vars {
			if v.Key == key {
				return wsID, true
			}
		}
	}
	return "", false
}

func (h *VariableHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := GetOrgIDFromRequest(r)
	wsID := GetWorkspaceID(r)
	if orgID != "" && wsID == "default" && UserWorkspaceResolverFunc != nil {
		if claims, ok := r.Context().Value("claims").(*jwt.MapClaims); ok {
			if sub, ok := (*claims)["sub"].(string); ok {
				userWS := UserWorkspaceResolverFunc(sub)
				if len(userWS) > 0 {
					wsID = userWS[0]
				} else {
					writeJSON(w, http.StatusOK, []models.Variable{})
					return
				}
			}
		}
	}
	vars, err := h.store.ListVariablesByWorkspace(wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if vars == nil {
		vars = []models.Variable{}
	}
	// Mask secret values, decrypt them first to check type
	for i := range vars {
		if vars[i].Type == models.VarTypeSecret {
			// Decrypt to verify it's valid, but return masked
			dec, err := h.crypto.Decrypt(vars[i].Value)
			if err == nil && dec != "" {
				vars[i].Value = "********"
			} else {
				vars[i].Value = "********"
			}
		}
	}

	// Paginated response when ?page= is set
	if r.URL.Query().Get("page") != "" {
		pp := ParsePageParams(r)
		total := len(vars)
		start := pp.Offset()
		end := start + pp.Limit()
		if start > total {
			start = total
		}
		if end > total {
			end = total
		}
		writeJSON(w, http.StatusOK, PaginateSlice(vars[start:end], total, pp))
		return
	}

	writeJSON(w, http.StatusOK, vars)
}

func (h *VariableHandler) Get(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	wsID, ok := h.variableWorkspace(r, key)
	if !ok {
		writeError(w, http.StatusNotFound, "variable not found")
		return
	}
	v, err := h.store.GetVariable(wsID, key)
	if err != nil {
		writeError(w, http.StatusNotFound, "variable not found")
		return
	}
	if v.Type == models.VarTypeSecret {
		v.Value = "********"
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *VariableHandler) Set(w http.ResponseWriter, r *http.Request) {
	var v models.Variable
	if err := decodeJSON(w, r, &v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// PUT /variables/{key} names the variable in its path. The body used to
	// win silently, so a request authorized for one key wrote another.
	if urlKey := chi.URLParam(r, "key"); urlKey != "" {
		if v.Key == "" {
			v.Key = urlKey
		} else if v.Key != urlKey {
			writeError(w, http.StatusBadRequest, "key in the body does not match the key in the path")
			return
		}
	}
	if v.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	// Validate key format (alphanumeric, dots, underscores, hyphens)
	v.Key = strings.TrimSpace(v.Key)
	if v.Type == "" {
		v.Type = models.VarTypeString
	}

	now := time.Now()

	ws, ok := writeWorkspace(r, v.WorkspaceID)
	if !ok {
		writeError(w, http.StatusForbidden, "workspace_id must be the workspace this request acts in")
		return
	}
	v.WorkspaceID = ws

	// Check if update (preserve created_at)
	//
	// keptCiphertext records that the value below is the stored one,
	// which is ALREADY encrypted. Without it the encrypt step cannot tell
	// restored ciphertext from a freshly typed secret -- neither is empty
	// and neither is the mask -- so saving a secret without re-entering
	// its value encrypted the ciphertext a second time. A pipeline then
	// decrypted once and got ciphertext, and the only way to recover was
	// to type the secret again.
	keptCiphertext := false
	existing, err := h.store.GetVariable(v.WorkspaceID, v.Key)
	if err == nil {
		v.CreatedAt = existing.CreatedAt
		// If secret and value is masked, keep the existing encrypted value
		if v.Type == models.VarTypeSecret && (v.Value == "********" || v.Value == "") {
			v.Value = existing.Value
			keptCiphertext = true
		}
	} else {
		v.CreatedAt = now
	}
	v.UpdatedAt = now

	// Encrypt secret values
	if v.Type == models.VarTypeSecret && !keptCiphertext && v.Value != "" && v.Value != "********" {
		enc, err := h.crypto.Encrypt(v.Value)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		v.Value = enc
	}

	if err := h.store.SetVariable(&v); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	AuditLog(r, "set", "variable", v.Key, nil, map[string]interface{}{"type": string(v.Type)})

	// Return with masked secret
	if v.Type == models.VarTypeSecret {
		v.Value = "********"
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *VariableHandler) Delete(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	wsID, ok := h.variableWorkspace(r, key)
	if !ok {
		writeError(w, http.StatusNotFound, "variable not found")
		return
	}
	if err := h.store.DeleteVariable(wsID, key); err != nil {
		writeError(w, http.StatusNotFound, "variable not found")
		return
	}
	AuditLog(r, "delete", "variable", key, nil, nil)
	w.WriteHeader(http.StatusNoContent)
}
