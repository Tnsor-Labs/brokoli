package artifact

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// stubProvider hands back fixed credentials and records that it was asked,
// which is how these tests tell which credential path the store took.
type stubProvider struct{ called bool }

func (p *stubProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	p.called = true
	return aws.Credentials{AccessKeyID: "from-provider", SecretAccessKey: "s", Source: "stub"}, nil
}

// A caller-supplied provider is used, so short-lived credentials that the
// SDK re-mints (STS sessions behind aws.CredentialsCache) work at all.
func TestS3StoreUsesCredentialsProvider(t *testing.T) {
	p := &stubProvider{}
	store, err := NewS3Store(context.Background(), S3StoreConfig{
		Bucket:              "b",
		Region:              "us-east-1",
		CredentialsProvider: p,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if store == nil {
		t.Fatal("expected a store")
	}
	creds, err := store.client.Options().Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if !p.called || creds.AccessKeyID != "from-provider" {
		t.Fatalf("provider was not used: called=%v creds=%q", p.called, creds.AccessKeyID)
	}
}

// A provider wins over a static pair: a caller that built one means it.
func TestS3StoreCredentialsProviderBeatsStaticPair(t *testing.T) {
	p := &stubProvider{}
	store, err := NewS3Store(context.Background(), S3StoreConfig{
		Bucket:              "b",
		Region:              "us-east-1",
		AccessKeyID:         "from-static",
		SecretAccessKey:     "s",
		CredentialsProvider: p,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	creds, err := store.client.Options().Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if creds.AccessKeyID != "from-provider" {
		t.Fatalf("static pair won over the provider: %q", creds.AccessKeyID)
	}
}

// Without a provider the static pair still applies, unchanged.
func TestS3StoreStaticPairStillWorks(t *testing.T) {
	store, err := NewS3Store(context.Background(), S3StoreConfig{
		Bucket:          "b",
		Region:          "us-east-1",
		AccessKeyID:     "from-static",
		SecretAccessKey: "s",
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	creds, err := store.client.Options().Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if creds.AccessKeyID != "from-static" {
		t.Fatalf("expected the static pair, got %q", creds.AccessKeyID)
	}
}
