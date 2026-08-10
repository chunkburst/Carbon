---
title: The agent loop
---

# The Carbon agent loop

Carbon makes an agent's work observable without allowing per-call scope overrides. Connect the
agent to a Home project session by default; it keeps one active project until an explicit switch.
Choose a cluster only when it must work in that shared task pool.

```sh
carbon serve --actor agent:codex --client codex \
  --home /work/carbon-home --project-session --compat-layer v2
```

## Required lifecycle

1. **Select and `identity`** — create/select an active project, then confirm actor, client, Home,
   active scope, `selectionVersion`, and stable v2.
2. **Find work** — list ready tasks in the bound project. A cluster read expansion is explicit.
3. **`begin`** — start a durable session with equal `expected_actor` and a unique idempotency key.
4. **Build and `heartbeat`** — work normally and update durable progress while active.
5. **`note`** — capture concise material decisions and handoff context.
6. **`run_checks`** — run the task's command/manual gate process before handoff.
7. **`finish`** — finish the session into review with a concise summary.
8. **Review then close** — a reviewer transitions the task after its gates pass.

`finish` is not close. Carbon retains the session, actor, notes, checks, and review history so the
next person or agent can understand the work.

## Ownership and conflict handling

Use durable leases when an assignment must survive reconnects. `lease_claim` requires a non-empty
reason and current `expected_version`; a collision becomes a pending request rather than silently
overwriting an existing holder. Use `lease_renew`, `lease_release`, or the documented reassignment
flow instead of changing another actor's assignment through a generic update.

## Scope discipline

- A project session may write only its active project; `select_project` is the sole explicit switch.
- A pinned `home + project` compatibility connection may write only that immutable project store.
- A cluster connection is an explicit shared-pool choice.
- A cluster-project connection is narrowed to a member project for normal reads/writes.
- `include_cluster: true` can expand selected reads within the already-bound cluster; it never
  broadens writes or crosses a Home boundary.

## Good provenance notes

Keep notes brief and factual:

```text
Decision: retained the stable asset endpoint and added ETag revalidation.
Evidence: go test ./internal/home ./internal/server passed.
Risk: custom image remains optional; UI falls back to the configured token.
```

Avoid copying command output, secrets, source files, or speculation into a note. Attach structured
Evidence when a task needs a commit, URL, artifact, or test-run reference.
