package secretstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// The AWS providers against LocalStack (docker-compose.test.yml):
//
//	docker compose -f docker-compose.test.yml up -d --wait localstack
//	BROKOLI_TEST_AWS_ENDPOINT=http://127.0.0.1:55545 go test ./pkg/secretstore -run AWS

const awsTestRegion = "eu-west-1"

type awsFixture struct {
	sm     *secretsmanager.Client
	ssm    *ssm.Client
	prefix string
}

func newAWSFixture(t *testing.T) *awsFixture {
	t.Helper()
	endpoint := os.Getenv("BROKOLI_TEST_AWS_ENDPOINT")
	if endpoint == "" {
		t.Skip("set BROKOLI_TEST_AWS_ENDPOINT to run the AWS secret-store tests against LocalStack")
	}
	prev := AWSEndpointForTesting
	AWSEndpointForTesting = endpoint
	t.Cleanup(func() { AWSEndpointForTesting = prev })
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	// The ambient chain reads the environment; keep it to what the test
	// sets, never this machine's own AWS configuration.
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")

	cfg := aws.Config{Region: awsTestRegion, Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}
	f := &awsFixture{
		sm:     secretsmanager.NewFromConfig(cfg, func(o *secretsmanager.Options) { o.BaseEndpoint = aws.String(endpoint) }),
		ssm:    ssm.NewFromConfig(cfg, func(o *ssm.Options) { o.BaseEndpoint = aws.String(endpoint) }),
		prefix: fmt.Sprintf("brokoli-test-%d", time.Now().UnixNano()),
	}
	return f
}

func (f *awsFixture) secret(t *testing.T, name, value string) string {
	t.Helper()
	full := f.prefix + "/" + name
	if _, err := f.sm.CreateSecret(context.Background(), &secretsmanager.CreateSecretInput{
		Name: aws.String(full), SecretString: aws.String(value)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.sm.DeleteSecret(context.Background(), &secretsmanager.DeleteSecretInput{
			SecretId: aws.String(full), ForceDeleteWithoutRecovery: aws.Bool(true)})
	})
	return full
}

func (f *awsFixture) param(t *testing.T, name, value string) string {
	t.Helper()
	full := "/" + f.prefix + "/" + name
	if _, err := f.ssm.PutParameter(context.Background(), &ssm.PutParameterInput{
		Name: aws.String(full), Value: aws.String(value), Type: ssmtypes.ParameterTypeSecureString}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.ssm.DeleteParameter(context.Background(), &ssm.DeleteParameterInput{Name: aws.String(full)})
	})
	return full
}

func oidcIdentity() Identity {
	return Identity{Method: AuthOIDC, Token: "header.payload.signature",
		Settings: Settings{"role_arn": "arn:aws:iam::000000000000:role/brokoli-reader"},
		Session:  Session{StoreID: "st-1", RunID: "run-0123456789abcdef", NodeID: "node-1"}}
}

func read(t *testing.T, p Provider, id Identity, path, version string) (Secret, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := p.Open(ctx, Settings{"region": awsTestRegion}, id)
	if err != nil {
		return Secret{}, err
	}
	defer st.Close() //nolint:errcheck
	return st.Get(ctx, path, version)
}

func TestAWSSecretsManager(t *testing.T) {
	f := newAWSFixture(t)
	p := AWSSecretsManager()
	plain := f.secret(t, "plain", "a-single-value")
	kv := f.secret(t, "kv", `{"username":"loader","password":"pw-0123456789","port":5432}`)
	nested := f.secret(t, "nested", `{"a":{"b":"c"}}`)

	got, err := read(t, p, oidcIdentity(), plain, "")
	if err != nil || string(got.Value) != "a-single-value" || got.Fields != nil {
		t.Fatalf("string secret: %+v %v", got, err)
	}
	got, err = read(t, p, oidcIdentity(), kv, "")
	if err != nil || string(got.Fields["password"]) != "pw-0123456789" || string(got.Fields["port"]) != "5432" {
		t.Fatalf("key/value secret: %+v %v", got, err)
	}
	if got, err = read(t, p, oidcIdentity(), nested, ""); err != nil || got.Fields != nil || string(got.Value) != `{"a":{"b":"c"}}` {
		t.Fatalf("a nested object is one value: %+v %v", got, err)
	}

	// Versions: a new value moves AWSCURRENT; AWSPREVIOUS and the old
	// VersionId still read the old one.
	first, _ := read(t, p, oidcIdentity(), plain, "")
	if _, err := f.sm.PutSecretValue(context.Background(), &secretsmanager.PutSecretValueInput{
		SecretId: aws.String(plain), SecretString: aws.String("rotated-value")}); err != nil {
		t.Fatal(err)
	}
	if got, _ := read(t, p, oidcIdentity(), plain, ""); string(got.Value) != "rotated-value" {
		t.Fatalf("current after rotation = %q", got.Value)
	}
	if got, err := read(t, p, oidcIdentity(), plain, "AWSPREVIOUS"); err != nil || string(got.Value) != "a-single-value" {
		t.Fatalf("AWSPREVIOUS = %q %v", got.Value, err)
	}
	if got, err := read(t, p, oidcIdentity(), plain, first.Version); err != nil || string(got.Value) != "a-single-value" {
		t.Fatalf("by VersionId %s = %q %v", first.Version, got.Value, err)
	}

	if _, err := read(t, p, oidcIdentity(), f.prefix+"/missing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing secret: %v", err)
	}
}

func TestAWSSSM(t *testing.T) {
	f := newAWSFixture(t)
	p := AWSSSM()
	name := f.param(t, "warehouse-password", "ssm-secure-0123")
	got, err := read(t, p, oidcIdentity(), name, "")
	if err != nil || string(got.Value) != "ssm-secure-0123" || got.Version != "1" {
		t.Fatalf("SecureString: %+v %v", got, err)
	}
	if _, err := f.ssm.PutParameter(context.Background(), &ssm.PutParameterInput{Name: aws.String(name),
		Value: aws.String("ssm-secure-v2"), Type: ssmtypes.ParameterTypeSecureString, Overwrite: aws.Bool(true)}); err != nil {
		t.Fatal(err)
	}
	if got, err := read(t, p, oidcIdentity(), name, "1"); err != nil || string(got.Value) != "ssm-secure-0123" {
		t.Fatalf("version 1 = %q %v", got.Value, err)
	}
	if got, _ := read(t, p, oidcIdentity(), name, ""); string(got.Value) != "ssm-secure-v2" {
		t.Fatalf("current = %q", got.Value)
	}
	if _, err := read(t, p, oidcIdentity(), "/"+f.prefix+"/missing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing parameter: %v", err)
	}
}

// ambient reads with the machine's own chain: here, credentials in the
// environment.
func TestAWSAmbientUsesTheEnvironment(t *testing.T) {
	f := newAWSFixture(t)
	name := f.param(t, "ambient", "ambient-value-0123")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	got, err := read(t, AWSSSM(), Identity{Method: AuthAmbient}, name, "")
	if err != nil || string(got.Value) != "ambient-value-0123" {
		t.Fatalf("ambient: %+v %v", got, err)
	}
	// With a role to assume, the machine's credentials assume it first.
	got, err = read(t, AWSSSM(), Identity{Method: AuthAmbient,
		Settings: Settings{"role_arn": "arn:aws:iam::000000000000:role/brokoli-reader"}}, name, "")
	if err != nil || string(got.Value) != "ambient-value-0123" {
		t.Fatalf("ambient with a role: %+v %v", got, err)
	}
}

// oidc is refused without a token, and never falls back to the machine's
// credentials.
func TestAWSOIDCNeedsItsToken(t *testing.T) {
	newAWSFixture(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	id := oidcIdentity()
	id.Token = ""
	if _, err := read(t, AWSSSM(), id, "/x", ""); err == nil || !strings.Contains(err.Error(), "no OIDC token") {
		t.Fatalf("oidc without a token: %v", err)
	}
}

// Reads go through the outbound policy: the default refuses LocalStack on
// loopback.
func TestAWSReadsGoThroughTheOutboundPolicy(t *testing.T) {
	f := newAWSFixture(t)
	name := f.param(t, "policy", "v")
	secret := f.secret(t, "policy", "v")
	// ambient with environment credentials: no STS call, so the only
	// request is the read itself.
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{}))
	if _, err := read(t, AWSSSM(), Identity{Method: AuthAmbient}, name, ""); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("SSM: err = %v, want the outbound policy's refusal", err)
	}
	if _, err := read(t, AWSSecretsManager(), Identity{Method: AuthAmbient}, secret, ""); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("Secrets Manager: err = %v, want the outbound policy's refusal", err)
	}
}
