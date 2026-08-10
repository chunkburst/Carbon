---
title: Carbon architecture
---

# Carbon architecture

Carbon centers all private coordination data in one user-chosen Home. A source repository is a
registered source surface, never a private Carbon data root. The design makes a standalone project
the natural starting point and uses clusters only when shared task-pool behavior is intentional.

## Home layout

```text
<home>/.carbon/
  home.json
  write.lock
  projects/
    project_site/.carbon/          # standalone site task store
    project_api/.carbon/           # standalone API task store
  clusters/
    cluster_product/.carbon/       # optional shared task pool
  catalog-presentation.json        # built-in/emoji tokens
  catalog-assets/                  # custom target-bound image index and blobs
  backups/
```

`home.json` has manifest schema v2. It contains stable Home, standalone-project, and optional
cluster IDs; project display metadata; and observed canonical source paths/fingerprints. Carbon
uses IDs for durable references, not source paths or display names.

## Project scope is the default

A standalone project has one isolated store under `.carbon/projects/<project-id>/.carbon/`. Its
store configuration is bound to its stable `project_id`, so no task becomes accidentally unowned.
A `home + project` MCP or HTTP scope can access only that store.

This default avoids hidden coupling between unrelated projects. It also lets a user add a new
project without creating a container first.

## Clusters are explicit shared-pool extensions

A cluster owns one store below `.carbon/clusters/<cluster-id>/.carbon/`. Projects nested in that
cluster share the store; `task.project_id` remains the durable owner for project-specific tasks.
The cluster may contain intentionally shared tasks when the supported operation allows no project
owner.

Carbon distinguishes four resolved scopes:

| Scope | Selectors | Store access |
| --- | --- | --- |
| Home-only | `home` | Catalog, metadata, backups, custom images; no task store. |
| Standalone project | `home + project` | One isolated project store. |
| Cluster | `home + cluster` | One optional shared pool. |
| Cluster project | `home + cluster + project` | A member-project subset of that shared pool. |

Scope resolution fails on ambiguous project references, missing membership, or attempts to select a
standalone project through a cluster. A project-bound read can deliberately expand to its selected
cluster where documented; writes do not expand.

## Write safety

Carbon resolves every managed path inside the canonical Home and rejects reparse points, symlinks,
malformed data paths, and containment escapes. Home metadata is protected by `.carbon/write.lock`
and published atomically. Task stores use the same conservative locking and atomic-write approach.

Source paths are observed for canonicalization and fingerprinting. A missing source can be marked
offline; an ambiguous or unsafe source is never guessed. Carbon does not place task data into the
source repository while resolving a Home project.

## MCP v2 and local HTTP

MCP v2 is the stable Carbon capability contract. An agent invokes `identity` first and receives its
fixed actor, client, scope, capabilities, and versions. The HTTP/MCP transport continues to expose
`apiVersion: "v1"` independently from stable layer `v2`.

`carbon serve` runs MCP over stdio. `carbon web` exposes the local HTTP API, SSE, and Streamable
HTTP MCP on loopback only. It prints `CARBON_WEB_URL=<url>` and does not bind a remote listener.
Actor assertions use `X-Carbon-Actor` on HTTP requests and are sanitized before durable writes.

## Presentation tokens and image assets

The existing token document remains deliberately small and backwards-compatible:

```text
<home>/.carbon/catalog-presentation.json
```

Custom project/cluster images are stored separately:

```text
<home>/.carbon/catalog-assets/
  index.json
  blobs/<sha256>.png
```

The Home-only API is target-bound:

```text
GET    /api/home/presentation/{kind}/{id}/asset
PUT    /api/home/presentation/{kind}/{id}/asset
DELETE /api/home/presentation/{kind}/{id}/asset
```

`kind` is `project` or `cluster`. `PUT` accepts only one raw, matching-MIME PNG/JPEG/WebP body,
limited to 1 MiB, 4096 pixels per side, and 1,048,576 decoded pixels. Carbon strips metadata by
re-encoding a verified image to PNG, content-addresses it with SHA-256, and preserves the old token
file unchanged. `GET` serves only normalized `image/png` with ETag, `nosniff`, and mandatory
revalidation. `DELETE` clears the override and is idempotent.

## Backup boundary

Local snapshots address the Carbon Home tree, not arbitrary caller-selected folders. Remote
publication is disabled until an encrypted profile and explicit authorization are present. Secrets
are resolved at operation time and are not persisted in Home files, snapshot manifests, or logs.

## Related documents

- [CLI reference](/reference/cli)
- [MCP v2 reference](/reference/mcp-tools)
- [HTTP API](/reference/http-api)
- [Legacy migration reader](/migration/0.4)
