# ADR-040: Remote file locations for file nodes

**Status:** proposed
**Date:** 2026-09-15

## Context

Pipelines need to deliver files to, and collect files from, SFTP
servers. It is the standard way data is exchanged with partners who will
not grant database access.

Brokoli appeared to support this, and did not:

- `sftp` has been a connection type since the connection model was
  written, and the README lists it among the seven.
- Its connection test opened a TCP connection and read the SSH banner.
  It never authenticated, so a wrong password tested green. The code
  said so: "full password auth requires golang.org/x/crypto/ssh which we
  don't import yet".
- The UI marked `sftp` as usable by nodes. No node read a connection of
  that type; the engine never mentioned SFTP.

S3 was in the same position: listed as usable by nodes, used by none.
The only S3 code is the artifact store that holds intermediate outputs.

So there is no remote file access in any file node today, and whatever
SFTP establishes is the pattern the next remote store follows.

### What the file nodes do now

`source_file` hands a local path to a loader chosen by its extension
(CSV, JSON, XML, Excel). `sink_file` encodes the whole output to bytes
and writes them with `os.WriteFile`. Both also have a streaming path
(ADR-019) for data too large to hold: the streamed sink writes through
an `io.Writer`, the streamed source reads a local file incrementally.

## Decision

### 1. A connection on the existing file nodes, not new node types

`source_file` and `sink_file` gain an optional `conn_id`. When it names
an SFTP connection, `path` is a path on that server.

New node types (`source_sftp`, `sink_sftp`) were considered and
rejected. A node type ripples through the lineage coverage gate,
validation, the IR, both SDKs and the UI palette, and would duplicate
every format the file nodes already handle. The file nodes' job is
"read or write a file in a format"; where the file lives is a property
of the location, and a connection is what describes a location.

`config` is free-form in the IR, so `conn_id` needs no schema change. A
file node without `conn_id` behaves exactly as before.

### 2. Paths

- A relative `path` resolves against the connection's base directory
  (its `schema` field), or the login directory when that is empty.
- An absolute `path` is used as given, **but when the connection sets a
  base directory it must be inside it**. The base directory is a
  boundary, not only a starting point: a `..` guard that an absolute
  path could walk around would guard nothing. With no base directory,
  the account's permissions on the server are the only boundary.
- A path containing a `..` segment is refused. The server's own
  permissions are the real boundary, but a pipeline that can walk out of
  the directory it was configured for is a surprise nobody wants to
  discover in an audit.
- The local data-directory check (`validateFilePath`) does not apply to
  remote paths: it guards this machine's filesystem, which a remote path
  never touches.

Variables in the path resolve as they do today, so
`/outbound/orders-${interval.start}.csv` works.

### 3. Authentication

Password (the connection's password reference) or a private key, or
both. The key and its passphrase live in the connection's encrypted
extra configuration, never in plaintext storage. SSH agents and
certificates are deferred.

### 4. The server's host key is verified, and that is not optional by default

The connection stores the server's host key fingerprint
(`SHA256:...`, as `ssh-keygen -lf` prints it), or its public key line.

- **A mismatch refuses to connect.** It is the signal that something
  other than the intended server answered.
- **A missing fingerprint refuses to connect**, and the error names the
  fingerprint the server actually presented, so setting it up is one
  copy and paste after verifying it out of band.
- Skipping the check requires setting `insecure_skip_host_key_check`
  explicitly. It is logged as a warning on every connection, so it
  stays visible rather than being a setting somebody forgot.

Trusting any host key is the common default in scripts and the wrong one
here: a delivery pipeline sends data to whatever answers, so anyone able
to intercept the connection receives the file.

A `known_hosts` line with a marker (`@revoked`, `@cert-authority`) is
refused. A configured public key or `known_hosts` line pins host key
negotiation to that key's type; otherwise a server with several keys
presents its preferred one and the check reports a mismatch, which
trains operators to paste whatever the server shows.

Only the algorithms `golang.org/x/crypto/ssh` does not classify as
insecure are offered (`ssh.SupportedAlgorithms()`). Its defaults still
include SHA-1 key exchange and DSA host keys for compatibility; a server
offering nothing better is refused.

### 5. Delivery is atomic

A file is uploaded to a temporary, hidden name in the destination
directory (`.<name>.<run-id>-<node-id>-<random>.part`) and renamed into
place only once it is complete. The node id is part of the name so that
two nodes of one run delivering the same file cannot write into each
other's temporary. The random part makes the name unguessable, and the
temporary is opened with `O_EXCL`: on a server shared with other
accounts, a file or symlink planted at the name makes the delivery fail
instead of writing through it. A temporary cannot be removed once the
connection is lost, so it stays behind in that one case, hidden and
never under the real name. A partner polling the directory never picks up a
half-written file, and a failed run leaves no partial file behind: the
temporary file is removed on failure.

The rename uses the `posix-rename@openssh.com` extension where the
server supports it, which replaces an existing file atomically. Where it
does not, the existing file is removed and the new one renamed in, and
the run log says so, because that leaves a moment with no file; and if
that rename then fails, the old file is already gone.

Parent directories are created as needed.

### 6. Reading

A remote file is downloaded to a local temporary file with the same
extension, and the existing loaders read it. Every format the local
source supports works remotely from the start. A download is capped at
the connection's `max_download_bytes` (10 GiB by default), checked
against the declared size and again against what actually arrives, so a
server cannot fill the shared data directory. The temporary file is
removed when the node finishes.

The copy is kept in a private directory (`.brokoli-sftp-*`) inside the
first data directory that accepts one, not in the system temp
directory. The loaders only open files inside the data directories, so
on a deployment that narrowed `BROKOLI_DATA_DIRS` a copy in `/tmp` could
never be loaded; the data directories are also where an operator already
expects large files. The copy is named `download` plus the remote
file's extension, because the loader is chosen by extension and the
data-directory check refuses any path containing `..`, which ordinary
names such as `q3..final.csv` do.

### 7. One destination step for both execution paths

The batch and streaming sinks both obtain their output writer from one
place, which returns either the local file or the remote one. There is
no path by which a file node with `conn_id` writes to the local disk:
the failure this ADR exists to prevent would otherwise be a streaming
run that "succeeds" by writing locally while the partner receives
nothing.

### 8. Network policy

SSH connections are dialed through the same outbound policy as HTTP
connectors (`pkg/netguard`), through a dialer shared with the HTTP
client so the two cannot diverge: the resolved IP is checked, and that
exact IP is dialed. Private and loopback targets need the same opt-in
they need for HTTP; cloud metadata endpoints stay blocked.

### 9. Lineage

A remote file's asset identity is `sftp://<conn_id>/<path>`, distinct
from a local file with the same path. The connection's slug is used
rather than its host, so identity does not depend on resolving
credentials to draw a graph.

### 10. The connection test is real

Testing an SFTP connection dials through the network policy, verifies
the host key, authenticates, opens the SFTP subsystem, and checks the
base directory exists. An unknown host key fails with the server's
fingerprint in the message.

Because the test now authenticates, it would send the stored password to
whatever host the connection names. Changing a connection's type, host
or port therefore requires entering its password again, for every
connection type, and its extra settings too unless they are a database's
driver options: a stored secret is never carried over to a different
server.

### 11. Timeouts and cancellation

The handshake is bounded as a whole. After it, a read or write that
waits longer than an idle timeout (two minutes) fails the connection, and
keepalives at a third of that keep a connection that is only quiet on
this side alive. The connection's lifetime is the node attempt's
context, so a node timeout or a cancelled run closes it and ends a
blocked transfer. Without these, a server that stopped responding would
hold a node, and its worker, for as long as the TCP connection lasted.

### 12. Workers

A file node on a remote worker resolves its connection through the
existing connection resolution, which reaches the control plane.

## Consequences

### Positive

- Pipelines can deliver files to, and collect files from, SFTP servers
  in every format the file nodes support.
- The connection test means what it says.
- The seam in section 7 is where the next remote store (S3) plugs in.

### Negative

- A host key must be obtained and pasted in before a connection works.
  That is deliberate friction, and the test turns it into one step.
- Downloading to a temporary file before loading costs disk in the first
  writable data directory equal to the file's size. A worker killed
  mid-read leaves that copy behind.
- On servers without `posix-rename`, replacement is not atomic.

### Deferred

- S3 and other remote stores through the same seam.
- Picking up files by pattern or wildcard, and acting on files after
  pickup (moving or deleting them).
- FTPS.
- SSH agent and certificate authentication.
- A delivery receipt beyond the run log: checksum or size read back from
  the server after the rename.
