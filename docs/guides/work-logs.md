---
title: Work Logs and Worker audit
---

# Work Logs and Worker audit

Work Logs are durable worker-authored audit records. They are distinct from task provenance notes:
notes explain a task, while a Work Log records a worker's scoped operational update.

## Scope and visibility

Work Logs belong to an explicitly selected shared cluster. A project may be attached where the
visibility requires it, but the log's cluster, worker, ID, creation audit fields, and version are
server-owned. A standalone project does not inherit sibling Work Log access.

| Visibility | Audience |
| --- | --- |
| `worker_private` | The owning Worker and local human audit reader. |
| `project_public` | Connections for the same project in the selected cluster. |
| `global_public` | Connections in the same Carbon Home, never Internet-visible or cross-Home. |

Filters are conjunctive; they cannot expand visibility. A task attachment must resolve in the
bound cluster and agree with the log's project scope.

## Write rules

`worklog_create` accepts the content and allowed visibility fields only. Carbon supplies the
opaque ID, worker, cluster ID, timestamps, provenance, and version. Updating or deleting a log
requires the current `expected_version`; stale writes fail rather than overwrite newer content.

An ordinary worker changes only its own log. A local `human:*` actor may audit visible fields but
does not gain an implicit right to modify another worker's record.

## HTTP actor provenance

For browser/API writes, Carbon records the sanitized connection actor. A client may assert one with
`X-Carbon-Actor` or `?actor=` when the endpoint permits it. Streamable HTTP MCP requires an
explicit actor at connection setup. Never use a display name as a substitute for an actor ID.

## Good log entries

Keep the title and body factual: what changed, which scope it applies to, evidence, and the next
action. Do not put secrets, large command output, untrusted HTML, or source archives in a Work
Log. Link an artifact or use task Evidence when detailed proof is needed.
