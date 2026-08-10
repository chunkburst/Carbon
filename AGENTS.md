# AGENTS.md — Carbon

Carbon is a file-backed task system for humans and agents. The `carbon` binary exposes one core
rule-set through MCP/stdio and the local web API. Read `SPEC.md` for the active v2 contract and
`docs/` for operational guidance.

**We dogfood Carbon.** Track non-trivial repository work through the Carbon task graph; use the
[agent-loop guide](docs/guides/agent-loop.md) for the lifecycle and concise provenance-note
discipline. Do not edit task storage by hand.

## Backend (Go)

- Gate logic lives only in `internal/task` (pure); MCP and HTTP adapters call it rather than
  reimplementing its rules.
- Run `make check` (gofmt, vet, and tests) before handoff.
- Carbon-owned Home metadata and task stores live below `.carbon/`.
- Standalone projects are the default scope. Use a cluster only when a deliberately shared task
  pool is required.

## Desktop app (Tauri)

Carbon ships as a light cross-platform desktop app. The Go sidecar starts `carbon web
--parent-watch`, prints `CARBON_WEB_URL=<url>`, waits for `/healthz`, and serves the webview. The
same sidecar exposes Streamable HTTP MCP at `/mcp` under a fixed Home/project or optional cluster
scope, plus explicit Home-authorized project-session routing.

- **Development:** `make desktop-up` runs the sidecar, Vite, and native window together; it can
  also be split with `make web` and `make desktop-dev`.
- **Build:** `make desktop` produces the platform bundle under
  `desktop/src-tauri/target/release/bundle/`.
- **Windows portable:** `pnpm.cmd --dir desktop portable:windows` creates a no-install ZIP under
  `desktop/src-tauri/target/portable/` with the native executable and `carbon` sidecar together.
- **Release:** a `v*` tag publishes `Carbon-<version>-windows-portable.zip`.
- The sidecar exits with the app when `carbon web --parent-watch` observes stdin EOF.
- It prefers `127.0.0.1:2525` and reports the chosen port through `CARBON_WEB_URL`.

### Desktop-native features

The desktop app is an always-on hub: closing its window hides it to the tray, a second launch
focuses the existing window, and launch-at-login is opt-in. Native notifications, quick capture,
menus, and window state are implemented in `desktop/src-tauri/` and guarded in the web client so
browser builds remain no-ops.

## UI / design system — required

The web UI should be clean, consistent, and keyboard-friendly.

1. Use shadcn/ui before hand-rolling a component. Check its catalogue first and add the component
   with `cd web && pnpm dlx shadcn@latest add <component>` when it fits.
2. Hand-roll only when shadcn has no equivalent. Match its API shape, use `cn()` and design
   tokens, and place the component under `web/src/components/`.
3. Use semantic Tailwind classes backed by `web/src/style.css`; do not add raw hex or arbitrary
   `oklch(...)` values in components.
4. Keep the visual language restrained: neutral base, one sparse indigo accent, compact spacing,
   subtle borders, quiet transitions, and clear focus states.
5. Reuse the existing spacing, radius, and typography scale. The living reference is `/#design`.

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
