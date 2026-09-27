package engine

import (
	"context"
	"fmt"
	"io"
	pathpkg "path"
	"path/filepath"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/sftpclient"
)

/*
 * One seam for every remote file location a file node can use.
 *
 * ADR-040 gave file nodes a remote location with SFTP as the only
 * transport, and the seam was SFTP-shaped: openFileConnection returned a
 * concrete SFTP client. S3 then arrived as `if conn.Type == S3 { ... }`
 * branches inside both deliverRemote and sourceFileLocal, so every new
 * transport meant another copy of those branches in both places -- the
 * parallel path #686 and #687 ask not to build.
 *
 * A transport answers two questions: fetch this remote file to a local
 * path, and write this remote file so that it appears only once the write
 * succeeded. Everything else -- dry runs, data directories, logging,
 * lineage, the no-local-fallback rule -- stays in file_location.go, once.
 */

// fileTransport moves one file between this machine and the remote
// location a file node's connection names.
type fileTransport interface {
	// scheme names the transport in asset URIs and messages.
	scheme() string
	// download fetches remotePath into dir, which the caller created and
	// owns. It returns the local path, how to describe what was fetched in
	// the run log, and the byte count.
	download(ctx context.Context, remotePath, dir string) (local, fetched string, n int64, err error)
	// upload writes remotePath through write. The bytes must not become
	// visible at remotePath unless write returned nil: a failed write fails
	// the run and never leaves a partial file where a reader would find it.
	// tag is unique per run and node, for transports that stage under a
	// temporary name.
	upload(ctx context.Context, remotePath, tag string, write func(io.Writer) error) (fileDelivery, error)
	close() error
}

// fileDelivery is where an upload landed.
type fileDelivery struct {
	bytes int64
	// path is the remote path as the server names it.
	path string
	// warning is set when the delivery was not atomic, for the run log.
	warning string
}

// openFileTransport resolves the connection a file node names and opens
// the transport for it. ctx is the node attempt's: its timeout, or the run
// being cancelled, ends a transfer in progress.
func (r *Runner) openFileTransport(ctx context.Context, node models.Node) (fileTransport, error) {
	connID := fileConnID(node)
	conn, err := r.resolveFileConnection(node)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	switch conn.Type {
	case models.ConnTypeSFTP:
		client, err := r.openFileConnection(ctx, node)
		if err != nil {
			return nil, err
		}
		return &sftpTransport{client: client}, nil
	case models.ConnTypeS3:
		client, err := newS3FileClient(ctx, conn)
		if err != nil {
			return nil, fmt.Errorf("conn_id %q: %w", connID, err)
		}
		return &s3Transport{client: client, connID: connID}, nil
	case models.ConnTypeAzureBlob:
		client, err := newAzureBlobFileClient(conn)
		if err != nil {
			return nil, fmt.Errorf("conn_id %q: %w", connID, err)
		}
		return &azureBlobTransport{client: client, connID: connID}, nil
	default:
		return nil, fmt.Errorf("conn_id %q is a %s connection; file nodes support %s", connID, conn.Type, fileTransportTypes)
	}
}

// fileTransportTypes is named in the refusal for any other connection
// type, so the message cannot fall behind the switch above.
const fileTransportTypes = "sftp, s3 and azure_blob"

// fileTransportScheme is the asset-URI scheme for a connection type, used
// where a message has to name the destination before anything is dialled.
func fileTransportScheme(t models.ConnectionType) string {
	switch t {
	case models.ConnTypeS3:
		return "s3"
	case models.ConnTypeAzureBlob:
		return "azblob"
	default:
		return "sftp"
	}
}

// sftpTransport is ADR-040's original transport, unchanged behind the
// interface.
type sftpTransport struct{ client *sftpclient.Client }

func (t *sftpTransport) scheme() string { return "sftp" }

func (t *sftpTransport) download(_ context.Context, remotePath, dir string) (string, string, int64, error) {
	// A fixed name with the remote file's extension: the loader is chosen
	// by extension alone, and the data-directory check refuses any path
	// containing "..", which an ordinary name like "q3..final.csv" does.
	local := filepath.Join(dir, "download"+pathpkg.Ext(remotePath))
	fetched, n, err := t.client.Download(remotePath, local)
	return local, fetched, n, err
}

func (t *sftpTransport) upload(_ context.Context, remotePath, tag string, write func(io.Writer) error) (fileDelivery, error) {
	res, err := t.client.Upload(remotePath, tag, write)
	if err != nil {
		return fileDelivery{}, err
	}
	d := fileDelivery{bytes: res.Bytes, path: res.Path}
	if !res.Atomic {
		d.warning = fmt.Sprintf("The server does not support posix-rename, so the previous %s was removed before the new one was renamed in: a reader polling the directory could have found no file for a moment", res.Path)
	}
	return d, nil
}

func (t *sftpTransport) close() error { return t.client.Close() }

// s3Transport is the customer-owned S3 connection, unchanged behind the
// interface. An S3 put is atomic: the object appears only when complete.
type s3Transport struct {
	client *s3FileClient
	connID string
}

func (t *s3Transport) scheme() string { return "s3" }

func (t *s3Transport) download(ctx context.Context, remotePath, dir string) (string, string, int64, error) {
	local := filepath.Join(dir, s3StagedFilename(remotePath))
	n, err := t.client.download(ctx, remotePath, local)
	return local, remoteFileAssetURI("s3", t.connID, remotePath), n, err
}

func (t *s3Transport) upload(ctx context.Context, remotePath, _ string, write func(io.Writer) error) (fileDelivery, error) {
	n, err := t.client.upload(ctx, remotePath, write)
	if err != nil {
		return fileDelivery{}, err
	}
	return fileDelivery{bytes: n, path: remotePath}, nil
}

func (t *s3Transport) close() error { return nil }
