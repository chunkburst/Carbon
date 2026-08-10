---
title: Codex
---

# Codex

Configure a Carbon MCP server named `carbon` in the Codex project or user configuration:

```toml
[mcp_servers.carbon]
command = "carbon"
args = [
  "serve",
  "--actor", "agent:codex",
  "--client", "codex",
  "--home", "/absolute/path/to/carbon-home",
  "--project-session",
  "--compat-layer", "v2",
]
```

The default project session creates/selects one active project and keeps subsequent calls there;
`select_project` is an explicit in-session switch. Use `--project project_id` only for a strict
pinned compatibility connection, or `--cluster` for a deliberately shared task pool.

At task start, call `identity` to verify the actor, Home, project, and MCP v2 contract. Record work
with `begin`, `heartbeat`, `note`, `run_checks`, and `finish` rather than editing task storage.
