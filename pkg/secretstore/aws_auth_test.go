package secretstore

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// oidc exchanges the token for the role with AssumeRoleWithWebIdentity,
// naming the run and node in the session, and reads with the result. A
// recording proxy in front of LocalStack shows the exchange, since
// LocalStack itself does not check IAM.
func TestAWSOIDCAssumesTheRoleWithTheToken(t *testing.T) {
	f := newAWSFixture(t)
	name := f.param(t, "oidc", "oidc-value-0123")
	upstream, _ := url.Parse(AWSEndpointForTesting)
	var mu sync.Mutex
	var forms []url.Values
	var signedWith []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		if form, err := url.ParseQuery(string(body)); err == nil && form.Get("Action") != "" {
			forms = append(forms, form)
		}
		if a := r.Header.Get("Authorization"); a != "" {
			signedWith = append(signedWith, a)
		}
		mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		httputil.NewSingleHostReverseProxy(upstream).ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	AWSEndpointForTesting = proxy.URL

	got, err := read(t, AWSSSM(), oidcIdentity(), name, "")
	if err != nil || string(got.Value) != "oidc-value-0123" {
		t.Fatalf("oidc read: %+v %v", got, err)
	}
	var assume url.Values
	for _, f := range forms {
		if f.Get("Action") == "AssumeRoleWithWebIdentity" {
			assume = f
		}
	}
	if assume == nil {
		t.Fatalf("no AssumeRoleWithWebIdentity call; saw %v", forms)
	}
	if assume.Get("WebIdentityToken") != "header.payload.signature" ||
		assume.Get("RoleArn") != "arn:aws:iam::000000000000:role/brokoli-reader" ||
		assume.Get("RoleSessionName") != "brokoli-run-run-01234567-node-1" {
		t.Fatalf("the exchange = %v", assume)
	}
	// The read is signed with the assumed role's session, not the
	// fixture's static "test" key.
	if len(signedWith) == 0 {
		t.Fatal("no signed request reached the proxy")
	}
	if last := signedWith[len(signedWith)-1]; strings.Contains(last, "Credential=test/") {
		t.Fatalf("the read was signed with the static test key, not the assumed role: %s", last)
	}
}

// Secrets Manager secrets are either shape, decided per secret: a field on
// a plain-text secret is refused, and a key/value secret needs one.
func TestAWSSecretsManagerShapeRulesEndToEnd(t *testing.T) {
	f := newAWSFixture(t)
	plain := f.secret(t, "shape-plain", "plain-text")
	kv := f.secret(t, "shape-kv", `{"password":"pw-0123456789"}`)
	for ref, want := range map[string]string{
		"secret://aws/" + plain + "#password": "single value; remove #password",
		"secret://aws/" + kv:                  "the secret has fields (password)",
	} {
		r, err := ParseRef(ref)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckShape(r, AWSSecretsManager().Shape()); err != nil {
			t.Fatalf("%s refused before reading: %v", ref, err)
		}
		s, err := read(t, AWSSecretsManager(), oidcIdentity(), r.Path, r.Version)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Pick(r, s); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", ref, err, want)
		}
	}
	r, _ := ParseRef("secret://aws/" + kv + "#password")
	s, _ := read(t, AWSSecretsManager(), oidcIdentity(), r.Path, "")
	if v, err := Pick(r, s); err != nil || string(v) != "pw-0123456789" {
		t.Fatalf("the field: %q %v", v, err)
	}
	// SSM is single values only: a field is refused before any read.
	field, _ := ParseRef("secret://ssm//x/y#f")
	if err := CheckShape(field, AWSSSM().Shape()); err == nil {
		t.Fatal("aws_ssm accepted a #field")
	}
}

// ambient with a role_arn assumes it with the machine's credentials, and
// reads as the role.
func TestAWSAmbientAssumesItsRole(t *testing.T) {
	f := newAWSFixture(t)
	name := f.param(t, "ambient-role", "ambient-role-0123")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	upstream, _ := url.Parse(AWSEndpointForTesting)
	var mu sync.Mutex
	var actions []url.Values
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if form, err := url.ParseQuery(string(body)); err == nil && form.Get("Action") != "" {
			mu.Lock()
			actions = append(actions, form)
			mu.Unlock()
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		httputil.NewSingleHostReverseProxy(upstream).ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	AWSEndpointForTesting = proxy.URL
	id := Identity{Method: AuthAmbient, Settings: Settings{"role_arn": "arn:aws:iam::000000000000:role/brokoli-reader"},
		Session: Session{StoreID: "st-amb"}}
	if got, err := read(t, AWSSSM(), id, name, ""); err != nil || string(got.Value) != "ambient-role-0123" {
		t.Fatalf("read: %+v %v", got, err)
	}
	var assumed bool
	for _, a := range actions {
		if a.Get("Action") == "AssumeRole" && a.Get("RoleArn") == "arn:aws:iam::000000000000:role/brokoli-reader" &&
			a.Get("RoleSessionName") == "brokoli-store-st-amb" {
			assumed = true
		}
	}
	if !assumed {
		t.Fatalf("the role was not assumed; saw %v", actions)
	}
}

func TestAWSSettings(t *testing.T) {
	p := AWSSecretsManager()
	ok := []struct {
		s, a Settings
		m    AuthMethod
	}{
		{Settings{"region": "eu-west-1"}, Settings{"role_arn": "arn:aws:iam::123456789012:role/reader"}, AuthOIDC},
		{Settings{"region": "us-gov-west-1"}, Settings{"role_arn": "arn:aws-us-gov:iam::123456789012:role/r", "audience": "brokoli"}, AuthOIDC},
		{Settings{"region": "eu-west-1"}, Settings{}, AuthAmbient},
		{Settings{"region": "eu-west-1"}, Settings{"role_arn": "arn:aws:iam::123456789012:role/path/reader"}, AuthAmbient},
	}
	for _, c := range ok {
		if err := p.ValidateSettings(c.s, c.m, c.a); err != nil {
			t.Errorf("%v %v %s: %v", c.s, c.a, c.m, err)
		}
	}
	for want, c := range map[string]struct {
		s, a Settings
		m    AuthMethod
	}{
		"not an AWS region":         {Settings{"region": "europe"}, Settings{}, AuthAmbient},
		"settings.endpoint is not":  {Settings{"region": "eu-west-1", "endpoint": "http://evil"}, Settings{}, AuthAmbient},
		"must be an IAM role ARN (": {Settings{"region": "eu-west-1"}, Settings{}, AuthOIDC},
		"applies only to oidc":      {Settings{"region": "eu-west-1"}, Settings{"audience": "x"}, AuthAmbient},
		"auth_settings.token is":    {Settings{"region": "eu-west-1"}, Settings{"token": "x"}, AuthAmbient},
	} {
		if err := p.ValidateSettings(c.s, c.m, c.a); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
	for _, m := range p.AuthMethods() {
		if m == AuthToken {
			t.Fatal("the AWS providers must not offer a stored token")
		}
	}
	if p.Audience(nil, Settings{}) != "sts.amazonaws.com" || p.Audience(nil, Settings{"audience": "x"}) != "x" {
		t.Fatal("audience")
	}
}

func TestAWSSessionName(t *testing.T) {
	for in, want := range map[Session]string{
		{RunID: "01a1-0229-1d07", NodeID: "api"}: "brokoli-run-01a1-0229-1d-api",
		{StoreID: "st-ssm"}:                      "brokoli-store-st-ssm",
		{}:                                       "brokoli",
		{RunID: "r/u:n", NodeID: "n o d e"}:      "brokoli-run-run-node",
	} {
		if got := sessionName(in); got != want {
			t.Errorf("sessionName(%+v) = %q, want %q", in, got, want)
		}
	}
}
