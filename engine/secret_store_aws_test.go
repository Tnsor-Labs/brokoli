package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/pkg/secretstore"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A run resolves secret://<aws store>/<name>#<field> through the real
// aws_secrets_manager provider, against LocalStack, with an OIDC token
// requested for this run and node.
func TestRunResolvesAnAWSSecret(t *testing.T) {
	endpoint := os.Getenv("BROKOLI_TEST_AWS_ENDPOINT")
	if endpoint == "" {
		t.Skip("set BROKOLI_TEST_AWS_ENDPOINT to run against LocalStack")
	}
	prev := secretstore.AWSEndpointForTesting
	secretstore.AWSEndpointForTesting = endpoint
	t.Cleanup(func() { secretstore.AWSEndpointForTesting = prev })
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))

	name := fmt.Sprintf("brokoli-engine-%d/partner-api", time.Now().UnixNano())
	sm := secretsmanager.NewFromConfig(aws.Config{Region: "eu-west-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")},
		func(o *secretsmanager.Options) { o.BaseEndpoint = aws.String(endpoint) })
	if _, err := sm.CreateSecret(context.Background(), &secretsmanager.CreateSecretInput{Name: aws.String(name),
		SecretString: aws.String(`{"api_token":"aws-token-0123456789"}`)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = sm.DeleteSecret(context.Background(), &secretsmanager.DeleteSecretInput{SecretId: aws.String(name), ForceDeleteWithoutRecovery: aws.Bool(true)})
	})

	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Api-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	t.Cleanup(srv.Close)

	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "aws.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UTC()
	if err := st.CreateSecretStore(&models.SecretStore{ID: "st-aws", Name: "aws", WorkspaceID: "ws-a", Provider: "aws_secrets_manager",
		Settings: map[string]string{"region": "eu-west-1"}, AuthMethod: "oidc",
		AuthSettings: map[string]string{"role_arn": "arn:aws:iam::000000000000:role/brokoli"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	key := testKey(6)
	extra, _ := json.Marshal(map[string]interface{}{"headers": map[string]string{"X-Api-Token": "secret://aws/" + name + "#api_token"}})
	sealed, _ := key.Encrypt(string(extra))
	if err := st.CreateConnection(&models.Connection{ID: "c-aws", ConnID: "partner", Type: models.ConnTypeHTTP, Host: "unused",
		WorkspaceID: "ws-a", Extra: sealed, ExtraRef: "encrypted://" + sealed, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	pipe := &models.Pipeline{ID: "aws-run", Name: "aws-run", Enabled: true, WorkspaceID: "ws-a",
		Nodes: []models.Node{{ID: "api", Type: models.NodeTypeSourceAPI, Name: "API",
			Config: map[string]interface{}{"conn_id": "partner", "url": srv.URL, "method": "GET"}}},
		CreatedAt: now, UpdatedAt: now}
	if err := st.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}

	enc := secrets.NewEncryptedResolver(key)
	chain := secrets.NewChain(enc, enc)
	res := NewSecretStoreResolver(st, secretstore.NewRegistry(secretstore.Builtin()...), chain)
	tokens := &recordingTokens{}
	res.SetTokenSource(tokens)
	chain.Register(res)
	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.ConnResolver = NewConnectionResolver(st, chain)
	run, err := eng.RunPipeline(pipe.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run: %v %+v", err, run)
	}
	if gotHeader != "aws-token-0123456789" {
		t.Fatalf("the target received %q, want the AWS secret's field", gotHeader)
	}
	if len(tokens.reqs) != 1 || tokens.reqs[0].Audience != "sts.amazonaws.com" || tokens.reqs[0].RunID != run.ID ||
		tokens.reqs[0].SubjectID != "st-aws" {
		t.Fatalf("token requests = %+v", tokens.reqs)
	}
}
