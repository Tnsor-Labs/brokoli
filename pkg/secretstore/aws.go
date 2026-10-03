package secretstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// AWSEndpointForTesting replaces the AWS endpoint (Secrets Manager, SSM and
// STS) for every AWS store. Tests only, for LocalStack. It is not a store
// setting: a store's settings come from a workspace editor, and whoever
// controls the endpoint receives the session's credentials and the OIDC
// token, so a store can never point the client anywhere but AWS.
var AWSEndpointForTesting string

// awsProvider is shared by aws_secrets_manager and aws_ssm: the settings
// and the authentication are the same, only the read differs.
type awsProvider struct {
	name  string
	shape Shape
}

// AWSSecretsManager is the aws_secrets_manager provider. Its secrets are a
// string or a JSON object (ShapeEither): a SecretString that parses as a
// JSON object of strings is map-shaped, anything else is a single value.
func AWSSecretsManager() Provider {
	return awsProvider{name: "aws_secrets_manager", shape: ShapeEither}
}

// AWSSSM is the aws_ssm provider: SSM Parameter Store, always a single
// value, decrypted (SecureString).
func AWSSSM() Provider { return awsProvider{name: "aws_ssm", shape: ShapeString} }

func (p awsProvider) Name() string { return p.name }
func (p awsProvider) Shape() Shape { return p.shape }
func (awsProvider) AuthMethods() []AuthMethod {
	// No "token": AWS has no static bearer token for these APIs, and a
	// long-lived access key pair stored in Brokoli is exactly what ADR-041
	// exists to avoid. ambient (an instance role, IRSA) or oidc instead.
	return []AuthMethod{AuthAmbient, AuthOIDC}
}

var (
	awsRegion  = regexp.MustCompile(`^[a-z]{2}(-gov|-iso[a-z]*)?-[a-z]+-[0-9]+$`)
	awsRoleARN = regexp.MustCompile(`^arn:aws[a-z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]{1,512}$`)
)

func (p awsProvider) ValidateSettings(s Settings, auth AuthMethod, a Settings) error {
	for k := range s {
		if k != "region" {
			// "endpoint" in particular: see AWSEndpointForTesting.
			return fmt.Errorf("settings.%s is not an %s setting (only region)", k, p.name)
		}
	}
	if !awsRegion.MatchString(s["region"]) {
		return fmt.Errorf("settings.region %q is not an AWS region, such as eu-west-1", s["region"])
	}
	for k := range a {
		if k != "role_arn" && k != "audience" {
			return fmt.Errorf("auth_settings.%s is not an %s setting (role_arn, audience)", k, p.name)
		}
	}
	switch auth {
	case AuthOIDC:
		if !awsRoleARN.MatchString(a["role_arn"]) {
			return errors.New(`auth_settings.role_arn must be an IAM role ARN (arn:aws:iam::<account>:role/<name>); oidc assumes it with the token`)
		}
	case AuthAmbient:
		if a["role_arn"] != "" && !awsRoleARN.MatchString(a["role_arn"]) {
			return errors.New("auth_settings.role_arn must be an IAM role ARN when set")
		}
		if a["audience"] != "" {
			return errors.New("auth_settings.audience applies only to oidc")
		}
	}
	return nil
}

// Audience is the token audience AWS STS expects, unless the role's trust
// policy names another.
func (awsProvider) Audience(_ Settings, a Settings) string {
	if a["audience"] != "" {
		return a["audience"]
	}
	return "sts.amazonaws.com"
}

// sessionName is the RoleSessionName AWS records in CloudTrail for every
// read: the run and node when there is one, else the store.
func sessionName(s Session) string {
	short := func(v string) string {
		v = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
				return r
			}
			return -1
		}, v)
		if len(v) > 12 {
			v = v[:12]
		}
		return v
	}
	name := "brokoli"
	if s.RunID != "" {
		name += "-run-" + short(s.RunID)
		if s.NodeID != "" {
			name += "-" + short(s.NodeID)
		}
	} else if s.StoreID != "" {
		name += "-store-" + short(s.StoreID)
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

// credentials builds the AWS credentials one store authenticates with.
func (p awsProvider) credentials(ctx context.Context, region string, id Identity) (aws.CredentialsProvider, error) {
	stsClient := func(creds aws.CredentialsProvider) *sts.Client {
		return sts.New(sts.Options{
			Region:      region,
			HTTPClient:  netguard.Outbound().Client(0),
			Credentials: creds,
		}, awsEndpointOption[sts.Options](func(o *sts.Options, ep string) { o.BaseEndpoint = aws.String(ep) }))
	}
	switch id.Method {
	case AuthOIDC:
		if id.Token == "" {
			return nil, errors.New("no OIDC token to exchange")
		}
		// AssumeRoleWithWebIdentity is unsigned: anonymous credentials, so
		// no identity of the machine can enter this path, even through a
		// misconfigured environment.
		web := stscreds.NewWebIdentityRoleProvider(stsClient(aws.AnonymousCredentials{}), id.Settings["role_arn"],
			staticToken(id.Token), func(o *stscreds.WebIdentityRoleOptions) { o.RoleSessionName = sessionName(id.Session) })
		return aws.NewCredentialsCache(web), nil
	case AuthAmbient:
		// The machine's own chain: environment, shared config, IRSA, the
		// instance role. Loaded with the SDK's own client, not the outbound
		// policy: the instance role lives at the metadata endpoint, which
		// the outbound policy always refuses, and this is the operator's
		// machine identity, already gated by AmbientAllowed. The reads
		// themselves go through the outbound policy.
		cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		if err != nil {
			return nil, fmt.Errorf("load the machine's AWS credentials: %w", err)
		}
		creds := cfg.Credentials
		if role := id.Settings["role_arn"]; role != "" {
			creds = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(stsClient(creds), role,
				func(o *stscreds.AssumeRoleOptions) { o.RoleSessionName = sessionName(id.Session) }))
		}
		return creds, nil
	}
	return nil, fmt.Errorf("auth method %q is not supported by %s", id.Method, p.name)
}

type staticToken string

func (t staticToken) GetIdentityToken() ([]byte, error) { return []byte(t), nil }

// awsEndpointOption applies AWSEndpointForTesting to a client's options.
func awsEndpointOption[O any](set func(*O, string)) func(*O) {
	return func(o *O) {
		if AWSEndpointForTesting != "" {
			set(o, AWSEndpointForTesting)
		}
	}
}

func (p awsProvider) Open(ctx context.Context, s Settings, id Identity) (Store, error) {
	creds, err := p.credentials(ctx, s["region"], id)
	if err != nil {
		return nil, err
	}
	cfg := aws.Config{Region: s["region"], Credentials: creds, HTTPClient: netguard.Outbound().Client(0)}
	if p.name == "aws_ssm" {
		return newSSMStore(creds, s["region"]), nil
	}
	return &smStore{client: secretsmanager.NewFromConfig(cfg, awsEndpointOption[secretsmanager.Options](func(o *secretsmanager.Options, ep string) {
		o.BaseEndpoint = aws.String(ep)
	}))}, nil
}

// smStore reads AWS Secrets Manager.
type smStore struct{ client *secretsmanager.Client }

// awsVersionID matches a Secrets Manager VersionId (a UUID). Any other
// ?version= value is a staging label: AWSCURRENT, AWSPREVIOUS or a custom
// one.
var awsVersionID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s *smStore) Get(ctx context.Context, path, version string) (Secret, error) {
	in := &secretsmanager.GetSecretValueInput{SecretId: aws.String(path)}
	switch {
	case version == "":
	case awsVersionID.MatchString(version):
		in.VersionId = aws.String(version)
	default:
		in.VersionStage = aws.String(version)
	}
	out, err := s.client.GetSecretValue(ctx, in)
	if err != nil {
		return Secret{}, awsError(err)
	}
	sec := Secret{Version: aws.ToString(out.VersionId)}
	if out.SecretString == nil {
		sec.Value = out.SecretBinary
		return sec, nil
	}
	str := aws.ToString(out.SecretString)
	// A JSON object of strings is the key/value secret the console creates;
	// its keys are fields. Anything else -- plain text, a JSON array, an
	// object with nested values -- is one value.
	var obj map[string]interface{}
	if strings.HasPrefix(strings.TrimSpace(str), "{") && json.Unmarshal([]byte(str), &obj) == nil {
		if fields, ok := flatFields(obj); ok {
			sec.Fields = fields
			return sec, nil
		}
	}
	sec.Value = []byte(str)
	return sec, nil
}

// flatFields turns a JSON object whose values are all strings, numbers or
// booleans into fields; ok is false when any value is nested.
func flatFields(obj map[string]interface{}) (map[string][]byte, bool) {
	fields := make(map[string][]byte, len(obj))
	for k, v := range obj {
		switch t := v.(type) {
		case string:
			fields[k] = []byte(t)
		case float64, bool:
			fields[k] = []byte(fmt.Sprint(t))
		default:
			return nil, false
		}
	}
	return fields, true
}

func (s *smStore) Close() error { return nil }

// awsError maps AWS's error codes onto the reasons a reference names. The
// SDK's messages carry no secret value; the request ID is kept for the
// customer's own support case.
func awsError(err error) error {
	var notFound *smtypes.ResourceNotFoundException
	if errors.As(err, &notFound) {
		return fmt.Errorf("%w (%v)", ErrNotFound, err)
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "AccessDeniedException", "AccessDenied", "UnrecognizedClientException", "InvalidClientTokenId",
			"ExpiredTokenException", "InvalidIdentityToken", "IDPRejectedClaim":
			return fmt.Errorf("%w (%s: %s)", ErrPermission, api.ErrorCode(), api.ErrorMessage())
		case "DecryptionFailure":
			return fmt.Errorf("%w: the KMS key could not decrypt the secret (%s)", ErrPermission, api.ErrorMessage())
		}
	}
	return err
}
