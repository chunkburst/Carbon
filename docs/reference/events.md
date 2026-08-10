---
title: Events (SSE)
---

# Events (SSE)

`GET /api/events` is Carbon's local Server-Sent Events stream. It is served only by the loopback
web server and emits changes for the resolved Carbon scope.

## Scope

Use the same selectors as the HTTP API:

| Scope | Example |
| --- | --- |
| Home-only | `/api/events?home=/work/carbon-home` |
| Standalone project | `/api/events?home=/work/carbon-home&project=project_site` |
| Shared cluster | `/api/events?home=/work/carbon-home&cluster=cluster_product` |
| Cluster project | `/api/events?home=/work/carbon-home&cluster=cluster_product&project=project_site` |

The server validates the scope before subscribing. A project-bound stream does not receive sibling
project events unless a documented cluster scope is selected, and no stream crosses a Home boundary.

## Event envelope

Events use standard SSE fields:

```text
event: task.updated
id: <monotonic-or-opaque-event-id>
data: {"scope": {"home": "…", "project": "…"}, "task": {"id": "…"}}
```

Clients should treat event payloads as invalidation hints, then fetch the current resource with the
same scope. This avoids assuming that a locally cached task, view, lease, session, Work Log, or
catalog presentation remains current after another writer completes an atomic update.

## Reconnect behavior

Use normal `EventSource` reconnect behavior and retain the last event ID where the client supports
it. A reconnect always revalidates its scope. The UI should tolerate missed events by refreshing
the current screen; Carbon's source of truth is the scoped HTTP/MCP read, not an unbounded event
history.

## Security

The stream is local and unauthenticated by design, so do not expose it directly beyond loopback.
Browser-origin rules protect mutations, but a remote tunnel remains an operator-managed security
boundary. See [Security](https://github.com/chunkburst/Carbon/blob/main/SECURITY.md).
