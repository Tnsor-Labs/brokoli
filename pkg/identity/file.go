package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// FilesEnv lists the token files a FileTokenSource reads, comma-separated.
const FilesEnv = "BROKOLI_OIDC_TOKEN_FILES"

// FileTokenSource serves tokens a platform writes to disk and keeps fresh,
// such as Kubernetes projected service-account tokens. Each file holds one
// token for one audience; a request is served from the file whose token
// carries the requested audience.
//
// The file is read on every request, never cached, because the platform
// rotates it in place. A token that has expired is refused rather than sent,
// since a stale file means the platform stopped refreshing it, and the
// relying party's refusal would say far less.
//
// These tokens identify the machine (a pod's service account), not the
// workspace, so this is a Machine source: it serves nothing where ambient
// identity is denied.
type FileTokenSource struct {
	paths []string
	now   func() time.Time
}

// NewFileTokenSource returns a source reading the given files.
func NewFileTokenSource(paths ...string) *FileTokenSource {
	return &FileTokenSource{paths: paths, now: time.Now}
}

// FileTokenSourceFromEnv returns a source for the files listed in FilesEnv,
// or nil when none are listed.
func FileTokenSourceFromEnv() *FileTokenSource {
	var paths []string
	for _, p := range strings.Split(os.Getenv(FilesEnv), ",") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return NewFileTokenSource(paths...)
}

// MachineIdentity reports that these tokens are the machine's own identity.
func (*FileTokenSource) MachineIdentity() bool { return true }

// Token returns the token from the first file whose audience matches.
func (s *FileTokenSource) Token(ctx context.Context, req TokenRequest) (string, error) {
	if err := req.Validate(); err != nil {
		return "", err
	}
	var seen []string
	for _, path := range s.paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		raw, err := os.ReadFile(path) // #nosec G304 -- operator-configured path (BROKOLI_OIDC_TOKEN_FILES), never request input
		if err != nil {
			return "", fmt.Errorf("identity: read token file %s: %w", path, err)
		}
		token := strings.TrimSpace(string(raw))
		claims, err := unverifiedClaims(token)
		if err != nil {
			return "", fmt.Errorf("identity: token file %s: %w", path, err)
		}
		if !claims.hasAudience(req.Audience) {
			seen = append(seen, strings.Join(claims.Audience, " "))
			continue
		}
		if claims.Expiry > 0 && !s.now().Before(time.Unix(claims.Expiry, 0)) {
			return "", fmt.Errorf("identity: the token in %s expired at %s; the platform that writes it has stopped refreshing it",
				path, time.Unix(claims.Expiry, 0).UTC().Format(time.RFC3339))
		}
		return token, nil
	}
	return "", fmt.Errorf("identity: no token file has audience %q (files carry: %s); project a token for that audience",
		req.Audience, strings.Join(seen, "; "))
}

// jwtClaims is the part of a token this source reads to choose a file. The
// token is not verified here: it was written by the platform, and the
// relying party that receives it is what verifies it.
type jwtClaims struct {
	Audience audience `json:"aud"`
	Expiry   int64    `json:"exp"`
}

func (c jwtClaims) hasAudience(want string) bool {
	for _, a := range c.Audience {
		if a == want {
			return true
		}
	}
	return false
}

// audience is a JWT "aud", which is a string or an array of strings.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New(`"aud" is neither a string nor a list of strings`)
	}
	*a = many
	return nil
}

func unverifiedClaims(token string) (jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}, errors.New("not a JWT (want three dot-separated parts)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}, fmt.Errorf("decode JWT payload: %w", err)
	}
	var c jwtClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return jwtClaims{}, fmt.Errorf("parse JWT payload: %w", err)
	}
	return c, nil
}
