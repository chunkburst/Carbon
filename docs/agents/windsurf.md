---
title: Windsurf
---

# Windsurf

Add Carbon to Windsurf's MCP server settings with the canonical server name `carbon`:

```json
{
  "mcpServers": {
    "carbon": {
      "command": "carbon",
      "args": [
        "serve",
        "--actor", "agent:windsurf",
        "--client", "windsurf",
        "--home", "/absolute/path/to/carbon-home",
        "--project-session",
        "--compat-layer", "v2"
      ]
    }
  }
}
```

Create or select an active project through Carbon; no cluster is required. The project stays active
until `select_project`. After connecting, ask the agent to call `identity` before beginning work.
