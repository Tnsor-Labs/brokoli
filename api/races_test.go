package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/golang-jwt/jwt/v5"
)

// The JWT secret was a package variable that GenerateToken, SignToken and
// the datacap issuer each initialised on first use with no lock. Requests
// arriving together raced on it, and with no BROKOLI_JWT_SECRET and no
// secret file each racer generated its own random secret: a token signed
// under one failed to verify under another. The first use is now one
// initialisation, however many requests arrive at once.
func TestJWTSecretFirstUseIsSafeConcurrently(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BROKOLI_JWT_SECRET", "")
	resetJWTSecretForTest(t)

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := GenerateToken(&User{ID: "u1", Username: "ada", Role: RoleAdmin})
			if err != nil {
				errs <- err
				return
			}
			if _, err := ParseToken(token); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a token from the first use did not verify: %v", err)
	}
}

// The engine counts runs and attempts with atomic adds, and /metrics read
// the same fields plainly: a scrape during a run was a data race (go test
// -race reports it). The handler now reads a snapshot taken atomically.
func TestMetricsScrapeDuringRunsIsRaceFree(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	eng := engine.NewEngine(s)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				atomic.AddInt64(&eng.RunsTotal, 1)
				atomic.AddInt64(&eng.AttemptsStarted, 1)
				atomic.AddInt64(&eng.EventsAppended, 1)
			}
		}
	}()
	metrics := NewMetrics()
	for i := 0; i < 50; i++ {
		rec := httptest.NewRecorder()
		PrometheusHandler(metrics, s, eng, nil)(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("scrape %d: status %d", i, rec.Code)
		}
	}
	close(stop)
	<-done
}

// Tokens are HS256. ParseToken accepted any algorithm the key happened to
// verify, so a token signed with the same secret under HS384 passed. Only
// HS256 is accepted now.
func TestParseTokenAcceptsOnlyHS256(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BROKOLI_JWT_SECRET", "a-test-secret-that-is-long-enough-123")
	resetJWTSecretForTest(t)
	claims := jwt.MapClaims{"sub": "u1", "username": "ada", "role": "admin", "exp": time.Now().Add(time.Hour).Unix()}

	good, err := SignToken(claims)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseToken(good); err != nil {
		t.Fatalf("an HS256 token was refused: %v", err)
	}
	other, err := jwt.NewWithClaims(jwt.SigningMethodHS384, claims).SignedString([]byte("a-test-secret-that-is-long-enough-123"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseToken(other); err == nil {
		t.Fatal("an HS384 token signed with the server's secret was accepted")
	}
}

// With no configured secret and a working directory it cannot write, the
// secret lives only in memory. InitJWTSecret, which NewServer calls, used
// to load it again each time, minting a new random secret and invalidating
// every token already issued. It now keeps the one it has.
func TestInitJWTSecretKeepsTheLoadedSecret(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	t.Setenv("BROKOLI_JWT_SECRET", "")
	resetJWTSecretForTest(t)

	token, err := GenerateToken(&User{ID: "u1", Username: "ada", Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	InitJWTSecret()
	if _, err := ParseToken(token); err != nil {
		t.Fatalf("a token issued before InitJWTSecret no longer verifies: %v", err)
	}
}
