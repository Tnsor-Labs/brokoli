package engine

import (
	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// VarStoreAdapter wraps a Store + Crypto to implement the VariableStore interface.
type VarStoreAdapter struct {
	store  store.Store
	crypto *crypto.Config
}

// NewVarStoreAdapter creates an adapter for resolving ${var.key} at runtime.
func NewVarStoreAdapter(s store.Store, c *crypto.Config) *VarStoreAdapter {
	return &VarStoreAdapter{store: s, crypto: c}
}

// GetVariableValue resolves ${var.key} for one workspace.
//
// The workspace is a parameter rather than adapter state because this
// adapter is a process-wide singleton on the Engine, shared by every run.
// Storing a workspace on it would make the value depend on whichever run
// constructed it.
func (a *VarStoreAdapter) GetVariableValue(workspaceID, key string) (string, bool, error) {
	v, err := a.store.GetVariable(workspaceID, key)
	if err != nil {
		return "", false, err
	}

	if v.Type == models.VarTypeSecret && a.crypto != nil {
		dec, err := a.crypto.Decrypt(v.Value)
		if err != nil {
			return "", true, err
		}
		return dec, true, nil
	}
	return v.Value, false, nil
}
