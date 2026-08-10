# Carbon instructions for Claude Code

This repository uses Carbon for durable task coordination. Use the `carbon` MCP server with a
Home-authorized project session and stable MCP v2. A cluster is optional and is selected only for
deliberate shared-pool work.

```sh
carbon serve --actor agent:claude --client claude \
  --home /absolute/path/to/carbon-home --project-session \
  --compat-layer v2
```

Create or select one active project, confirm it with `identity`, then use `begin`, `heartbeat`, `note`, `run_checks`, and
`finish` for the task lifecycle. Do not edit Carbon-managed task files directly. See
[AGENTS.md](AGENTS.md) and [the agent-loop guide](docs/guides/agent-loop.md).

<!-- carbon:agent-loop:start -->
## Agent loop — required

All work in this repo is tracked in **Carbon** (the task graph under `.carbon/`). Drive every
non-trivial change through a task using Carbon's MCP tools — don't edit task files by hand:

1. **select + identity** — create/select the intended active project, then confirm actor, scope,
   and `selectionVersion`.
2. **find work** — list ready tasks in the initial state.
3. **begin** — claim a task and open a session (`expected_actor` + a unique `idempotency_key`).
4. **build + heartbeat** — make the change; report concise progress.
5. **note** — add a short provenance note at each meaningful decision.
6. **run_checks** — run the task's checks before handoff.
7. **finish** — end the session into review with a summary.
8. **close** — transition to a closed state once reviewed (re-runs checks).

Full lifecycle, gates, and note discipline: [.carbon/WORKFLOW.md](.carbon/WORKFLOW.md).
<!-- carbon:agent-loop:end -->
