package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestS3FileConfigFromExtra(t *testing.T) {
	tests := []struct {
		name    string
		extra   string
		wantErr string
	}{
		{name: "valid", extra: `{"bucket":"customer-data","region":"us-east-1","access_key":"key","secret_key":"secret"}`},
		{name: "temporary credentials", extra: `{"bucket":"customer-data","region":"us-east-1","access_key":"key","secret_key":"secret","session_token":"token"}`},
		{name: "missing bucket", extra: `{"region":"us-east-1"}`, wantErr: "requires bucket"},
		{name: "credential pair", extra: `{"bucket":"customer-data","region":"us-east-1","access_key":"key"}`, wantErr: "provided together"},
		{name: "session token without credentials", extra: `{"bucket":"customer-data","region":"us-east-1","session_token":"token"}`, wantErr: "requires access_key"},
		{name: "invalid bucket target", extra: `{"bucket":"ignored@127.0.0.1:1/","region":"us-east-1"}`, wantErr: "target blocked"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := S3FileConfigFromExtra(test.extra)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestS3FileClientUsesSessionToken(t *testing.T) {
	client, err := newS3FileClient(context.Background(), &models.Connection{Extra: `{"bucket":"customer-data","region":"us-east-1","access_key":"key","secret_key":"secret","session_token":"token"}`})
	if err != nil {
		t.Fatalf("newS3FileClient: %v", err)
	}
	got, err := client.client.Options().Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("retrieve credentials: %v", err)
	}
	if got.SessionToken != "token" {
		t.Fatalf("session token = %q, want %q", got.SessionToken, "token")
	}
}
