package api

import "testing"

func resetJWTSecretForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		jwtSecretMu.Lock()
		jwtSecret = nil
		jwtSecretMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}
