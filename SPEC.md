# Carbon task system specification (v2)

This is the active Carbon contract. It defines the canonical identity, Home storage boundary,
standalone-project default, optional shared clusters, MCP v2, and custom catalog images. The
transport may report `apiVersion: "v1"`; that is the wire version and does not change Carbon's
stable v2 feature contract.

## 1. Canonical identity

| Surface | Canonical value |
| --- | --- |
| Product | Carbon |
| CLI and binary | `carbon` |
| MCP configuration key | `carbon` |
| Private data directory | `.carbon/` |
| Environment prefix | `CARBON_` |
| HTTP actor header | `X-Carbon-Actor` |
| Desktop URI | `carbon://` |
| Stable MCP compatibility layer | `v2` |

Carbon identifiers are case-sensitive where their protocol or filesystem requires it. Display
names are not identifiers: clients retain stable IDs returned by Carbon rather than deriving
paths from names.

## 2. Goals and boundaries

Carbon provides durable local task coordination for a human and coding agents. It preserves task
history, dependencies, provenance, sessions, and safe cross-process writes without placing its
private data inside a source repository.

Carbon is not an authenticated multi-user network service. HTTP is loopback-only and MCP stdio
inherits the trust of its parent process. Remote use is through an operator-controlled loopback
tunnel, never a public listener.

## 3. Carbon Home

A Carbon **Home** is an existing directory selected explicitly by a human, desktop app, or CLI.
Carbon owns only its `.carbon/` child:

```text
<home>/.carbon/
  home.json
  write.lock
  projects/<project-id>/.carbon/       # isolated standalone-project task store
  clusters/<cluster-id>/.carbon/       # optional shared task-pool store
  catalog-presentation.json            # built-in/emoji token document
  catalog-assets/
    index.json
    blobs/<sha256>.png
  backups/
```

All managed paths resolve beneath the canonical Home and must be trusted directories or regular
files. A malformed component, symlink, reparse point, or escaped path fails closed. Carbon does
not accept a client-supplied asset filename or data-root path.

`home.json` contains the Home ID, schema version, standalone projects, and optional clusters. A
project records a stable `project_id`, display metadata, and a source path/fingerprint. The source
path is metadata, not a Carbon storage root.

## 4. Standalone projects and optional clusters

A project registered directly in a Home gets its own store at:

```text
<home>/.carbon/projects/<project-id>/.carbon/
```

This is the normal Carbon workflow. A `home + project` connection can read and write only that
project store. Its configuration carries the stable `project_id`, preventing accidental unscoped
task creation.

A **cluster** is an optional shared-pool extension. Its members share one store at:

```text
<home>/.carbon/clusters/<cluster-id>/.carbon/
```

`task.project_id` preserves ownership within a shared pool. Cluster-wide work has no project owner
only where the operation permits it. A cluster connection may narrow to a member project, but a
standalone project cannot be selected through a cluster; the two storage modes never merge.

## 5. Scope resolution

Every request and MCP connection resolves exactly one scope before opening a task store.

| Scope | Required selectors | Permitted use |
| --- | --- | --- |
| Home-only | `home` | Catalog metadata, presentation assets, backup settings, identity. |
| Standalone project | `home`, `project` | The selected isolated project store. |
| Cluster | `home`, `cluster` | The selected optional shared pool. |
| Cluster project | `home`, `cluster`, `project` | The selected member subset of that shared pool. |

Selectors resolve to stable IDs before use. A Home-only operation never opens a task store.
Incompatible selector combinations and ambiguous project references fail instead of silently
choosing a store. A project-bound read defaults to that project; `include_cluster: true` is an
explicit same-cluster read expansion and never broadens a write.

## 6. CLI and process contract

```text
carbon home init [--home PATH]
carbon home doctor [--home PATH] [--repair]
carbon home import --home PATH --legacy-cluster PATH [--plan FILE | --apply --expected-digest SHA256]
carbon snapshot create|verify|upload --home PATH ...
carbon serve --actor ACTOR --home PATH [--project PROJECT | --cluster CLUSTER [--project PROJECT]] --compat-layer v2
carbon web --home PATH [--project PROJECT | --cluster CLUSTER [--project PROJECT]] --compat-layer v2
```

`carbon web` accepts only `127.0.0.1` or `localhost` and prints `CARBON_WEB_URL=<url>` to stdout.
`carbon serve` runs MCP over stdio. An actor is fixed for the connection lifetime, not supplied per
tool write. `CARBON_SHELL` selects the check shell, `CARBON_PARENT_WATCH` is the desktop-sidecar
lifecycle signal, and `X-Carbon-Actor` may assert an HTTP writer identity.

## 7. Stable MCP v2

MCP v2 is Carbon's stable feature and storage contract. Every session starts with `identity`,
which returns its actor, client, resolved scope, `apiVersion`, requested layer, stable layer, and
capabilities. `apiVersion: "v1"` is the independent HTTP/MCP transport version.

All tool inputs are snake_case. A connection cannot broaden its scope through a tool argument.
The stable v2 tool groups are:

| Group | Representative tools |
| --- | --- |
| Identity and catalog | `identity`, `list_clusters`, `resolve_cluster`, `list_projects`, `resolve_project`, `create_cluster`, `create_project` |
| Task work | `list`, `get`, `create`, `update`, `transition`, `run_checks`, `attest`, `note` |
| Sessions | `begin`, `heartbeat`, `finish`, `cancel`, `get_session`, `list_sessions` |
| Leases | `lease_claim`, `lease_status`, `lease_renew`, `lease_release`, `lease_reassign`, `lease_approve` |
| Planning/recovery | `search`, blockers, evidence, trash, bulk, saved-view, and template tools |
| Worker audit | Work Log and worker/project metrics tools |

Catalog creation is explicit: a caller supplies `allow_create: true` and a non-empty `reason`; a
project also supplies a local `source_path`. A failed lookup never grants creation permission.
Standalone creation has no cluster prerequisite; a cluster selector opts into the shared-pool mode.

## 8. Tasks, sessions, and concurrency

Task IDs are engine-assigned. A v2 task creation supplies `type` and `importance`; priority is a
separate ordering signal. Dependencies already exist before use, and project ownership is
`project_id`. Moving ownership is explicit and scope-checked.

The normal agent lifecycle is `identity → begin → heartbeat* → finish | cancel`. `finish`
requests review; it does not close a task. `begin` requires an equal `expected_actor` and an
idempotency key. Version-protected writes use a raw version or quoted ETag in `expected_version`;
stale writes reject rather than overwrite newer state.

Leases create durable ownership coordination. `lease_claim` requires a reason and current version.
A conflict produces a pending request, not a silent replacement. Batch writes provide an expected
version for every selected task.

## 9. Catalog presentation and custom images

Built-in/emoji presentation tokens remain in `.carbon/catalog-presentation.json`; the client maps
them to shipped glyphs. Custom images are separate optional overrides, so token readers remain
backward-compatible.

```text
GET    /api/home/presentation/{kind}/{id}/asset
PUT    /api/home/presentation/{kind}/{id}/asset
DELETE /api/home/presentation/{kind}/{id}/asset
```

These Home-only routes accept `kind` exactly `project` or `cluster` and a stable target ID. `PUT`
takes one raw body, never JSON/multipart, a filename, path, URL, or data URI. Its declared and
decoded type must agree and be `image/png`, `image/jpeg`, or `image/webp`. SVG, GIF, malformed
bytes, and decompression-bomb inputs are rejected.

| Limit | Value |
| --- | --- |
| Submitted body | 1 MiB |
| Normalized PNG | 1 MiB |
| Width or height | 4096 pixels |
| Total decoded pixels | 1,048,576 |

Carbon decodes accepted pixels and re-encodes them as PNG, removing source filename, original MIME
metadata, and EXIF content. It stores a SHA-256-addressed blob beneath
`.carbon/catalog-assets/blobs`, with a separate atomic index for project/cluster metadata. Writes
hold the Home lock, enforce containment, and collect only known, unreferenced PNG blobs.

Successful `PUT` returns `204 No Content` with an ETag. `DELETE` is idempotent, returns `204`, and
restores token/default rendering. A successful `GET` returns normalized `image/png`, a strong ETag,
`X-Content-Type-Options: nosniff`, and `Cache-Control: private, max-age=0, must-revalidate`; it
honors `If-None-Match`. No custom image returns `404` with `Cache-Control: no-store`.

## 10. HTTP, events, and security

The local HTTP API uses JSON except for documented raw/binary endpoints. `/api/version`,
`/api/status`, `/api/identity`, and `/healthz?format=json` expose the resolved contract. Streamable
HTTP MCP lives at `/mcp` and requires an explicit actor plus a valid Carbon scope. SSE emits only
events for the resolved scope. Mutating browser requests carrying `Origin` must be same-origin
except for the narrow loopback development allowance.

Every managed Home, task-store, metadata, and asset write is containment-checked, lock-protected,
and atomic. The local API is intentionally unauthenticated but loopback-only. Detailed route and
event semantics are in `docs/reference/http-api.md` and `docs/reference/events.md`.

## 11. Legacy migration reader

The only historical input Carbon reads is an explicit migration source: a `.cairn/` task store and
its `.cairn-cluster.json` registry. `carbon home import` first emits a reviewable plan and binds
apply to its review digest. It reads that source without changing it, stages Carbon output privately,
and publishes copies only after validation. It never makes the historical source an active Carbon
workspace. See `docs/migration/0.4.md` and `docs/migration/canonical-name-audit.md`.
