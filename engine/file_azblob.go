package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

/*
 * Azure Blob Storage as a file-node transport (#687).
 *
 * The third implementation of fileTransport, beside SFTP and S3. The
 * configuration lives in the connection's extra JSON, which is encrypted
 * at rest and blanked in API responses, the same path S3's secret key
 * takes; nothing here puts a credential into a URL that is logged.
 *
 * The field names are the ones the connection catalogue has documented
 * since before any node could use this type -- container, account, key --
 * so a connection saved against that hint works without editing.
 *
 * Authentication is an account key or a SAS token. A service principal
 * is refused by name rather than half-supported: it needs a tenant to
 * test against, and the emulator used for everything else here cannot
 * stand in for Entra ID.
 */

const defaultAzureBlobDownloadLimit int64 = 10 << 30

// AzureBlobFileConfig is a customer-owned Azure Blob Storage location.
type AzureBlobFileConfig struct {
	Account   string
	Container string
	// Exactly one of AccountKey and SASToken is set.
	AccountKey string
	SASToken   string
	// Endpoint is the service URL. Empty means the public cloud's
	// https://<account>.blob.core.windows.net/; set it for a sovereign
	// cloud, a private endpoint, or the Azurite emulator.
	Endpoint         string
	MaxDownloadBytes int64
}

// AzureBlobFileConfigFromExtra parses and validates the extra JSON of an
// azure_blob connection.
func AzureBlobFileConfigFromExtra(extra string) (AzureBlobFileConfig, error) {
	var raw map[string]interface{}
	if strings.TrimSpace(extra) == "" {
		return AzureBlobFileConfig{}, fmt.Errorf("Azure Blob extra config is required")
	}
	if err := json.Unmarshal([]byte(extra), &raw); err != nil {
		return AzureBlobFileConfig{}, fmt.Errorf("Azure Blob extra config must be a JSON object: %w", err)
	}
	str := func(key string) string {
		value, _ := raw[key].(string)
		return strings.TrimSpace(value)
	}
	for _, spKey := range []string{"tenant_id", "client_id", "client_secret"} {
		if str(spKey) != "" {
			return AzureBlobFileConfig{}, fmt.Errorf(
				"Azure Blob service principal authentication (%s) is not supported yet; use an account key (key) or a SAS token (sas_token)", spKey)
		}
	}
	cfg := AzureBlobFileConfig{
		Account:          str("account"),
		Container:        str("container"),
		AccountKey:       str("key"),
		SASToken:         strings.TrimPrefix(str("sas_token"), "?"),
		Endpoint:         str("endpoint"),
		MaxDownloadBytes: defaultAzureBlobDownloadLimit,
	}
	if cfg.Account == "" {
		return AzureBlobFileConfig{}, fmt.Errorf("Azure Blob extra config requires account")
	}
	if !validAzureAccountName(cfg.Account) {
		return AzureBlobFileConfig{}, fmt.Errorf("Azure Blob account must be 3-24 lowercase letters and digits")
	}
	if cfg.Container == "" {
		return AzureBlobFileConfig{}, fmt.Errorf("Azure Blob extra config requires container")
	}
	if !validAzureContainerName(cfg.Container) {
		return AzureBlobFileConfig{}, fmt.Errorf("Azure Blob container name is invalid: 3-63 lowercase letters, digits and single hyphens, starting and ending with a letter or digit")
	}
	switch {
	case cfg.AccountKey != "" && cfg.SASToken != "":
		return AzureBlobFileConfig{}, fmt.Errorf("Azure Blob key and sas_token are alternatives; set one")
	case cfg.AccountKey == "" && cfg.SASToken == "":
		return AzureBlobFileConfig{}, fmt.Errorf("Azure Blob extra config requires key (an account key) or sas_token")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://" + cfg.Account + ".blob.core.windows.net/"
	} else {
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return AzureBlobFileConfig{}, fmt.Errorf("Azure Blob endpoint must be an http or https URL")
		}
		if u.RawQuery != "" {
			// A SAS token pasted into the endpoint would be logged with it.
			return AzureBlobFileConfig{}, fmt.Errorf("Azure Blob endpoint must not carry a query string; put a SAS token in sas_token")
		}
	}
	if value, ok := raw["max_download_bytes"]; ok {
		n, err := positiveWholeNumber(value)
		if err != nil {
			return AzureBlobFileConfig{}, fmt.Errorf("Azure Blob max_download_bytes must be a positive whole number")
		}
		cfg.MaxDownloadBytes = n
	}
	return cfg, nil
}

func positiveWholeNumber(value interface{}) (int64, error) {
	switch v := value.(type) {
	case float64:
		if v < 1 || v != float64(int64(v)) {
			return 0, errors.New("not a positive whole number")
		}
		return int64(v), nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || n < 1 {
			return 0, errors.New("not a positive whole number")
		}
		return n, nil
	}
	return 0, errors.New("not a positive whole number")
}

func validAzureAccountName(name string) bool {
	if len(name) < 3 || len(name) > 24 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func validAzureContainerName(name string) bool {
	if len(name) < 3 || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for i, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
		if r == '-' && i > 0 && name[i-1] == '-' {
			return false
		}
	}
	return true
}

type azureBlobFileClient struct {
	container *container.Client
	name      string
	limit     int64
}

func newAzureBlobFileClient(conn *models.Connection) (*azureBlobFileClient, error) {
	cfg, err := AzureBlobFileConfigFromExtra(conn.Extra)
	if err != nil {
		return nil, err
	}
	// Every request goes through the deployment's outbound policy, as SFTP
	// and S3 do (ADR-040, section 8).
	opts := &azblob.ClientOptions{ClientOptions: azcore.ClientOptions{
		Transport: netguard.Outbound().Client(0),
		// Refused credentials are not retried, but a refused destination
		// is: with the SDK's defaults a blocked address takes several
		// backoffs to report. Two tries is enough to ride out a blip.
		Retry: policy.RetryOptions{MaxRetries: 2},
	}}
	serviceURL := strings.TrimRight(cfg.Endpoint, "/") + "/"

	var client *azblob.Client
	if cfg.AccountKey != "" {
		cred, err := azblob.NewSharedKeyCredential(cfg.Account, cfg.AccountKey)
		if err != nil {
			return nil, fmt.Errorf("Azure Blob key is not a valid account key: %w", err)
		}
		client, err = azblob.NewClientWithSharedKeyCredential(serviceURL, cred, opts)
		if err != nil {
			return nil, fmt.Errorf("create Azure Blob client: %w", err)
		}
	} else {
		client, err = azblob.NewClientWithNoCredential(serviceURL+"?"+cfg.SASToken, opts)
		if err != nil {
			return nil, fmt.Errorf("create Azure Blob client: %w", err)
		}
	}
	return &azureBlobFileClient{
		container: client.ServiceClient().NewContainerClient(cfg.Container),
		name:      cfg.Container,
		limit:     cfg.MaxDownloadBytes,
	}, nil
}

// TestAzureBlobConnection authenticates for real and lists the configured
// container, through the same client file nodes use at runtime. A
// connection test that only checked the fields would pass a wrong key.
func TestAzureBlobConnection(ctx context.Context, extra map[string]interface{}) error {
	encoded, err := json.Marshal(extra)
	if err != nil {
		return fmt.Errorf("encode Azure Blob config: %w", err)
	}
	client, err := newAzureBlobFileClient(&models.Connection{Extra: string(encoded)})
	if err != nil {
		return err
	}
	return client.checkContainer(ctx)
}

func (c *azureBlobFileClient) checkContainer(ctx context.Context) error {
	one := int32(1)
	pager := c.container.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{MaxResults: &one})
	if _, err := pager.NextPage(ctx); err != nil {
		return fmt.Errorf("list Azure Blob container %q: %w", c.name, explainAzureError(err))
	}
	return nil
}

func (c *azureBlobFileClient) download(ctx context.Context, blobName, destination string) (int64, error) {
	if blobName == "" {
		return 0, fmt.Errorf("Azure Blob name is required")
	}
	blob := c.container.NewBlobClient(blobName)
	props, err := blob.GetProperties(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("read Azure blob %q: %w", blobName, explainAzureError(err))
	}
	if props.ContentLength != nil && *props.ContentLength > c.limit {
		return 0, fmt.Errorf("Azure blob %q is %d bytes, over the %d-byte download limit", blobName, *props.ContentLength, c.limit)
	}
	resp, err := blob.DownloadStream(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("download Azure blob %q: %w", blobName, explainAzureError(err))
	}
	defer resp.Body.Close() //nolint:errcheck

	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return 0, fmt.Errorf("create Azure Blob download directory: %w", err)
	}
	f, err := os.Create(destination) // #nosec G304 -- destination is a fixed filename inside a process-created scratch directory.
	if err != nil {
		return 0, fmt.Errorf("create Azure Blob download: %w", err)
	}
	defer f.Close() //nolint:errcheck
	counted := &countingWriter{w: f}
	// The size was checked above, but the blob can be replaced between the
	// two requests: the limit is enforced on the bytes actually read.
	if _, err := io.Copy(counted, io.LimitReader(resp.Body, c.limit+1)); err != nil {
		return 0, fmt.Errorf("download Azure blob %q: %w", blobName, err)
	}
	if counted.n > c.limit {
		return 0, fmt.Errorf("Azure blob %q exceeds the %d-byte download limit", blobName, c.limit)
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("finish Azure Blob download: %w", err)
	}
	return counted.n, nil
}

// upload streams write into a block blob. UploadStream stages blocks and
// commits them only after the body has been read to EOF; a body that
// fails makes it return before the commit, so neither a new blob nor a
// replacement of an existing one ever appears from a failed write. Blocks
// staged before the failure stay uncommitted and are discarded by the
// service.
func (c *azureBlobFileClient) upload(ctx context.Context, blobName string, write func(io.Writer) error) (int64, error) {
	if blobName == "" {
		return 0, fmt.Errorf("Azure Blob name is required")
	}
	reader, writer := io.Pipe()
	result := make(chan error, 1)
	go func() {
		_, err := c.container.NewBlockBlobClient(blobName).UploadStream(ctx, reader, nil)
		// Unblock the writer if the upload stopped reading early.
		_ = reader.CloseWithError(err)
		result <- err
	}()

	counted := &countingWriter{w: writer}
	writeErr := write(counted)
	if writeErr != nil {
		_ = writer.CloseWithError(writeErr)
	} else {
		_ = writer.Close()
	}
	uploadErr := <-result
	// Which failure is the cause decides the message. When the node's own
	// write failed, UploadStream stops on that same error from the pipe,
	// so it is reported as the node's. When the service failed first, the
	// write then fails on the closed pipe, and reporting that would bury
	// the service's reason -- a refused key, a missing container.
	if writeErr != nil && (uploadErr == nil || errors.Is(uploadErr, writeErr)) {
		return 0, writeErr
	}
	if uploadErr != nil {
		return 0, fmt.Errorf("upload Azure blob %q: %w", blobName, explainAzureError(uploadErr))
	}
	return counted.n, nil
}

// explainAzureError puts the storage error code first, where it says what
// to fix, ahead of the SDK's long request dump.
func explainAzureError(err error) error {
	switch {
	case bloberror.HasCode(err, bloberror.BlobNotFound):
		return fmt.Errorf("the blob does not exist (BlobNotFound)")
	case bloberror.HasCode(err, bloberror.ContainerNotFound):
		return fmt.Errorf("the container does not exist (ContainerNotFound)")
	case bloberror.HasCode(err, bloberror.AuthenticationFailed):
		return fmt.Errorf("the key or SAS token was refused (AuthenticationFailed)")
	case bloberror.HasCode(err, bloberror.AuthorizationPermissionMismatch, bloberror.AuthorizationResourceTypeMismatch):
		return fmt.Errorf("the credentials do not grant this operation (%w)", err)
	case bloberror.HasCode(err, bloberror.AuthorizationFailure):
		// Azure reports a wrong key as AuthenticationFailed; the Azurite
		// emulator reports the same thing as AuthorizationFailure, which is
		// also what an expired SAS gets. Name both causes.
		return fmt.Errorf("the key or SAS token was refused, or does not grant this operation (AuthorizationFailure)")
	}
	return err
}

// azureBlobTransport is the fileTransport for an azure_blob connection.
type azureBlobTransport struct {
	client *azureBlobFileClient
	connID string
}

func (t *azureBlobTransport) scheme() string { return "azblob" }

func (t *azureBlobTransport) download(ctx context.Context, remotePath, dir string) (string, string, int64, error) {
	local := filepath.Join(dir, s3StagedFilename(remotePath))
	n, err := t.client.download(ctx, remotePath, local)
	return local, remoteFileAssetURI("azblob", t.connID, remotePath), n, err
}

func (t *azureBlobTransport) upload(ctx context.Context, remotePath, _ string, write func(io.Writer) error) (fileDelivery, error) {
	n, err := t.client.upload(ctx, remotePath, write)
	if err != nil {
		return fileDelivery{}, err
	}
	return fileDelivery{bytes: n, path: remotePath}, nil
}

func (t *azureBlobTransport) close() error { return nil }
