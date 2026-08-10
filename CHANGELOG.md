# Changelog

All notable changes to **Carbon** are documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and this project follows
[Semantic Versioning](https://semver.org/).

The packaged version is defined in `desktop/src-tauri/tauri.conf.json`; the sidecar build stamps
the same value into the `carbon` CLI and MCP/API identity.

## [Unreleased]

## [1.0.0] - 2026-08-10

### Added

- **Multi-platform release artifacts.** Carbon v1.0.0 publishes Windows x64 NSIS
  (`Carbon_1.0.0_x64-setup.exe`), MSI (`Carbon_1.0.0_x64_en-US.msi`), portable
  (`Carbon-1.0.0-windows-portable.zip`), and CLI (`carbon-1.0.0-windows-x64-cli.zip`) files;
  unsigned and unnotarized macOS Apple Silicon and Intel DMGs
  (`Carbon-1.0.0-macos-arm64.dmg`, `Carbon-1.0.0-macos-x64.dmg`) with matching CLI tarballs
  (`carbon-1.0.0-macos-arm64-cli.tar.gz`, `carbon-1.0.0-macos-x64-cli.tar.gz`); and Linux x64
  and ARM64 AppImage, Debian, and CLI tarball files
  (`Carbon-1.0.0-linux-x64.AppImage`, `Carbon-1.0.0-linux-x64.deb`,
  `carbon-1.0.0-linux-x64-cli.tar.gz`, `Carbon-1.0.0-linux-arm64.AppImage`,
  `Carbon-1.0.0-linux-arm64.deb`, `carbon-1.0.0-linux-arm64-cli.tar.gz`). Every file is
  covered by the single `SHA256SUMS.txt` manifest. Windows desktop packages require the
  Windows-only WebView2 Runtime; Linux desktop packages require WebKitGTK at runtime.
- **Worker operations and analytics.** Worker records can be reset or tombstoned without
  changing task files, leases, assignments, provenance, sessions, or Work Logs. Later durable
  activity recreates a deleted Worker from its reset boundary. Scoped reports now include
  project/cluster aggregate cycle metrics and bounded recent work.
- **Work Logs.** Stable Carbon v2 adds durable `worker_private`, `project_public`, and same-Home
  `global_public` logs through HTTP, MCP, and the desktop UI. Server-owned identity/audit fields,
  strict scope checks, and required optimistic versions protect every mutation.
- **Task blocker and Evidence records.** Task detail pages can store a blocker reason and
  structured, auditable Evidence such as commits, artifacts, links, and test runs.
- **Catalog presentation and navigation.** Clusters/projects support safe built-in icon tokens;
  tasks open in refreshable detail routes; task rows expose a keyboard-accessible context menu;
  Worker, Work Logs, and owner logs have dedicated navigation surfaces.
- **Custom catalog images.** A project or optional cluster can now have a target-bound custom
  image through the Home-only `GET`, raw-body `PUT`, and idempotent `DELETE`
  `/api/home/presentation/{kind}/{id}/asset` routes. Carbon validates PNG/JPEG/WebP bytes and
  matching MIME, enforces 1 MiB/4096-pixel/1,048,576-pixel limits, re-encodes to metadata-stripped
  PNG, and stores a content-addressed blob under `.carbon/catalog-assets`.
- **Standalone projects.** A Home can register a project directly into an isolated project-owned
  task store. This is the default Carbon path; clusters remain an optional shared-pool extension.
- **MCP project sessions.** An opt-in Home-authorized v2 connection can create or select one
  sticky active project, keep subsequent task and Work Log calls on it, and switch explicitly
  with `select_project` without reconnecting.
- **Confirmed project removal.** Local human users can remove a project from the Home catalog
  through a two-stage, exact-name confirmation and may opt to clear only that project's task data.
  Source folders, managed roots, configuration, views, templates, Work Logs, sibling projects,
  and shared-cluster data remain protected.

### Changed

- **MCP v2 is stable.** Carbon Home, standalone-project, cluster, and cluster-project connections
  advertise and default to the approved stable `v2` compatibility layer. The HTTP/MCP transport
  `apiVersion` remains `v1` and is explicitly independent of the compatibility layer.
- **Agent Connect defaults to project sessions.** Existing `--project` configurations remain
  strict pinned scopes, while new one-click connections use `--project-session` by default.
- **Responsive Carbon task surface.** Carbon uses compact 32 px status-grouped rows instead
  of Kanban columns. Large disconnected dependency graphs are packed into deterministic 2D
  components, hidden-window polling is paused, task filtering is deferred, and notification
  sounds can be previewed from Settings.
- **Remembered workspace presentation.** Project switches retain the current workspace surface;
  board sections remember their scoped expanded state; row/card presentation is selectable from
  Settings and background context menus; both modes show task importance. The Carbon sidebar now
  restores Carbon's task and agent-work shortcuts, identity, Help, theme, and Connect affordances.
- **Readable dependency overview.** Connected dependency islands remain in the interactive graph,
  while large unlinked task sets use a searchable, progressively revealed grid instead of being
  squeezed into unreadable graph nodes.

### Removed

- **Floating progress window.** The unused floating-progress route, desktop commands, tray/menu
  entry, permissions, persistence, and recovery code have been removed. Quick Capture remains.

## Historical release record — legacy migration source

The entries below preserve historical release facts, including historical identifiers and layouts.
They are not current Carbon instructions; the only supported use of historical on-disk data is the
explicit read-only migration reader described in `docs/migration/0.4.md`.

### [0.4.0]

### Added

- **Carbon home and shared cluster task pools.** A user-selected main directory owns
  `<main>/.carbon/home.json`. Each cluster has an isolated data root, while projects inside one
  cluster share that cluster's task pool. Cluster boundaries keep tasks, sessions, views, trash,
  and backups independent.
- **Stable project ownership.** Carbon assigns durable project ids independent of a source path,
  task prefix, or task id. Project-scoped tasks record `task.project_id`, so a project can be
  moved or relinked without losing task ownership. Cluster-wide reads remain available for
  planning the shared pool.
- **Workflow metadata.** Optional `type` and `importance` fields are distinct from the existing
  `priority` field. Built-in task types are `foundation`, `library`, `patch`, `extension`, and
  `plugin`; importance is `core`, `important`, `normal`, `optional`, or `experimental`.
- **Leases and conflict visibility.** Renewable durable leases prevent silent ownership races.
  A conflicting claim becomes an auditable pending request; reassignment and optimistic versions
  make conflict resolution deliberate.
- **Recoverable deletion.** Tasks can move to trash with durable deletion provenance and original
  project ownership, then be restored or permanently emptied through an explicit action.
- **Planning and execution surfaces.** Worker summaries, full-text search, saved views, bulk
  update or move operations, and reusable task templates support project and cluster planning.
- **Desktop attention flows.** Carbon adds quick capture alongside tray and native notifications
  for ready, failed, assigned, and review events.
- **Immutable snapshots.** Local snapshots are content-addressed and verified. Restore stages
  files into a new directory rather than replacing live data silently.
- **Local snapshots and remote backup choices.** Local snapshots run every six hours and on host
  start by default, reuse unchanged manifests, and retain the newest 30 plus 30 days of local
  history. The default remote provider is `s3`; `cos` supports Tencent Cloud Object Storage.
  Remote publication starts disabled (`enabled=false`) with continuous authorization off. After a
  separately confirmed authorization, scheduled local runs first verify their snapshot, then use
  the same encrypted/read-back-verified remote publication path. Unchanged content at an unchanged
  destination is skipped; remote failures retain the local snapshot and are rate-limited by the
  configured interval.
  S3/COS uploads use client-side AES-256 envelope encryption with externally resolved credentials
  and key material, so plaintext access credentials and encryption keys are never persisted in
  Carbon metadata, task files, logs, or command-line arguments.

### Changed

- **Product name and release artifact.** The visible product is now **Carbon**. Carbon 0.4
  publishes `Carbon-<version>-windows-portable.zip` as its only release asset. The compatible
  `cairn` binary, `.cairn/` workspace format, MCP config key, repository identity, and
  `cairn://` deep-link scheme intentionally remain unchanged; locally installed builds also
  accept the new `carbon://` alias.
- **Portable-first Windows guidance.** The Carbon portable ZIP is the preferred no-install
  Windows path. It ships the Carbon executable and the adjacent `cairn.exe` sidecar together;
  portable updates replace the extracted artifact instead of using the installer update channel.

### Removed

- **Automatic updater feed.** Carbon 0.4 does not register the updater and sets
  `createUpdaterArtifacts=false`; releases publish only the Windows portable ZIP and do not
  publish a current `latest.json` feed. The `latest.json` reference in the 0.2 history below
  describes that older release only.

### Migration and compatibility

- Existing project-local `cairn --repo` workflows continue without conversion. Existing task
  Markdown stays valid; new Carbon fields are optional and unknown frontmatter remains preserved.
- Importing a 0.3 `.cairn-cluster.json` registry is a read-only preflight followed by an explicit,
  digest-checked apply. Carbon backs up and reads the source stores, then copies their reviewed
  config, tasks, sessions, and recognized run evidence into a new central cluster store. Target
  task copies gain stable `project_id` and mapped collision/reference rewrites; source files are
  never renamed, moved, deleted, or rewritten.
- Offline sources remain represented by their stable project ids and can be explicitly relinked
  later. Ambiguous source folders fail closed rather than being assigned to a guessed project.

### Security

- Carbon homes and cluster data roots are resolved and validated before use. Carbon rejects scope
  ambiguity, unsafe metadata paths, and cross-cluster task-store resolution.
- Backup publication is opt-in and immutable. Encrypted object storage uses authenticated,
  per-object envelopes and resolves master keys only at operation time.

### [0.3.0]

### Added

- **Legacy project clusters.** The upper-left selector opened a cluster root whose registry was
  `.cairn-cluster.json`. Registered projects retained their own `.cairn` data, with tasks,
  sessions, and MCP context kept project-scoped. Existing single-project roots could be
  registered as legacy projects without copying or migrating their data.
- **Project-scoped Codex setup for added projects.** The Add project flow makes a best-effort
  write of that project's `.codex/config.toml`; a failed Codex setup does not undo the project
  registration and can be retried from Connect.

### [0.2.0]

### Added

- **Git-aware review context.** Each session derives review evidence from the checkout:
  branch, start or finish commits, files changed, commits, and uncommitted state. The task detail
  page surfaces actionable warnings for a dirty finish, stale checks, or no changes.
- **One-click agent integrations.** The **Connect** page detects installed agents and writes
  their MCP config under their own identities (`agent:<id>`). Auto-connect supports Claude Code,
  Cursor, Codex, Windsurf, OpenCode, Kilo Code, and Pi; Antigravity and other MCP clients have a
  copy-paste guide. Configuration writes modify only the compatible `cairn` entry, are atomic,
  create `<file>.bak` on overwrite, and are re-read for verification.
- **Documentation site.** A VitePress site under `docs/` provides guides, per-agent pages, and
  HTTP, MCP, and CLI reference material.
- **Cross-platform desktop installers.** The release workflow builds macOS, Windows, and Linux
  desktop installers and publishes them with a signed `latest.json` feed. The Go binary ships as
  an embedded sidecar, and the project builds on Windows with platform-aware file-lock and
  check-timeout handling.
- `SECURITY.md` and this changelog.

### [0.1.0]

Initial release.

### Added

- **File-based task graph** under `.cairn/`: tasks as Markdown (YAML frontmatter plus prose
  body), engine-assigned collision-free ids, dependencies, and a two-gate transition model.
- **MCP server** (`cairn serve`) over stdio, plus MCP over Streamable HTTP from `cairn web`.
- **Web UI** (`cairn web`): task board, dependency graph, and live updates over SSE.
- **Observable agent sessions:** `begin`/`heartbeat`/`finish`/`cancel` with stall detection and
  review handoff.
- **Desktop app:** the same binary embedded in a Tauri shell with a live menu-bar tray.
