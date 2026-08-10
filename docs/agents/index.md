---
title: Connect an agent
---

# Connect an agent

Carbon connects coding agents through stable MCP v2. The usual connection binds one agent to a
Carbon Home and keeps one explicit active project for that connection. It does not require a
cluster or reconnect to switch projects.

```sh
carbon serve --actor agent:<client> --client <client> \
  --home /absolute/path/to/carbon-home --project-session \
  --compat-layer v2
```

The MCP configuration key is always `carbon`. Start each agent session with `identity` and check its
actor, Home, active project, `selectionVersion`, and `stableCompatLayer: "v2"` before writing.

## Choose a scope deliberately

| Scope | When to use it |
| --- | --- |
| `--home + --project-session` | Default: create/select one sticky active project; switch explicitly without reconnecting. |
| `--home + --project` | Compatibility: one immutable pinned project until reconnect. |
| `--home + --cluster` | An intentionally shared task pool. |
| `--home + --cluster + --project` | One member project inside that shared pool. |

Do not use a cluster just to start work. Add it later only when several projects should share one
task pool. Task calls stay on the active project; only `select_project` changes it.

## Common agent lifecycle

1. Call `identity`.
2. Find ready work in the bound scope.
3. Call `begin` with `expected_actor` and an idempotency key.
4. Use `heartbeat` while active and `note` for material decisions.
5. Run checks, then `finish` into review.

Use the client-specific pages below for the configuration file or command. Every page uses the
canonical `carbon` binary and MCP key.

- [Claude Code](/agents/claude)
- [Cursor](/agents/cursor)
- [Codex](/agents/codex)
- [Windsurf](/agents/windsurf)
- [OpenCode](/agents/opencode)
- [Kilo Code](/agents/kilo)
- [Pi](/agents/pi)
- [Antigravity](/agents/antigravity)
