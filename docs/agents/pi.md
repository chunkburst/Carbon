---
title: Pi
---

# Pi

Register Carbon as a stdio MCP server named `carbon`. The exact Pi configuration command can vary
by release; its process arguments should be equivalent to:

```sh
carbon serve --actor agent:pi --client pi \
  --home /absolute/path/to/carbon-home --project-session \
  --compat-layer v2
```

Create or select one active project through Carbon; do not pass a source path as project identity.
An optional `--cluster cluster_example` intentionally selects a shared task pool instead. A fixed
`--project project_id` remains available for pinned compatibility.

Use `identity` after connection, then follow `begin`, `heartbeat`, `note`, `run_checks`, and
`finish`.
