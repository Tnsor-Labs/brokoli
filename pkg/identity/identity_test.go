package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// jwt builds an unsigned token with the given claims. FileTokenSource does
// not verify signatures (the relying party does), so a placeholder signature
// is enough.
func jwt(t *testing.T, claims map[string]interface{}) string {
	t.Helper()
	enc := func(v interface{}) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + enc(claims) + ".c2ln"
}

func writeToken(t *testing.T, dir, name, token string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFileTokenSourceServesTheFileWithTheRequestedAudience(t *testing.T) {
	dir := t.TempDir()
	exp := time.Now().Add(time.Hour).Unix()
	gcp := jwt(t, map[string]interface{}{"aud": "https://iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/brokoli", "exp": exp})
	vault := jwt(t, map[string]interface{}{"aud": []string{"vault", "other"}, "exp": exp})
	src := NewFileTokenSource(writeToken(t, dir, "gcp", gcp), writeToken(t, dir, "vault", vault))

	got, err := src.Token(context.Background(), TokenRequest{Audience: "vault"})
	if err != nil || got != vault {
		t.Fatalf("audience in a list: got %q, %v", got, err)
	}
	got, err = src.Token(context.Background(), TokenRequest{Audience: "https://iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/brokoli"})
	if err != nil || got != gcp {
		t.Fatalf("audience as a string: got %q, %v", got, err)
	}
	if _, err := src.Token(context.Background(), TokenRequest{Audience: "sts.amazonaws.com"}); err == nil || !strings.Contains(err.Error(), `no token file has audience "sts.amazonaws.com"`) {
		t.Fatalf("an audience no file carries: err = %v", err)
	}
}

// A token minted for one audience must never be presented to another, so
// a request without one is refused rather than served the first file.
func TestATokenRequestNeedsAnAudience(t *testing.T) {
	dir := t.TempDir()
	src := NewFileTokenSource(writeToken(t, dir, "t", jwt(t, map[string]interface{}{"aud": "x"})))
	if _, err := src.Token(context.Background(), TokenRequest{}); err == nil {
		t.Fatal("a request with no audience was served")
	}
}

// The platform rotates the file in place; every request reads it again.
func TestFileTokenSourceRereadsTheFile(t *testing.T) {
	dir := t.TempDir()
	exp := time.Now().Add(time.Hour).Unix()
	first := jwt(t, map[string]interface{}{"aud": "a", "exp": exp, "n": 1})
	path := writeToken(t, dir, "t", first)
	src := NewFileTokenSource(path)
	if got, _ := src.Token(context.Background(), TokenRequest{Audience: "a"}); got != first {
		t.Fatal("first read")
	}
	second := jwt(t, map[string]interface{}{"aud": "a", "exp": exp, "n": 2})
	writeToken(t, dir, "t", second)
	if got, _ := src.Token(context.Background(), TokenRequest{Audience: "a"}); got != second {
		t.Fatal("the rotated token was not served; the source cached the old one")
	}
}

func TestFileTokenSourceRefusesAnExpiredToken(t *testing.T) {
	dir := t.TempDir()
	src := NewFileTokenSource(writeToken(t, dir, "t", jwt(t, map[string]interface{}{"aud": "a", "exp": time.Now().Add(-time.Minute).Unix()})))
	_, err := src.Token(context.Background(), TokenRequest{Audience: "a"})
	if err == nil || !strings.Contains(err.Error(), "stopped refreshing") {
		t.Fatalf("an expired token: err = %v", err)
	}
}

func TestFileTokenSourceRefusesSomethingThatIsNotAJWT(t *testing.T) {
	dir := t.TempDir()
	src := NewFileTokenSource(writeToken(t, dir, "t", "not-a-token"))
	if _, err := src.Token(context.Background(), TokenRequest{Audience: "a"}); err == nil || !strings.Contains(err.Error(), "not a JWT") {
		t.Fatalf("err = %v", err)
	}
}

func TestFileTokenSourceFromEnv(t *testing.T) {
	t.Setenv(FilesEnv, "")
	if FileTokenSourceFromEnv() != nil {
		t.Fatal("a source was configured from an empty list")
	}
	t.Setenv(FilesEnv, " /a/token , ,/b/token ")
	src := FileTokenSourceFromEnv()
	if src == nil || len(src.paths) != 2 || src.paths[0] != "/a/token" || src.paths[1] != "/b/token" {
		t.Fatalf("paths = %v", src)
	}
}

// A machine's token is the machine's identity. Where ambient identity is
// denied, Token refuses it by name, before reading anything; elsewhere it
// is served. Both directions.
func TestAMachineTokenFollowsTheAmbientSwitch(t *testing.T) {
	dir := t.TempDir()
	token := jwt(t, map[string]interface{}{"aud": "a", "exp": time.Now().Add(time.Hour).Unix()})
	src := NewFileTokenSource(writeToken(t, dir, "t", token))

	t.Setenv(AmbientEnv, "deny")
	if _, err := Token(context.Background(), src, TokenRequest{Audience: "a"}); !errors.Is(err, ErrAmbientDenied) {
		t.Fatalf("ambient denied: err = %v, want ErrAmbientDenied", err)
	}
	t.Setenv(AmbientEnv, "")
	if got, err := Token(context.Background(), src, TokenRequest{Audience: "a"}); err != nil || got != token {
		t.Fatalf("ambient allowed: got %q, %v", got, err)
	}
}

// A source that issues a token per workspace is not the machine's identity
// and is not affected by the switch.
type perWorkspaceSource struct{ seen TokenRequest }

func (p *perWorkspaceSource) Token(_ context.Context, req TokenRequest) (string, error) {
	p.seen = req
	return "per-workspace", nil
}

func TestAPerWorkspaceSourceIsServedWhereAmbientIsDenied(t *testing.T) {
	t.Setenv(AmbientEnv, "deny")
	src := &perWorkspaceSource{}
	req := TokenRequest{Audience: "a", WorkspaceID: "ws", SubjectKind: "connection", SubjectID: "c1", RunID: "r", NodeID: "n"}
	if got, err := Token(context.Background(), src, req); err != nil || got != "per-workspace" {
		t.Fatalf("got %q, %v", got, err)
	}
	if src.seen != req {
		t.Fatalf("the source got %+v, want the whole request %+v", src.seen, req)
	}
}

func TestTokenWithNoSourceSaysSo(t *testing.T) {
	if _, err := Token(context.Background(), nil, TokenRequest{Audience: "a"}); !errors.Is(err, ErrNoTokenSource) {
		t.Fatalf("err = %v", err)
	}
}

func TestAmbientAllowed(t *testing.T) {
	for value, want := range map[string]bool{"": true, "allow": true, "deny": false, " DENY ": false} {
		t.Setenv(AmbientEnv, value)
		if got := AmbientAllowed(); got != want {
			t.Errorf("%s=%q: AmbientAllowed = %v, want %v", AmbientEnv, value, got, want)
		}
	}
}
