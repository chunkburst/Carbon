---
title: Canonical-name audit
---

# Carbon canonical-name investigation and update report

**Status:** updated for the active Carbon documentation and VitePress site.

## Scope investigated

This audit covered active documentation entry points, guide pages, agent snippets, CLI/MCP/HTTP
references, VitePress base path and repository links, root operating instructions, security policy,
specification, and the Unreleased changelog entry.

## Canonical Carbon surfaces

| Surface | Canonical form | Documentation status |
| --- | --- | --- |
| Product | Carbon | Current prose and headings use Carbon. |
| CLI/binary | `carbon` | Current commands and agent snippets use `carbon`. |
| MCP key | `carbon` | Connection examples and configuration guidance use `carbon`. |
| Home storage | `.carbon/` | Current layouts and security guidance use `.carbon/`. |
| Actor header | `X-Carbon-Actor` | HTTP/security references use the canonical header. |
| Environment | `CARBON_*` | Current process variables use the canonical prefix. |
| URI | `carbon://` | Current deep-link guidance uses the canonical URI. |
| Documentation content | Relative VitePress links | Current pages use Carbon terminology without depending on a future hosting slug. |
| Scope model | standalone project first | Current quickstarts do not require a cluster. |
| MCP contract | stable v2 | Current guides distinguish stable v2 from transport `apiVersion: "v1"`. |
| Catalog images | Home-only target-bound asset routes | HTTP/architecture/spec describe raw `PUT`, `GET`, and `DELETE`. |

## External hosting

The canonical public repository is `chunkburst/Carbon`. Release, security, specification,
changelog, edit, and social links all target that repository. GitHub Pages is built with the
case-sensitive `/Carbon/` base path.

## Deliberate legacy-reader exceptions

The following old literals remain only where preservation is necessary and clearly labelled:

| Location | Literal class | Why it remains |
| --- | --- | --- |
| [Legacy migration reader](/migration/0.4) | Historical `.cairn/` source | It identifies the exact read-only task-store source accepted by the importer. |
| [Legacy migration reader](/migration/0.4) | Historical `.cairn-cluster.json` registry | It identifies the exact read-only registry source accepted by the importer. |
| [Specification](https://github.com/chunkburst/Carbon/blob/main/SPEC.md) | Same historical import paths | The active specification constrains the reader to those sources. |
| [Security policy](https://github.com/chunkburst/Carbon/blob/main/SECURITY.md) | Same historical import paths | The policy documents the reader's read-only safety boundary. |
| [Changelog](https://github.com/chunkburst/Carbon/blob/main/CHANGELOG.md) historical record | Historical names/layouts | Release facts are preserved and labelled as non-current migration history. |

No current instruction tells a user to invoke an old binary, configure an old MCP key, create a
cluster before a project, or use a historical directory as active storage.

## Feature-contract verification recorded

The current docs state that custom catalog assets are:

- Home-only and bound to `/api/home/presentation/{kind}/{id}/asset`.
- Read with `GET`, written with raw-body `PUT`, and cleared by idempotent `DELETE`.
- Limited to actual PNG, JPEG, or WebP input with matching `Content-Type`, 1 MiB body/output,
  4096 maximum dimension, and 1,048,576 decoded pixels.
- Re-encoded as metadata-stripped PNG and stored content-addressed below
  `.carbon/catalog-assets`.
- Served only as `image/png` with ETag, `X-Content-Type-Options: nosniff`, and revalidation cache
  controls; a missing override returns 404.

## Follow-up rule

When adding documentation, use only the canonical values in the first table. If a historical input
must be mentioned for import support, place it in a labelled legacy-migration section and describe
it as a read-only source, never as a runnable setup command.
