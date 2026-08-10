---
title: OpenCode
---

# OpenCode

Add the following MCP server to OpenCode's configuration. Keep the entry key, command, and
compatibility layer canonical:

```json
{
  "mcp": {
    "carbon": {
      "command": "carbon",
      "args": [
        "serve",
        "--actor", "agent:opencode",
        "--client", "opencode",
        "--home", "/absolute/path/to/carbon-home",
        "--project-session",
        "--compat-layer", "v2"
      ]
    }
  }
}
```

Carbon keeps one active project in this connection; switch explicitly with `select_project`. For an
intentional shared task pool, use an explicit `--cluster` selector instead. Verify identity before
task writes.
