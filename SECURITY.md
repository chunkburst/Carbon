# Security Policy

**Report vulnerabilities privately through GitHub Security Advisories.** Open this repository's
*Security* tab, then choose *Report a vulnerability*. Do not open a public issue for a security
report.

- Supported versions: Carbon is pre-1.0; only the latest release is supported.
- Public disclosure window: 90 days from acknowledgement.

## Product identity

The supported product identity is **Carbon**: the CLI and binary are `carbon`, the MCP
configuration key is `carbon`, private state is `.carbon/`, deep links are `carbon://`, and
environment variables use the `CARBON_*` prefix. These names identify local product surfaces;
they do not create authentication or a network trust boundary.

## Trust model

Carbon is a local, single-user tool. It is not an authenticated multi-user service.

- **No authentication; hard loopback boundary.** `carbon web` listens only on `127.0.0.1` or
  `localhost`; `carbon serve` uses stdio. `--allow-remote` fails before binding. Do not expose
  Carbon directly on a network. For another machine, keep Carbon on loopback and use SSH local
  forwarding or a VPN tunnel that terminates on loopback.
- **Explicit Home scope.** A request selects a Carbon Home and, for task operations, either a
  standalone project or an optional cluster scope. Mixing selectors is rejected instead of
  silently choosing storage.
- **Standalone isolation by default.** A standalone project owns a private task store under its
  Home. A cluster is an explicit shared-pool boundary: its member projects share one task store,
  but another cluster cannot read or write that pool through normal scope resolution.
- **Source folders are metadata.** Carbon records canonical source paths and stable IDs. A moved
  or ambiguous source is relinked explicitly rather than guessed, and source repositories are
  never copied into a Home during ordinary scope selection.

> [!WARNING]
> **Checks run arbitrary shell commands. Trust your repositories.** A task check runs through the
> configured shell, and closing a task can run checks again. Treat task files with the same care
> as a repository `Makefile` or test suite.

> [!WARNING]
> **Administrator autostart elevates the application.** On Windows this includes the WebView,
> local server, and every project check. It is disabled by default, requires UAC, and must not
> point at a directory writable by ordinary users.

## Catalog image assets

Custom project and cluster images are Home-only, target-bound resources. The API accepts only a
raw PNG, JPEG, or WebP body with a matching `Content-Type`; filenames, paths, URLs, SVG, GIF, and
multipart wrappers are not accepted. Carbon limits inputs and normalized output to 1 MiB, limits
dimensions to 4096 and pixels to 1,048,576, decodes the image, and stores a metadata-stripped,
content-addressed PNG beneath `.carbon/catalog-assets`.

Asset metadata and blobs are separately atomic, lock-protected, and constrained to trusted regular
files below the selected Home. Reparse points and paths escaping that root fail closed. Asset GET
responses are `image/png` with `X-Content-Type-Options: nosniff`, a strong ETag, and mandatory
revalidation; an absent custom image returns 404 rather than a fallback file.

## Snapshots, remote storage, and keys

Carbon snapshots are immutable and content-addressed. Restore verifies the complete snapshot and
writes to a newly created staging directory; it does not replace a live Home automatically.

- **Remote scheduling requires durable consent.** The configuration defaults to disabled remote
  storage. Saving configuration, reading status, retention, and manual local runs do not resolve
  credentials or open a network connection. An upload first verifies a local snapshot and requires
  an enabled, configured, encrypted profile plus explicit authorization.
- **Secrets do not persist in Carbon data.** Carbon resolves opaque credential/key references at
  upload time and never stores plaintext credentials or encryption material in `.carbon/`, logs,
  snapshots, manifests, object headers, or command-line arguments.
- **Remote encryption is mandatory.** Every S3/COS object uses an authenticated AES-256 envelope
  with a fresh per-object data key.
- **Endpoint restrictions apply.** Use HTTPS official provider endpoints or approved private
  endpoints. Use credential references such as `aws-default://`, `aws-profile://NAME`,
  `env://PREFIX`, or `cos-env://PREFIX`, and use `env://VAR` for encryption keys.

## Guarantees

- **Identity integrity.** Every write is stamped with a sanitized connection actor. HTTP clients
  may assert one with `X-Carbon-Actor` or `?actor=`; MCP-over-HTTP requires an explicit actor.
  `begin` rejects a mismatched `expected_actor` before writing.
- **Browser write isolation.** Mutating browser requests carrying `Origin` must be same-origin,
  with a narrow loopback Vite development exception.
- **Safe configuration writes.** Carbon modifies only its `carbon` MCP entry. Writes are atomic,
  preserve other server entries, make a `<file>.bak` before replacing an existing file, and
  re-read the result for verification. Disconnect removes only the `carbon` entry.
- **Cross-process safety.** `.carbon/write.lock` is an advisory lock for Carbon metadata and task
  writes; lock-file existence is never treated as ownership.
- **Visible ownership conflicts.** A lease collision creates a pending claim rather than silently
  replacing the holder.

## Legacy migration reader

The explicit import reader may read a historical `.cairn/` task source and a historical
`.cairn-cluster.json` registry. It is not a general operating mode: the source is opened
read-only, a review digest is required before apply, copies are staged privately, and the original
source is never renamed, moved, deleted, or rewritten. See
[the migration guide](docs/migration/0.4.md).

## What to report

- Path traversal or read/write access outside the intended Home, project, cluster, asset, or
  configuration path.
- Cross-project or cross-cluster task, session, trash, snapshot, backup, or catalog-asset access.
- Identity bypass, actor spoofing, or a conflict that silently overwrites work.
- An image upload that accepts an unsafe type, escapes Home containment, or exposes stale content
  after replacement.
- A remote backup that uploads while disabled, persists plaintext credentials/key material, or
  decrypts without the configured external key provider.
- Command execution beyond the documented task-check mechanism.
- HTTP or MCP reachability beyond localhost.

## Out of scope

- The deliberate unauthenticated localhost model.
- Arbitrary shell commands from checks you authored in a repository you control.
- Direct remote binding; use an SSH local port forward or VPN tunnel terminating on loopback.
- Retention, access control, or deletion guarantees made by a third-party storage account after
  an explicit upload.
