package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/pkg/sftpclient"
)

// Where a file node's file lives (ADR-040).
//
// A source_file or sink_file without conn_id reads and writes this
// machine's filesystem, exactly as it always has. With conn_id naming an
// SFTP connection, path is a path on that server. Both sinks, batch and
// streamed, write through writeFileOutput, so there is no way for a node
// with conn_id to write to the local disk instead: a streamed run that
// "succeeded" by writing locally while the partner received nothing is
// the failure this layout exists to rule out.

// fileConnID is the connection a file node goes through, "" for a file on
// this machine.
func fileConnID(node models.Node) string {
	id, _ := node.Config["conn_id"].(string)
	return strings.TrimSpace(id)
}

// remoteFileAssetID is a remote file's identity in lineage and logs. The
// connection's id rather than its host, so drawing a graph never needs
// credentials, and distinct from a local file with the same path.
func remoteFileAssetID(connID, path string) string {
	return "sftp://" + connID + "/" + path
}

// SFTPConfig builds a client configuration from a resolved sftp
// connection. The connection test uses it too, so for settings stored on
// the connection a green test means the ones a run will use work, not a
// similar set. (Credentials given as references are resolved only by
// runs; the test does not see them.)
//
// The server's host key and a private key live in the connection's
// encrypted extra settings:
//
//	{"host_key": "SHA256:...", "private_key": "-----BEGIN OPENSSH PRIVATE KEY-----...",
//	 "passphrase": "...", "max_download_bytes": 10737418240,
//	 "insecure_skip_host_key_check": false}
func SFTPConfig(conn *models.Connection) (sftpclient.Config, error) {
	cfg := sftpclient.Config{
		Host:     conn.Host,
		Port:     conn.Port,
		User:     conn.Login,
		Password: conn.Password,
		BaseDir:  strings.TrimSpace(conn.Schema),
		Dial:     netguard.Outbound().DialContext,
	}
	if strings.TrimSpace(conn.Extra) == "" {
		return cfg, nil
	}
	var extra map[string]interface{}
	if err := json.Unmarshal([]byte(conn.Extra), &extra); err != nil {
		// The content is not quoted back: it holds the private key.
		return cfg, fmt.Errorf("the connection's extra settings are not a JSON object")
	}
	str := func(k string) string { s, _ := extra[k].(string); return s }
	cfg.HostKey = str("host_key")
	cfg.PrivateKey = str("private_key")
	cfg.Passphrase = str("passphrase")
	switch v := extra["max_download_bytes"].(type) {
	case nil:
	case float64:
		if v < 1 || v != float64(int64(v)) {
			return cfg, fmt.Errorf("max_download_bytes must be a positive whole number of bytes")
		}
		cfg.MaxDownloadBytes = int64(v)
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("max_download_bytes must be a positive whole number of bytes")
		}
		cfg.MaxDownloadBytes = n
	default:
		return cfg, fmt.Errorf("max_download_bytes must be a positive whole number of bytes")
	}
	switch v := extra["insecure_skip_host_key_check"].(type) {
	case bool:
		cfg.InsecureSkipHostKeyCheck = v
	case string:
		cfg.InsecureSkipHostKeyCheck = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return cfg, nil
}

// openFileConnection dials the SFTP connection a file node names. ctx is
// the node attempt's: its timeout, or the run being cancelled, closes the
// connection and ends a transfer in progress.
func (r *Runner) openFileConnection(ctx context.Context, node models.Node) (*sftpclient.Client, error) {
	connID := fileConnID(node)
	if r.connResolver == nil {
		return nil, fmt.Errorf("conn_id %q is set, but this runner has no connection store to resolve it", connID)
	}
	conn, err := r.connResolver.ResolveConnectionIn(connID, r.workspaceID())
	if err != nil {
		return nil, fmt.Errorf("conn_id %q: %w", connID, err)
	}
	if conn.Type != models.ConnTypeSFTP {
		return nil, fmt.Errorf("conn_id %q is a %s connection; a file node reads and writes through an sftp connection", connID, conn.Type)
	}
	cfg, err := SFTPConfig(conn)
	if err != nil {
		return nil, fmt.Errorf("conn_id %q: %w", connID, err)
	}
	cfg.Warn = func(msg string) { r.log(node.ID, models.LogLevelWarning, "%s", msg) }
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := sftpclient.Dial(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("conn_id %q: %w", connID, err)
	}
	return client, nil
}

// fileWrite says where a sink_file's output went.
type fileWrite struct {
	bytes int64
	// where is the local path, or the connection and the server's path.
	where  string
	remote bool
	// skipped is set when nothing was written: a dry run never delivers.
	skipped bool
}

// writeFileOutput is the one place a sink_file's output goes, for the
// batch and the streamed path alike. write receives the destination; on a
// remote destination its bytes land under a temporary name and appear at
// path only once write has returned without error.
func (r *Runner) writeFileOutput(ctx context.Context, node models.Node, path string, write func(io.Writer) error) (fileWrite, error) {
	if connID := fileConnID(node); connID != "" {
		return r.deliverRemote(ctx, node, connID, path, write)
	}

	if err := validateFilePath(path); err != nil {
		return fileWrite{}, fmt.Errorf("sink_file: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		// #nosec G301 -- the mode sink_file has always created it with.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fileWrite{}, fmt.Errorf("create output directory: %w", err)
		}
	}
	// #nosec G304,G302 -- path has been through validateFilePath; 0644 is
	// what the sink has always produced.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fileWrite{}, fmt.Errorf("write %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // closed explicitly on success

	counted := &countingWriter{w: bufio.NewWriterSize(f, encodeBufferSize)}
	if err := write(counted); err != nil {
		return fileWrite{}, err
	}
	if err := counted.w.Flush(); err != nil {
		return fileWrite{}, fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fileWrite{}, fmt.Errorf("write %s: %w", path, err)
	}
	return fileWrite{bytes: counted.n, where: path}, nil
}

func (r *Runner) deliverRemote(ctx context.Context, node models.Node, connID, path string, write func(io.Writer) error) (fileWrite, error) {
	asset := remoteFileAssetID(connID, path)
	if r.dryRun {
		// A preview must never hand a partner a truncated file.
		r.log(node.ID, models.LogLevelInfo, "Dry run: nothing delivered; a run would write %s", asset)
		return fileWrite{where: asset, remote: true, skipped: true}, nil
	}

	client, err := r.openFileConnection(ctx, node)
	if err != nil {
		return fileWrite{}, fmt.Errorf("sink_file: %w", err)
	}
	defer client.Close() //nolint:errcheck

	// Unique per run and node, so two runs (or two nodes of one run)
	// delivering the same file never write into each other's temporary.
	tag := node.ID
	if r.run != nil && r.run.ID != "" {
		tag = r.run.ID + "-" + node.ID
	}
	res, err := client.Upload(path, tag, write)
	if err != nil {
		return fileWrite{}, fmt.Errorf("sink_file: deliver %s: %w", asset, err)
	}
	if !res.Atomic {
		r.log(node.ID, models.LogLevelWarning,
			"The server does not support posix-rename, so the previous %s was removed before the new one was renamed in: a reader polling the directory could have found no file for a moment",
			res.Path)
	}
	return fileWrite{bytes: res.Bytes, where: connID + ":" + res.Path, remote: true}, nil
}

// sourceFileLocal returns a path on this machine holding the file a
// source_file node reads, and a cleanup to call when the node is done
// with it. A remote file is downloaded to a temporary file with the same
// name, so the loader chosen by its extension is the one a local file
// would get.
func (r *Runner) sourceFileLocal(ctx context.Context, node models.Node, path string) (local string, cleanup func(), remote bool, err error) {
	connID := fileConnID(node)
	if connID == "" {
		if err := validateFilePath(path); err != nil {
			return "", nil, false, fmt.Errorf("source_file: %w", err)
		}
		return path, func() {}, false, nil
	}

	client, err := r.openFileConnection(ctx, node)
	if err != nil {
		return "", nil, true, fmt.Errorf("source_file: %w", err)
	}
	defer client.Close() //nolint:errcheck

	dir, err := downloadScratchDir()
	if err != nil {
		return "", nil, true, fmt.Errorf("source_file: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	// A fixed name with the remote file's extension: the loader is chosen
	// by extension alone, and the data-directory check refuses any path
	// containing "..", which an ordinary name like "q3..final.csv" does.
	local = filepath.Join(dir, "download"+pathpkg.Ext(path))
	remotePath, n, err := client.Download(path, local)
	if err != nil {
		cleanup()
		return "", nil, true, fmt.Errorf("source_file: fetch %s: %w", remoteFileAssetID(connID, path), err)
	}
	r.log(node.ID, models.LogLevelInfo, "Fetched %s from %s (%s)", remotePath, connID, humanBytes(n))
	return local, cleanup, true, nil
}

// downloadScratchDir makes a private directory for a downloaded file in
// the first data directory that accepts one. The loaders read only inside
// the data directories, so a copy kept anywhere else, the system temp
// directory included, could not be loaded by a deployment that narrowed
// them. It is also where an operator expects large files to go.
func downloadScratchDir() (string, error) {
	var tried []string
	for _, d := range common.DataDirs() {
		dir, err := os.MkdirTemp(d, ".brokoli-sftp-")
		if err == nil {
			return dir, nil
		}
		tried = append(tried, err.Error())
	}
	return "", fmt.Errorf("no data directory can hold a downloaded file: %s", strings.Join(tried, "; "))
}
