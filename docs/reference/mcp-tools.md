---
title: MCP tools
---

# Carbon MCP tools (stable v2)

Carbon's stable MCP capability contract is **v2**. Start every connection with `identity`; it
returns the fixed actor and client, current scope, binding mode, capabilities, and version
contract. `apiVersion: "v1"` remains the independent transport version.

## Connection shape

```json
{
  "mcpServers": {
    "carbon": {
      "command": "carbon",
      "args": [
        "serve",
        "--actor", "agent:codex",
        "--client", "codex",
        "--home", "/work/carbon-home",
        "--project-session",
        "--compat-layer", "v2"
      ]
    }
  }
}
```

This is the recommended project-session connection. It binds authority to one Carbon Home while
keeping an explicit active project inside that MCP connection. Creating a project activates it;
`select_project` switches deliberately later. The selected project stays active until another
selection or disconnect.

Existing `--home ... --project <id>` configurations remain strict pinned connections: they do not
register `select_project`, never auto-switch, and still require reconnecting to use another
project. Use `--cluster <id>` only for an intentional shared task pool.

## Common rules

- Tool input keys are snake_case.
- Actor, client, Home authority, and compatibility layer are fixed. In project-session mode only,
  `select_project` may replace the active project within that Home; it never changes actor or Home.
- Before a project-session has an active project, task and Work Log tools fail closed instead of
  writing to the Home root.
- Project-bound reads default to that project. `include_cluster: true` is an explicit same-cluster
  read expansion only and never broadens a write.
- Version-protected calls accept the current raw version or quoted ETag in `expected_version` and
  reject stale writes.
- Task creation supplies explicit `type` and `importance`; `project_id` expresses ownership.
- Worker Identity Mode is disabled by default. When a project store enables it, typed task claims,
  sessions, reassignment, and approval are checked against the Worker's claimed task types.

## Catalog tools

Catalog tools operate in a Carbon Home. Persist canonical IDs returned by a resolver instead of
reusing an ambiguous display name.

| Tool | Purpose |
| --- | --- |
| `list_clusters`, `get_cluster`, `resolve_cluster`, `describe_cluster` | List, resolve, or describe optional shared pools. |
| `create_cluster` | Explicitly create a shared pool with `allow_create: true` and a reason. |
| `list_projects`, `get_project`, `resolve_project`, `describe_project` | List, resolve, or describe standalone and cluster projects. |
| `create_project` | Create a source-bound project; no cluster is needed for a standalone project. |
| `select_project` | In project-session mode, select an existing Home project as the sticky active project. |

Creation is never inferred from a failed lookup. `create_project` requires a local `source_path`,
`allow_create: true`, and a non-empty reason. Passing a cluster selector deliberately creates a
shared-pool member; omitting it creates the default standalone project.

In project-session mode, a successful `create_project` also activates the newly created project.
Its normal wire result remains the project description; call `identity` to confirm the new scope
and monotonic `selectionVersion`. `select_project` accepts `{project, cluster?}` and returns
`{bindingMode, selectionVersion, scope}`. It resolves a stable ID or an unambiguous project
reference and leaves the previous selection unchanged on failure.

## Core task tools

| Tool | Purpose |
| --- | --- |
| `identity` | Bound actor/client/Home, active scope, binding mode, selection version, and capabilities. |
| `list`, `get`, `search` | Read task summaries, a full task, or matching task content. |
| `create`, `update`, `reorder`, `transition`, `delete` | Create and manage ordinary task state. |
| `list_types`, `create_type` | Read or add reusable task types. |
| `worker_identity_get`, `worker_identity_list`, `worker_identity_claim` | Read or claim a stable Worker role and one or more task types when Identity Mode is enabled. |
| `run_checks`, `attest` | Run command checks or record a manual check result. |
| `note`, `edit_note`, `delete_note` | Maintain concise task provenance. |

Dependencies already exist before task creation. Generic update does not silently change ownership
or assignment; use the dedicated bulk/lease operations where the change is permitted.

## Sessions and leases

| Tool | Purpose |
| --- | --- |
| `begin`, `heartbeat`, `finish`, `cancel` | Durable agent session lifecycle. |
| `get_session`, `list_sessions` | Inspect session state and health. |
| `lease_claim`, `lease_status`, `lease_renew`, `lease_release` | Durable ownership coordination. |
| `lease_reassign`, `lease_approve` | Deliberate reassignment and pending-request decisions. |

`begin` requires `expected_actor` to equal `identity.actor`. `lease_claim` requires a non-empty
reason and current `expected_version`; a conflict becomes a pending request rather than silently
replacing a holder. With Identity Mode enabled, an Agent must have an identity whose `types`
contains the typed task it is about to execute. Human/system administration remains available.

## Planning, recovery, and audit

| Group | Tools |
| --- | --- |
| Blockers and evidence | `set_blocker`, `add_evidence`, `remove_evidence` |
| Trash and bulk work | `trash`, `trash_many`, `list_trash`, `restore_trash`, `bulk_update`, `bulk_move` |
| Views and templates | `list_views`, `get_view`, `create_view`, `save_view`, `delete_view`, `apply_view`, plus template create/save/delete/instantiate tools |
| Work Logs | `worklog_create`, `worklog_get`, `worklog_list`, `worklog_update`, `worklog_delete`, `worklog_draft_send` |
| Metrics | Worker and project/cluster metrics tools |

Bulk writes supply expected versions for every selected task. An ownership move is explicit and
requires the documented force/reason conditions when it changes project scope. Work Logs belong to
an explicitly selected project store and have server-owned identity/audit fields.

`worklog_draft_send` is available only to an Agent in a project whose Identity Mode is enabled. It
creates a `worker_private`, append-only coordination record with a server-owned versioned envelope.
Optional recipients are actual access controls: only the sender, addressed Agents, and local human
administration can read a directed draft. With no recipients it is a same-project broadcast. Reply
by sending another draft with the same `thread`; drafts cannot be updated or deleted.

## Catalog images are HTTP resources

Custom project and cluster images are intentionally not MCP blobs. Use the Home-only HTTP routes
documented in [HTTP API: catalog presentation assets](/reference/http-api#catalog-presentation-assets).
MCP returns catalog metadata; clients render a custom image through its target-bound URL and fall
back to the token/default when `GET` returns 404.
