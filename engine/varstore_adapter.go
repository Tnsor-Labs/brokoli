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

func (a *VarStoreAdapter) GetVariableValue(key string) (string, bool, error) {
	v, err := a.store.GetVariable(key)
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
