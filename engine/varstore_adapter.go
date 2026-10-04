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

	if v.Type != models.VarTypeSecret {
		return v.Value, false, nil
	}
	// A secret variable is secret however it arrives. With a key, the
	// stored value is ciphertext and is decrypted here. Without one -- a
	// worker that holds no encryption key and reads variables from its
	// control plane, which decrypts them for the job -- it arrives as the
	// value. Either way it is reported as secret, so recorded SQL and the
	// run's other records mask it. It used to be reported as not secret
	// when there was no key, which left the value unmasked.
	if a.crypto == nil {
		return v.Value, true, nil
	}
	dec, err := a.crypto.Decrypt(v.Value)
	if err != nil {
		return "", true, err
	}
	return dec, true, nil
}
