package secretstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// ssmStore reads SSM Parameter Store with one signed GetParameter call,
// not the SSM SDK module: that module is the whole SSM API and added about
// 3 MB to the binary for this one read (ADR-041 section 3: a provider whose
// SDK is disproportionate talks to the vendor's API directly). The request
// is the documented JSON 1.1 protocol, signed with the SDK's own SigV4
// signer.
type ssmStore struct {
	creds    aws.CredentialsProvider
	region   string
	endpoint string
	client   *http.Client
}

func newSSMStore(creds aws.CredentialsProvider, region string) *ssmStore {
	endpoint := "https://ssm." + region + ".amazonaws.com/"
	if AWSEndpointForTesting != "" {
		endpoint = strings.TrimRight(AWSEndpointForTesting, "/") + "/"
	}
	return &ssmStore{creds: creds, region: region, endpoint: endpoint, client: netguard.Outbound().Client(30 * time.Second)}
}

// ssmAPIError is SSM's JSON error body.
type ssmAPIError struct {
	Type    string `json:"__type"`
	Message string `json:"message"`
	Code    string // the type without its namespace
}

func (e *ssmAPIError) Error() string { return e.Code + ": " + e.Message }

func (s *ssmStore) Get(ctx context.Context, path, version string) (Secret, error) {
	name := path
	if version != "" {
		// SSM's own selector: name:version (a number) or name:label.
		name = path + ":" + version
	}
	body, err := json.Marshal(map[string]interface{}{"Name": name, "WithDecryption": true})
	if err != nil {
		return Secret{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return Secret{}, err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonSSM.GetParameter")
	creds, err := s.creds.Retrieve(ctx)
	if err != nil {
		return Secret{}, awsError(err)
	}
	sum := sha256.Sum256(body)
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), "ssm", s.region, time.Now()); err != nil {
		return Secret{}, fmt.Errorf("sign the SSM request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Secret{}, err
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Secret{}, err
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &ssmAPIError{}
		_ = json.Unmarshal(raw, apiErr)
		apiErr.Code = apiErr.Type
		if i := strings.LastIndexByte(apiErr.Code, '#'); i >= 0 {
			apiErr.Code = apiErr.Code[i+1:]
		}
		if apiErr.Code == "" {
			apiErr.Code = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return Secret{}, ssmError(apiErr)
	}
	var out struct {
		Parameter struct {
			Value   string `json:"Value"`
			Version int64  `json:"Version"`
		} `json:"Parameter"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Secret{}, fmt.Errorf("read the SSM response: %w", err)
	}
	if !utf8.ValidString(out.Parameter.Value) {
		return Secret{}, errors.New("the parameter is not valid text")
	}
	return Secret{Value: []byte(out.Parameter.Value), Version: fmt.Sprint(out.Parameter.Version)}, nil
}

func (s *ssmStore) Close() error { return nil }

// ssmError maps SSM's error types onto the reasons a reference names.
func ssmError(e *ssmAPIError) error {
	switch e.Code {
	case "ParameterNotFound", "ParameterVersionNotFound", "ParameterVersionLabelLimitExceeded":
		return fmt.Errorf("%w (%v)", ErrNotFound, e)
	case "AccessDeniedException", "UnrecognizedClientException", "InvalidSignatureException", "ExpiredTokenException":
		return fmt.Errorf("%w (%v)", ErrPermission, e)
	case "InvalidKeyId":
		return fmt.Errorf("%w: the KMS key could not decrypt the parameter (%s)", ErrPermission, e.Message)
	}
	return e
}
