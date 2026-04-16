package secrets

import (
	"log"

	"github.com/Tnsor-Labs/brokoli/crypto"
)

// NewDefaultChain builds a Chain with all backends that are available
// in the current environment. The EncryptedResolver is always registered
// (as both the "encrypted" scheme and the fallback for legacy bare
// ciphertexts). Vault and K8s resolvers are only added if their
// required env vars are present.
func NewDefaultChain(cr *crypto.Config) *Chain {
	var enc *EncryptedResolver
	if cr != nil {
		enc = NewEncryptedResolver(cr)
	}

	backends := []Resolver{
		EnvResolver{},
	}
	schemes := []string{"env"}

	if enc != nil {
		backends = append(backends, enc)
		schemes = append(schemes, "encrypted")
	}

	if vr := NewVaultResolver(); vr != nil {
		backends = append(backends, vr)
		schemes = append(schemes, "vault")
	}

	k8s := NewK8sResolver()
	backends = append(backends, k8s)
	schemes = append(schemes, "k8s")

	var fallback Resolver
	if enc != nil {
		fallback = enc
	}

	log.Printf("[secrets] Resolver chain: %v", schemes)

	return NewChain(fallback, backends...)
}
