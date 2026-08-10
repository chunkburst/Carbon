---
title: Claude Code
---

# Claude Code

Add Carbon as a Home project-session MCP server:

```sh
claude mcp add carbon -- carbon serve \
  --actor agent:claude --client claude \
  --home /absolute/path/to/carbon-home --project-session \
  --compat-layer v2
```

Inspect the installed entry, then start Claude Code in the source project. The server key is
`carbon` and the command is `carbon`. Create/select a project through MCP and confirm it with
`identity`; do not infer an ID from a source folder name.

For an intentional shared pool, use `--cluster cluster_example` instead of project-session mode.
Use `--project project_id` only for an older integration that needs an immutable pinned scope.

Begin each task with `identity`, then use Carbon's `begin → heartbeat → finish` session flow.
