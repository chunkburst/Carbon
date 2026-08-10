---
title: Installation
---

# Installation

Carbon v1.0.0 ships four Windows x64 artifacts: a recommended NSIS installer, an MSI installer,
a portable desktop ZIP, and a CLI-only ZIP. They use the same `carbon` binary, `.carbon/` Home,
MCP surface, and `carbon://` deep links where deep links are installed.

## Windows downloads

| Distribution | File | Notes |
| --- | --- | --- |
| NSIS installer (recommended) | `Carbon_1.0.0_x64-setup.exe` | Current-user installation and `carbon://` registration. |
| MSI installer | `Carbon_1.0.0_x64_en-US.msi` | Standard Windows Installer for scripted or managed deployment. |
| Portable desktop | `Carbon-1.0.0-windows-portable.zip` | No installation; Home defaults to the extracted directory. |
| CLI only | `carbon-1.0.0-windows-x64-cli.zip` | CLI, local Web server, and MCP server without the desktop UI. |

Download them with `SHA256SUMS.txt` from the
[Carbon releases](https://github.com/chunkburst/Carbon/releases). v1.0.0 artifacts are currently
unsigned, so Windows may display a SmartScreen prompt.

The NSIS and MSI installers check for Microsoft Edge WebView2 Runtime and download its bootstrapper
when needed. The default installation is scoped to the current user.

## Windows portable

Extract the complete portable archive and run **Carbon Portable.exe**. Keep it beside
`carbon.exe` and `WebView2Loader.dll`. Portable mode is manually updated by replacing the
extracted runtime files, requires the Microsoft Edge WebView2 Runtime, and does not install a
global binary or register a deep link.

The desktop app lets you choose a Home, register standalone projects, optionally create shared
clusters, select custom catalog images, and generate agent configuration.

## Build the CLI from source

Requirements:

- Go version declared by `go.mod`
- Node.js and pnpm only when building the web/desktop UI

```sh
make build
# or
go build -o bin/carbon ./cmd/carbon
```

Initialize a Home and start the local UI:

```sh
./bin/carbon home init --home /work/carbon-home
./bin/carbon web --home /work/carbon-home
```

The server binds loopback only and prints `CARBON_WEB_URL=<url>`. If the requested port is busy,
use the printed URL rather than assuming a port number.

## Connect an MCP client

Use a Home project session as the usual scope:

```sh
carbon serve --actor agent:codex --client codex \
  --home /work/carbon-home --project-session --compat-layer v2
```

The AI creates or selects one active project and keeps using it until an explicit
`select_project`. Existing `--project <id>` configurations remain available as pinned
compatibility mode.

For a deliberately shared task pool, select the optional cluster instead:

```sh
carbon serve --actor agent:codex --client codex \
  --home /work/carbon-home --cluster cluster_product --compat-layer v2
```

The `--compat-layer v2` flag selects the stable Carbon feature contract. It is separate from the
HTTP/MCP transport field `apiVersion: "v1"`.

Use the [agent guides](/agents/) for client-specific configuration examples.

## Environment

| Variable | Purpose |
| --- | --- |
| `CARBON_SHELL` | Shell used for task command checks. |
| `CARBON_WEB_URL` | Machine-readable URL printed by `carbon web`; consumers should read it rather than construct a URL. |
| `CARBON_PARENT_WATCH` | Desktop-sidecar lifecycle signal. |

## Verify your installation

```sh
carbon version
carbon home doctor --home /work/carbon-home
```

Then create or select a standalone project, open the task board, and use `identity` from an MCP
client to verify its actor, Home, project, and stable v2 contract.
