---
title: Cursor
---

# Cursor

Create or edit Cursor's MCP configuration and add the canonical Carbon server key:

```json
{
  "mcpServers": {
    "carbon": {
      "command": "carbon",
      "args": [
        "serve",
        "--actor", "agent:cursor",
        "--client", "cursor",
        "--home", "/absolute/path/to/carbon-home",
        "--project-session",
        "--compat-layer", "v2"
      ]
    }
  }
}
```

Restart or reload Cursor after saving the configuration. Confirm the connection with `identity`
before asking the agent to mutate tasks.

Project-session mode keeps one active project until `select_project`. If you intentionally need a
shared pool, replace it with `--cluster cluster_example`; use `--project project_id` only for a
strict pinned compatibility connection.
