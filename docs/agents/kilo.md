---
title: Kilo Code
---

# Kilo Code

Configure Kilo Code's MCP settings with a Carbon server named `carbon`:

```json
{
  "mcpServers": {
    "carbon": {
      "command": "carbon",
      "args": [
        "serve",
        "--actor", "agent:kilo",
        "--client", "kilo",
        "--home", "/absolute/path/to/carbon-home",
        "--project-session",
        "--compat-layer", "v2"
      ]
    }
  }
}
```

Restart the MCP connection after saving. Create or select one active project, then confirm it with
`identity`; it remains active until `select_project`. Choose a `--cluster` only when the task truly
belongs to a shared pool.
