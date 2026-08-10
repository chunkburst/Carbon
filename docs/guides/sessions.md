---
title: Sessions
---

# Durable sessions

A Carbon session is an auditable record of one actor working on one task. It survives a client
reconnect and keeps the task's state, assignment, progress, and final summary separate from a
terminal transcript.

## Lifecycle

```text
identity → begin → heartbeat* → finish | cancel
```

`identity` fixes the actor and scope. `begin` requires `expected_actor` to equal that actor and a
unique idempotency key. `heartbeat` refreshes health and can add concise progress. `finish` moves
the session toward review but does not close the task. `cancel` ends the session and leaves the
task open.

## Scope

In a standalone-project connection, sessions live only in that project-owned task store. In a
cluster scope they live in the selected shared pool; a project-bound cluster connection filters
normal access to that member project. Session reads may use the documented same-cluster expansion,
but no operation reaches another Home or cluster.

## Session health

Carbon exposes session status and health so a human can distinguish active work, a clean handoff,
and an abandoned client. An expired lease is not silently reassigned; a later actor follows the
durable lease/request flow.

## Storage and safety

Session data is managed under the selected Carbon store below `.carbon/`. Carbon writes through
locks and atomic replacement, validates the store's containment below its Home, and does not use a
source repository as a session-data root. Avoid manually editing session files; use MCP or the
HTTP/UI surface so provenance and version checks remain intact.
