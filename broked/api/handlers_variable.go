package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hc12r/broked/crypto"
	"github.com/hc12r/broked/models"
	"github.com/hc12r/broked/store"
)

type VariableHandler struct {
	store  store.Store
	crypto *crypto.Config
}

func NewVariableHandler(s store.Store, c *crypto.Config) *VariableHandler {
	return &VariableHandler{store: s, crypto: c}
}

func (h *VariableHandler) List(w http.ResponseWriter, r *http.Request) {
	vars, err := h.store.ListVariables()
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
	writeJSON(w, http.StatusOK, vars)
}

func (h *VariableHandler) Get(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	v, err := h.store.GetVariable(key)
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
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
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
	// Check if update (preserve created_at)
	existing, err := h.store.GetVariable(v.Key)
	if err == nil {
		v.CreatedAt = existing.CreatedAt
		// If secret and value is masked, keep the existing encrypted value
		if v.Type == models.VarTypeSecret && (v.Value == "********" || v.Value == "") {
			v.Value = existing.Value
		}
	} else {
		v.CreatedAt = now
	}
	v.UpdatedAt = now

	// Encrypt secret values
	if v.Type == models.VarTypeSecret && v.Value != "" && v.Value != "********" {
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

	// Return with masked secret
	if v.Type == models.VarTypeSecret {
		v.Value = "********"
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *VariableHandler) Delete(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if err := h.store.DeleteVariable(key); err != nil {
		writeError(w, http.StatusNotFound, "variable not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
