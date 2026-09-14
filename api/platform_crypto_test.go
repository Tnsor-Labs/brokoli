package api

import (
	"context"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/go-chi/chi/v5"
)

// The crypto config reaches the platform provider (#244).
//
// An enterprise build that stores a secret of its own must encrypt it
// with the same key core uses. The alternative is re-deriving the key
// from the environment, and two components resolving a key
// independently is how they end up disagreeing -- which surfaces as a
// decryption failure long after the write, on data that is now
// unreadable.

type recordingPlatform struct {
	args []interface{}
}

func (p *recordingPlatform) Enabled() bool { return true }
func (p *recordingPlatform) RegisterRoutes(_, _, _ interface{}, extra ...interface{}) {
	p.args = extra
}
func (p *recordingPlatform) StartServices(interface{}) {}
func (p *recordingPlatform) StopServices()             {}
func (p *recordingPlatform) MigrateDB(interface{})     {}

func TestPlatformProviderReceivesTheCryptoConfig(t *testing.T) {
	s := attributionStore(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	cc := &crypto.Config{Key: key}

	eng := engine.NewEngine(s)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})

	platform := &recordingPlatform{}
	RegisterRoutes(chi.NewRouter(), s, eng, nil, nil,
		&extensions.Registry{Platform: platform}, nil, cc)

	if len(platform.args) < 2 {
		t.Fatalf("platform received %d extra args, want the engine and the crypto config", len(platform.args))
	}
	got, ok := platform.args[1].(*crypto.Config)
	if !ok {
		t.Fatalf("second arg is %T, want *crypto.Config", platform.args[1])
	}
	// The same key, not a zero-value stand-in: a provider encrypting with
	// a zero key writes data core cannot read back.
	if string(got.Key) != string(key) {
		t.Errorf("provider got a different key than core uses")
	}
}
