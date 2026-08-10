---
title: CLI commands
---

# Carbon CLI commands

`carbon` is the canonical CLI and sidecar binary. It operates on a Carbon Home whose private state
lives below `<home>/.carbon/`. Standalone projects are the default task scope; clusters are an
optional shared-pool extension.

## Command overview

| Command | Purpose |
| --- | --- |
| `carbon home init` | Initialize or open a Carbon Home. |
| `carbon home doctor` | Inspect a Home and optionally apply deterministic metadata repairs. |
| `carbon home import` | Explicitly import a labelled historical source through the migration reader. |
| `carbon snapshot` | Create, verify, or explicitly upload an encrypted Carbon-Home snapshot. |
| `carbon serve` | Run stable MCP v2 over stdio. |
| `carbon web` | Run the local HTTP, SSE, and Streamable HTTP MCP server. |
| `carbon version` | Print the Carbon build version. |

## Home commands

```sh
carbon home init [--home <home>]
carbon home doctor [--home <home>] [--repair]
carbon home import --home <home> --legacy-cluster <source> \
  [--plan <file> | --apply --expected-digest <review-digest>]
```

`home init` creates `<home>/.carbon/home.json`. `home doctor` reports deterministic manifest and
managed-path problems; `--repair` applies only deterministic repairs. `home import` first emits a
reviewable plan. It re-plans under the Home lock during apply and binds apply to the reviewed digest.
See [Legacy migration reader](/migration/0.4).

## Snapshot commands

```sh
carbon snapshot create --home <home>
carbon snapshot verify --home <home> --id <snapshot-id>
carbon snapshot upload --home <home> --id <snapshot-id> --confirm
```

`create` and `verify` are local operations over the Carbon Home. `upload` verifies the snapshot
first, requires an enabled encrypted remote profile, and requires `--confirm`; it does not schedule
or implicitly upload data.

## MCP over stdio

Use a project session for the normal AI workflow:

```sh
carbon serve --actor agent:codex --client codex --compat-layer v2 \
  --home /work/carbon-home --project-session
```

The connection starts at Home scope. A successful MCP `create_project` activates that project;
`select_project` can activate another Home project later without restarting the process. Task and
Work Log tools fail closed until a project is active.

Use `home + project` only when an existing integration needs the previous pinned behavior:

```sh
carbon serve --actor agent:codex --client codex --compat-layer v2 \
  --home /work/carbon-home --project project_site
```

Pinned connections keep their original immutable project boundary and do not expose
`select_project`.

Use a cluster only for an intentional shared task pool:

```sh
carbon serve --actor agent:codex --client codex --compat-layer v2 \
  --home /work/carbon-home --cluster cluster_product
```

A cluster connection may be narrowed to a member project:

```sh
carbon serve --actor agent:codex --client codex --compat-layer v2 \
  --home /work/carbon-home --cluster cluster_product --project project_site
```

`--actor` is required and fixed for the process lifetime. `--client` is optional client metadata.
MCP v2 is the stable Carbon capability contract. `apiVersion: "v1"` in an identity response is the
separate transport version, not a request to use a different feature layer.

`--project-session` requires an explicit `--home` and cannot be combined with `--repo`,
`--cluster`, or `--project`. It is a stable-v2 feature; legacy v1 remains unchanged.

## Local web server

```sh
carbon web [--addr 127.0.0.1:2525] [--actor human:web] [--compat-layer v2] \
  --home <home> [--project <project> | --cluster <cluster> [--project <project>]]
```

The server accepts only loopback addresses. It prints `CARBON_WEB_URL=<url>` to stdout; use that
value because a busy requested port can fall back to another available port. `--parent-watch` stops
a desktop sidecar when its parent closes stdin. For another machine, use an SSH local forward or a
VPN tunnel that terminates on loopback instead of remote binding.

## Environment variables

| Variable | Default | Meaning |
| --- | --- | --- |
| `CARBON_SHELL` | Platform default | Shell used to run task command checks. |
| `CARBON_WEB_URL` | Printed by `carbon web` | Machine-readable resolved web URL. |
| `CARBON_PARENT_WATCH` | Unset | Enables desktop-sidecar parent-stdin shutdown. |

## Scope rules

- Home-only commands and routes manage catalog metadata without opening a task store.
- `--home + --project-session` opens an explicit Home-authorized session whose active project can
  change only through `create_project` or `select_project`.
- `--home + --project` resolves a standalone project only.
- `--home + --cluster` resolves one optional shared task pool.
- `--home + --cluster + --project` resolves a member project within that pool.
- A standalone project cannot be selected through a cluster, and an ambiguous project reference
  fails rather than guessing.
