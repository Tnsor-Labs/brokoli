# Delivering and collecting files over SFTP

A `sink_file` node can deliver its output to an SFTP server, and a
`source_file` node can read its input from one. It is the usual way to
exchange files with a partner who will not grant database access: the
partner gives you an account on their server, or you give them one on
yours.

The file nodes do this themselves. There is no separate SFTP node: every
format a file node reads or writes locally works over SFTP too. The
design is recorded in
[ADR-040](adr/040-remote-file-locations-for-file-nodes.md).

- [1. Create the connection](#1-create-the-connection)
- [2. Get the server's host key](#2-get-the-servers-host-key)
- [3. Test the connection](#3-test-the-connection)
- [4. Allow the server through the network policy](#4-allow-the-server-through-the-network-policy)
- [5. Deliver a file](#5-deliver-a-file)
- [6. Collect a file](#6-collect-a-file)
- [Paths](#paths)
- [What delivery guarantees](#what-delivery-guarantees)
- [Timeouts and cancellation](#timeouts-and-cancellation)
- [Security](#security)
- [Dry runs and previews](#dry-runs-and-previews)
- [Workers](#workers)
- [Lineage](#lineage)
- [Troubleshooting](#troubleshooting)
- [Not supported yet](#not-supported-yet)

## 1. Create the connection

Create a connection of type **SFTP / SSH**.

| Field | Meaning |
| --- | --- |
| Host | The server's name or address. |
| Port | 22 when empty. |
| Base directory | Where relative paths in file nodes start, for example `/upload`, and the directory every path must stay inside. Empty means relative paths start in the account's login directory and absolute paths are limited only by the account's permissions on the server. |
| Login | The account name. |
| Password | The account's password, if it signs in with one. |
| Extra settings | A JSON object holding the host key and, for key authentication, the private key. Encrypted at rest like the password. |

The extra settings:

```json
{
  "host_key": "SHA256:x5b0aF3Nf0pWj8wq3n7Zb3hUQy8mE3l2kYVwqXcG1Ts",
  "private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\n...\n-----END OPENSSH PRIVATE KEY-----\n",
  "passphrase": "only if the private key is encrypted"
}
```

| Key | Required | Meaning |
| --- | --- | --- |
| `host_key` | yes | The server's host key. See [step 2](#2-get-the-servers-host-key). |
| `private_key` | for key authentication | An OpenSSH or PEM private key. In JSON the line breaks are written `\n`. |
| `passphrase` | if the key is encrypted | The key's passphrase. |
| `max_download_bytes` | no | The largest file a `source_file` may fetch through this connection, in bytes. 10 GiB (`10737418240`) when unset. |
| `insecure_skip_host_key_check` | no | `true` accepts any host key. See below before using it. |

A connection may have a password, a private key, or both; each is
offered to the server. With neither, the connection is refused before
anything is sent.

**Changing a connection's host, port or type requires entering its
password and extra settings again.** Stored secrets cannot be read back
through the API, and they are never carried over to a different server:
otherwise anyone allowed to edit the connection could point it at a
server of their own and have the next test or run send them there. (A
database connection's extra settings are driver options such as
`sslmode`, not secrets, and are kept; its password is not.)

## 2. Get the server's host key

Every connection checks that the server it reached is the one you
configured, by comparing the key the server presents with `host_key`.
A connection whose key does not match, or that has no key configured,
**refuses to connect**. Nothing is sent.

This matters more for delivery than for most connections: a pipeline
sends data to whatever answers. Without the check, anyone able to
intercept the connection would receive the file.

The easiest way to obtain the key is to let Brokoli show it to you:

1. Save the connection without `host_key`.
2. Press **Test**. It fails, and the message names the key the server
   presented:

   ```
   sftp: the server's host key is not configured; the server presented
   SHA256:x5b0aF3Nf0pWj8wq3n7Zb3hUQy8mE3l2kYVwqXcG1Ts -- verify it with
   the server's operator, then set it as the connection's host key
   ```

3. **Confirm that fingerprint with the server's operator** over a
   channel you already trust: a call, a ticket, their documentation.
   This step is the whole point. A fingerprint copied from the very
   connection you are trying to verify proves nothing on its own.
4. Put it in `host_key` and test again.

You can also read the key yourself from a machine you trust:

```sh
ssh-keyscan -t ed25519 sftp.partner.example | ssh-keygen -lf -
# 256 SHA256:x5b0aF3Nf0pWj8wq3n7Zb3hUQy8mE3l2kYVwqXcG1Ts sftp.partner.example (ED25519)
```

`host_key` accepts any of these forms:

- a fingerprint, `SHA256:...`, as `ssh-keygen -lf` prints it;
- a public key line, `ssh-ed25519 AAAAC3Nza...`;
- a line from a `known_hosts` file. A line marked `@revoked` or
  `@cert-authority` is refused: the first names a key that must not be
  trusted, the second a signer rather than the server's own key.

A value that is none of these is refused as malformed, not treated as
missing, so a mistyped key cannot quietly turn the check off.

**Servers with several host keys.** A public key line or `known_hosts`
line pins the connection to that key's type, so the server presents
that key. A fingerprint cannot say which type it belongs to, so it is
compared with whichever key the server prefers, which is the one the
Test button shows. Configure the fingerprint Test shows, or a full key
line.

**When the partner changes their server's key**, deliveries start
failing with `does not match the configured one`, naming both keys.
Confirm the new key with them and update `host_key`.

### Skipping the check

`"insecure_skip_host_key_check": true` accepts any host key. It exists
for test servers whose keys are regenerated constantly. Every
connection made with it logs a warning naming the key it accepted, so
it stays visible in every run that uses it rather than being a setting
somebody forgot. Do not use it for a real partner.

## 3. Test the connection

**Test** does what a run does:

1. connects through the outbound network policy;
2. checks the host key;
3. signs in;
4. opens the SFTP subsystem;
5. checks that the base directory exists and is a directory.

A green test means a pipeline using this connection can reach the
server and sign in. Test uses the password and extra settings stored on
the connection; credentials given as references to an external secret
store are resolved only by runs, so Test reports them as missing. It does not check that the account may write where
your pipeline writes; the server's own permissions decide that, and a
denied write fails the run with the server's error.

## 4. Allow the server through the network policy

SSH connections go through the same outbound policy as HTTP
connections. Public addresses are allowed. Private, loopback, link-local
and shared ranges (including `100.64.0.0/10`, where Tailscale addresses
live) are refused unless the operator allows them. Cloud metadata
endpoints are refused even then, unless an allowlisted CIDR names that
exact address. An IPv6 address that routes to an IPv4 one (NAT64, 6to4)
is judged as that IPv4 address.

A partner's server on the internet needs nothing. For a server on your
own network, set one of these on the Brokoli server and its workers:

| Variable | Effect |
| --- | --- |
| `BROKOLI_OUTBOUND_ALLOW_CIDRS=10.20.0.0/16` | Allows the listed ranges, comma-separated. Prefer this: it allows only what you name. For a server reached over Tailscale, `100.64.0.0/10`. |
| `BROKOLI_OUTBOUND_ALLOW_PRIVATE=true` | Allows every private range. |

A refused connection fails with
`request target is a blocked private/internal address`, both in a run
and in the connection test.

## 5. Deliver a file

Set `conn_id` on a `sink_file` node to the connection's id. `path` is
then a path on the server.

```json
{
  "id": "deliver",
  "type": "sink_file",
  "name": "Deliver to partner",
  "config": {
    "conn_id": "partner-sftp",
    "path": "outbound/orders-${interval.start|date:YYYYMMDD}.csv",
    "format": "csv"
  }
}
```

In the editor, choose the connection under **Location** on the node's
form; "This server's data directories" is the local disk.

Every `sink_file` format works: `csv`, `json` and `sql`. `csv` and
`json` are streamed straight to the server when the input is large, so
the file is never held in memory on the way.

The run log says where the file went:

```
Wrote csv to orders-2026-09-15.csv (412 KB, 10238 rows)
  Full path: partner-sftp:/upload/outbound/orders-2026-09-15.csv
```

## 6. Collect a file

Set `conn_id` on a `source_file` node the same way:

```json
{
  "id": "rates",
  "type": "source_file",
  "name": "Partner rates",
  "config": {
    "conn_id": "partner-sftp",
    "path": "inbound/rates.json"
  }
}
```

The reader is chosen by the file's extension, exactly as for a local
file: `.csv`, `.json`, `.xml`, `.xlsx` and `.xls`. CSV is streamed.

The file is first downloaded, then read. Files larger than the
connection's `max_download_bytes` (10 GiB by default) are refused, both
when the server reports the size up front and when a file keeps growing
while it is read. The copy is kept in a private
directory named `.brokoli-sftp-*` inside the first data directory that
accepts one, and is removed when the node finishes, whether it succeeded
or not. It is kept there, rather than in the system temp directory,
because file readers only open files inside the data directories. Allow
for disk equal to the file's size in that directory. A worker killed
while reading leaves the copy behind.

The run log names the file it fetched:

```
Fetched /upload/inbound/rates.json from partner-sftp (60 KB)
Loaded 1204 rows, 6 columns from rates.json (.json, 60 KB)
```

## Paths

| Path | Resolves to |
| --- | --- |
| `outbound/orders.csv` | the connection's base directory, then `outbound/orders.csv` |
| `/upload/outbound/orders.csv` | exactly that path, when it is inside the base directory (`/upload` here) |
| `/etc/passwd`, `/upload-old/orders.csv` | refused when a base directory is set: `the path is outside the connection's base directory` |
| `/srv/exchange/orders.csv` with no base directory | exactly that path |
| `../orders.csv`, `a/../../b` | refused: `a path may not contain a '..' segment` |

- Variables resolve in the path as they do everywhere else, so
  `${interval.start}` and `${var.partner_dir}` work. A timestamp also takes
  filters for the format and the offset a filename needs:
  `${interval.start|date:YYYYMMDD}` delivers `orders-20240314.csv`, and
  `${interval.start|shift:-1d|date:YYYY-MM-DD}` names the day before.
  Unfiltered, the value is RFC3339 (`2024-03-14T00:00:00Z`), colons
  included, which is rarely what a partner wants in a filename. The tokens
  are `YYYY`, `YY`, `MM`, `DD`, `HH`, `mm`, `ss`; a shift is a signed count
  and one of `s`, `m`, `h`, `d`, `w`. A filter the resolver cannot satisfy
  leaves the reference visible in the path instead of inventing a date.
- **Set a base directory to confine a connection.** With one set, no
  path in any pipeline can leave it, absolute or relative. Symlinks on
  the server that point outside it are the server's to control.
- A name that merely contains dots, such as `q3..final.csv`, is an
  ordinary file.
- The data-directory rule for local files (`BROKOLI_DATA_DIRS`) does
  not apply to a remote path; it governs this machine's disk, which a
  remote path never touches.
- Directories that do not exist yet are created on delivery.

## What delivery guarantees

**A partner never picks up a half-written file.** The file is uploaded
under a hidden temporary name in the destination directory,
`.<name>.<run-id>-<node-id>-<random>.part`, and renamed to its real name
only once every byte has been written. The random part makes the name
unguessable, and the temporary is created exclusively: on a server
shared with other accounts, a file or link somebody placed at that name
makes the delivery fail rather than write through it.

**A failed run leaves the destination as it was.** If encoding fails or
the server refuses a write, the temporary file is removed and the
previous delivery, if there was one, is untouched. If the connection
itself is lost, the temporary cannot be removed and stays behind under
its hidden `.part` name; it never takes the real name.

**Replacing an existing file is atomic when the server supports it.**
The rename uses the `posix-rename@openssh.com` extension, which OpenSSH
servers provide, and which replaces the old file in one step. A server
without it gets the old file removed and the new one renamed in, which
leaves a moment with no file. On such a server, if that last rename
fails, the old file is already gone: the run fails, and no file remains
until the next successful run. The run log says when replacement was not
atomic:

```
The server does not support posix-rename, so the previous
/upload/outbound/orders.csv was removed before the new one was renamed
in: a reader polling the directory could have found no file for a moment
```

**Nothing is written locally.** A file node with `conn_id` has no path
to the local disk, on either the batch or the streamed path.

## Timeouts and cancellation

- Opening the TCP connection is bounded at 10 seconds, and the SSH
  handshake after it at 15.
- A transfer fails when no data moves in either direction for two
  minutes, so a server that stops responding cannot hold a node, and the
  worker running it, open indefinitely. Brokoli sends SSH keepalives
  every 40 seconds, so a connection that is merely quiet on this side
  (waiting for rows from upstream, say) stays up.
- A node's `timeout` and a cancelled run close the connection, which
  ends a transfer in progress immediately.

## Security

- The server's identity is checked on every connection; see
  [step 2](#2-get-the-servers-host-key).
- Only algorithms Go's SSH library considers secure are offered. A
  server that supports nothing better than SHA-1 key exchange or DSA
  host keys is refused.
- The password, private key and passphrase are encrypted at rest with
  the connection and are never returned by the API. They are not written
  to run logs, and error messages do not quote the extra settings.
- Connections go through the outbound network policy.
- A downloaded copy is readable only by the Brokoli process's user
  (mode 0600, in a 0700 directory) and is removed when the node ends.

## Dry runs and previews

The editor's preview runs the pipeline on a few rows. A `sink_file`
with `conn_id` **does not connect at all** during a preview, so a
partner never receives a truncated file. A `source_file` with `conn_id`
does read from the server during a preview, since reading changes
nothing there.

## Workers

A file node running on a worker resolves its connection the same way
database and API nodes do. The worker connects to the SFTP server
itself, so the server must be reachable from the workers, and the
network policy variables above must be set on them too.

A remote file lives on its server, not on any worker's disk, so the
warnings about per-worker filesystems do not apply to it.

## Lineage

A remote file is its own asset in the lineage graph,
`sftp://<conn_id>/<path>`, distinct from a local file with the same
path. The connection's id is used rather than its host, so drawing the
graph never needs credentials.

## Troubleshooting

| Message | Cause and fix |
| --- | --- |
| `the server's host key is not configured; the server presented SHA256:...` | No `host_key`. Verify the named key with the server's operator and set it. |
| `the server's host key does not match the configured one: configured ..., server presented ...` | The server presented a different key. Either its key changed (confirm with the operator, then update `host_key`), or you did not reach the server you meant to. Do not simply paste the new key in without confirming it. |
| `the configured host key is not a SHA256 fingerprint, a public key line, or a known_hosts line` | `host_key` is malformed. Paste it again in one of the three forms. |
| `request target is a blocked private/internal address` | The server is on a private or loopback address. See [step 4](#4-allow-the-server-through-the-network-policy). |
| `the connection has neither a password nor a private key` | Set one. |
| `the private key could not be read` | The key is not OpenSSH or PEM, its line breaks were lost (in JSON they are `\n`), or the passphrase is wrong or missing. |
| `ssh: handshake failed: ssh: unable to authenticate` | Wrong login, password or key, or the account does not allow that method. |
| `accepted SSH but not the SFTP subsystem` | The account has a shell but SFTP is disabled for it. Ask the operator to enable it. |
| `conn_id "x" is a postgres connection; a file node reads and writes through an sftp connection` | The node names a connection of another type. |
| `a path may not contain a '..' segment` | Use a path inside the base directory, or an absolute path. |
| `the path is outside the connection's base directory` | The node's absolute path is outside the base directory. Use a relative path, or an absolute one inside it. |
| `ssh: no common algorithm for key exchange` or `for host key` | The server supports only algorithms considered insecure (SHA-1 key exchange, DSA host keys). The server's operator needs to enable a modern one; every OpenSSH release of the last decade has them. |
| `i/o timeout` during a transfer | Nothing moved for two minutes; the server stopped responding. |
| `the file is larger than the download limit` | The file exceeds `max_download_bytes`. Raise the limit on the connection if the file is expected to be that large. |
| `the configured host key is a known_hosts line marked @revoked` | The pasted line revokes the key rather than trusting it. Use the server's current key. |
| `enter the password again` when saving a connection | The host, port or type changed. Enter the password (and extra settings) again. |
| `the connection's extra settings are not a JSON object` | The extra settings do not parse. The message does not quote them, because they hold the private key. |
| `no data directory can hold a downloaded file` | None of the data directories is writable on this machine. Make one writable, or set `BROKOLI_DATA_DIRS`. |
| `fetch sftp://x/inbound/rates.csv: ... file does not exist` | The file is not there yet, or the path is wrong. Relative paths start in the base directory. |

## Not supported yet

- Picking up files by pattern (`inbound/*.csv`), and moving or deleting
  a file after it has been read.
- FTPS (FTP over TLS), a different protocol from SFTP.
- SSH agent and certificate authentication.
- A delivery receipt beyond the run log, such as the server's checksum
  of the delivered file.
- S3 as a file location. The seam that SFTP goes through is where it
  would plug in.
