---
title: HTTP API
---

# Carbon HTTP API

`carbon web` exposes Carbon's local HTTP API, SSE stream, and Streamable HTTP MCP endpoint. It is
a loopback-only, single-user service with no authentication. Keep it on `127.0.0.1` or `localhost`;
for another machine, use an operator-controlled SSH local forward or VPN tunnel that terminates on
loopback.

## Versions

`GET /api/version`, `GET /api/status`, `GET /api/identity`, and `/healthz?format=json` return the
current product/build and capability contract.

| Field | Meaning |
| --- | --- |
| `productVersion` | Descriptive Carbon build version. |
| `apiVersion` | HTTP/MCP transport version; currently `v1`. |
| `requestedCompatLayer` | Layer selected for this connection. |
| `supportedCompatLayers` | Layers understood by this build. |
| `stableCompatLayer` | Approved stable Carbon layer: `v2`. |
| `capabilities` | Features exposed by the resolved layer and scope. |

The transport field and stable feature layer are intentionally independent. New clients negotiate
from the compatibility fields, not from a product display version.

## Scope and actor selectors

Every route resolves a Home first. Home-only routes must not carry a project or cluster selector.
Task routes resolve one of the following scopes:

| Scope | Query selectors | Equivalent headers |
| --- | --- | --- |
| Home-only | `home` | `X-Carbon-Home` |
| Standalone project | `home`, `project` | `X-Carbon-Home`, `X-Carbon-Project` |
| Cluster | `home`, `cluster` | `X-Carbon-Home`, `X-Carbon-Cluster` |
| Cluster project | `home`, `cluster`, `project` | `X-Carbon-Home`, `X-Carbon-Cluster`, `X-Carbon-Project` |

HTTP writes use the configured server actor unless a permitted request asserts `X-Carbon-Actor` or
`?actor=`. Carbon sanitizes and records the resulting actor. Mutating browser requests with an
`Origin` header must be same-origin except for the narrow loopback development allowance.

## Home catalog

All routes in this table are Home-only. JSON requests use `Content-Type: application/json`.

| Method and path | Purpose |
| --- | --- |
| `GET /api/home` | Read Home initialization state and manifest. |
| `POST /api/home` | Initialize the selected Home. |
| `GET /api/home/projects` | List top-level standalone projects. |
| `POST /api/home/projects` | Create a standalone project from display metadata and `sourcePath`. |
| `GET/PATCH /api/home/projects/{project}` | Read/update one standalone project. |
| `POST /api/home/projects/{project}/relink` | Explicitly relink a standalone source path. |
| `POST /api/home/projects/{project}/clear-task-data` | Permanently clear one project's task data after exact-name confirmation. |
| `POST /api/home/projects/{project}/delete` | Remove a project from the catalog, optionally clearing its task data first. |
| `GET /api/home/clusters` | List optional shared clusters. |
| `POST /api/home/clusters` | Create a shared cluster. |
| `GET/PATCH /api/home/clusters/{cluster}` | Read/update a cluster. |
| `POST /api/home/clusters/{cluster}/projects` | Add a project to that shared pool. |
| `PATCH /api/home/clusters/{cluster}/projects/{project}` | Update a cluster member's metadata. |
| `POST /api/home/clusters/{cluster}/projects/{project}/relink` | Explicitly relink a member source path. |
| `POST /api/home/clusters/{cluster}/projects/{project}/detach` | Deliberately detach a member into a standalone project. |
| `GET /api/home/doctor` | Report managed-path and manifest health. |

Project creation is standalone unless the cluster route is deliberately selected. A Home route
rejects inherited/default project or cluster scope rather than silently applying it.

### Clear project task data

`POST /api/home/projects/{project}/clear-task-data` is a Home-only, local-human administration
operation. `{project}` must be the stable project ID. The strict JSON body contains only the exact
current display name, with no trimming, case folding, or Unicode normalization:

```json
{ "name": "Carbon" }
```

Carbon clears the selected project's active tasks, task Trash entries, associated sessions/live
state, and check-run logs. It preserves the project/catalog entry, source link, configuration,
icon, Worker records and aliases, Work Logs, views, templates, and the task-ID counter; later task
IDs continue increasing and are never reset or reused. In a shared cluster store, tasks belonging
to other projects and cluster-wide tasks are preserved.

The server re-reads the current Home manifest while holding the Home lock, verifies the stable ID
and exact name, then performs the filesystem change beneath the Store write lock with a durable
receipt and crash-recovery journal. A non-`human:*` actor receives `403`; malformed/extra JSON is
`400`; a name mismatch is `422`; unsafe paths, surviving cross-project references, lock conflicts,
or an incomplete rollback fail closed with `409`.

### Delete a project

`POST /api/home/projects/{project}/delete` is also Home-only and local-human-only. `{project}` must
be the stable project ID. Its strict JSON body requires the exact current display name and an
explicit data choice:

```json
{ "name": "Carbon", "deleteData": false }
```

With `deleteData: false`, Carbon removes only the catalog entry. With `deleteData: true`, it first
clears that project's active tasks, Trash entries, associated sessions/live state, and check-run
logs, using the same project isolation and reference checks as the clear operation, and then
removes the catalog entry. In both modes Carbon deliberately preserves the linked source folder,
the managed project/cluster data root, project configuration, templates, views, Work Logs, Worker
metadata, and presentation assets. A shared cluster root, sibling-project data, and cluster-wide
tasks are never removed.

The browser presents a two-stage confirmation, an optional “clear Carbon task data” checkbox, and
requires the project name to be typed exactly before enabling the destructive action. The server
enforces the same confirmation independently; malformed JSON is `400`, non-human actors receive
`403`, an exact-name mismatch is `422`, and unsafe paths, surviving references, or transactional
conflicts fail closed with `409`.

## Catalog presentation tokens

`GET /api/home/presentation` reads catalog presentation metadata. `PUT
/api/home/presentation/{kind}/{id}/icon` updates the safe built-in/emoji token for one project or
cluster. The token document remains separate from custom image assets.

## Catalog presentation assets

Custom image routes are Home-only and target-bound. `kind` is exactly `project` or `cluster`; `id`
is the target's stable ID.

| Method | Path | Request | Success | Failure behavior |
| --- | --- | --- | --- | --- |
| `PUT` | `/api/home/presentation/{kind}/{id}/asset` | One raw PNG, JPEG, or WebP body; matching `Content-Type` | `204 No Content`, `ETag` | `415` invalid declared type, `413` body over 1 MiB, `422` invalid image/target, `404` missing target |
| `GET` | `/api/home/presentation/{kind}/{id}/asset` | Optional `If-None-Match` | `200 image/png` or `304` | `404` when no custom asset or target is absent |
| `DELETE` | `/api/home/presentation/{kind}/{id}/asset` | No body | `204 No Content` | Target validation errors as above; clearing an absent override is idempotent |

`PUT` is deliberately a raw-body contract: it rejects multipart forms, JSON wrappers, client
filenames, filesystem paths, URLs, data URIs, SVG, GIF, corrupt bytes, and declared/decoded MIME
mismatches. Carbon accepts at most 1 MiB, each dimension at most 4096 pixels, and at most
1,048,576 decoded pixels. It decodes a valid input, re-encodes it as PNG to strip source metadata,
hashes the normalized bytes with SHA-256, and stores an atomic content-addressed blob beneath
`.carbon/catalog-assets`.

Successful `GET` always serves the normalized representation with:

```text
Content-Type: image/png
X-Content-Type-Options: nosniff
Cache-Control: private, max-age=0, must-revalidate
ETag: "<sha256>"
```

An absent custom image returns `404` with `Cache-Control: no-store`, so a browser can retry after a
later upload. A client renders a configured token/default after a 404; it should never substitute a
local file at this endpoint.

## Task, session, and planning routes

These routes require a resolved standalone-project, cluster, or cluster-project task scope:

| Area | Routes |
| --- | --- |
| Tasks | `GET/POST /api/tasks`, `GET/DELETE /api/tasks/{id}`, transition/update/reorder/check/note subroutes |
| Search/types | `GET /api/search`, `GET/POST /api/types` |
| Sessions | Task session-begin/list routes and `/api/sessions` read/heartbeat/finish/cancel routes |
| Leases | `/api/tasks/{id}/lease/claim`, `renew`, `release`, `reassign`, and approval routes |
| Recovery/planning | Trash, bulk update/move, views, templates, Evidence/blocker, and statistics routes |
| Work Logs | `GET/POST /api/worklogs`, `GET/PUT/DELETE /api/worklogs/{id}` in an explicit cluster scope |
| Backups | `/api/backup/*` Home-only backup configuration, snapshot, verification, upload, and restore-plan routes |

Request/response documents use JSON and stable IDs. Operations marked version-protected require the
current raw version or quoted ETag in `expectedVersion`/`expected_version` according to their route
or MCP adapter; a stale writer receives a conflict instead of overwriting later data.

## Events and Streamable HTTP MCP

`GET /api/events` opens the scoped SSE stream. See [Events](/reference/events). Streamable HTTP
MCP is available at `/mcp`; existing Home/project and optional-cluster URLs remain fixed to their
initial identity and scope. An explicit Home-only `routing=session` connection enables the stable
v2 active-project workflow: `create_project` activates the new project and `select_project`
switches within that Home. Omitting `routing=session` preserves the previous Home-only catalog
contract. See [MCP tools](/reference/mcp-tools).

## Response conventions

JSON errors carry a human-readable error field and an appropriate status. Common results include
`400` malformed/mixed scope, `404` absent scoped resource, `409` stale ownership/version conflict,
`413` over-limit body, `415` unsupported media type, and `422` syntactically valid but rejected
domain data. The server never treats a request-body path as authority to escape the selected Home.

## Historical migration endpoints

Home-only migration preflight, preview, apply, and receipt routes exist solely for the explicit
read-only migration reader. Use the [migration guide](/migration/0.4) rather than treating those
routes as a normal project setup path.
