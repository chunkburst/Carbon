---
title: Installation
---

# Installation

Carbon v1.0.0 ships desktop and CLI artifacts for Windows x64, macOS (Apple Silicon and Intel),
and Linux (x64 and ARM64). They use the same `carbon` binary, `.carbon/` Home, MCP surface, and
`carbon://` deep links where deep links are installed.

## Release matrix

Download the artifact that matches both your operating system and CPU architecture. Desktop
artifacts include the Carbon UI; CLI artifacts include the CLI, local Web server, and MCP server
without the desktop UI.

| Platform | Desktop artifacts | CLI artifact |
| --- | --- | --- |
| Windows x64 | NSIS: `Carbon_1.0.0_x64-setup.exe`<br>MSI: `Carbon_1.0.0_x64_en-US.msi`<br>Portable: `Carbon-1.0.0-windows-portable.zip` | `carbon-1.0.0-windows-x64-cli.zip` |
| macOS Apple Silicon (arm64) | `Carbon-1.0.0-macos-arm64.dmg` | `carbon-1.0.0-macos-arm64-cli.tar.gz` |
| macOS Intel (x64) | `Carbon-1.0.0-macos-x64.dmg` | `carbon-1.0.0-macos-x64-cli.tar.gz` |
| Linux x64 | AppImage: `Carbon-1.0.0-linux-x64.AppImage`<br>Debian package: `Carbon-1.0.0-linux-x64.deb` | `carbon-1.0.0-linux-x64-cli.tar.gz` |
| Linux ARM64 | AppImage: `Carbon-1.0.0-linux-arm64.AppImage`<br>Debian package: `Carbon-1.0.0-linux-arm64.deb` | `carbon-1.0.0-linux-arm64-cli.tar.gz` |

All artifacts are published with one `SHA256SUMS.txt` manifest in the
[Carbon releases](https://github.com/chunkburst/Carbon/releases). The manifest covers every
Windows, macOS, and Linux desktop and CLI file in that release; there are no platform-specific
checksum files. From the directory containing the downloads, verify the complete set on Linux
with:

```sh
sha256sum --check SHA256SUMS.txt
```

macOS ships the compatible `shasum` command instead:

```sh
shasum -a 256 --check SHA256SUMS.txt
```

On Windows, compare an individual download with its manifest entry by running
`Get-FileHash -Algorithm SHA256 <file>` in PowerShell.

## Windows downloads

| Distribution | File | Notes |
| --- | --- | --- |
| NSIS installer (recommended) | `Carbon_1.0.0_x64-setup.exe` | Current-user installation and `carbon://` registration. |
| MSI installer | `Carbon_1.0.0_x64_en-US.msi` | Standard Windows Installer for scripted or managed deployment. |
| Portable desktop | `Carbon-1.0.0-windows-portable.zip` | No installation; Home defaults to the extracted directory. |
| CLI only | `carbon-1.0.0-windows-x64-cli.zip` | CLI, local Web server, and MCP server without the desktop UI. |

The v1.0.0 Windows installers and portable executable are unsigned, so Windows may display a
SmartScreen prompt. The single checksum manifest described above covers these files as well.

Microsoft Edge WebView2 Runtime is a Windows-only dependency. The NSIS and MSI installers check
for it and download its bootstrapper when needed; the portable desktop app also requires the
runtime. The macOS and Linux packages do not use WebView2. The default Windows installation is
scoped to the current user.

## Windows portable

Extract the complete portable archive and run **Carbon Portable.exe**. Keep it beside
`carbon.exe` and `WebView2Loader.dll`. Portable mode is manually updated by replacing the
extracted runtime files, requires the Microsoft Edge WebView2 Runtime, and does not install a
global binary or register a deep link.

The desktop app lets you choose a Home, register standalone projects, optionally create shared
clusters, select custom catalog images, and generate agent configuration.

## macOS downloads

Choose the DMG for Apple Silicon (`arm64`) or Intel (`x64`) on macOS 12 Monterey or newer. The
v1.0.0 DMGs are intentionally unsigned and unnotarized. macOS may show a Gatekeeper warning on
first launch; use the normal
Finder **Open** confirmation only after verifying that the file came from the Carbon release and
matches `SHA256SUMS.txt`.

The matching CLI tarballs are `carbon-1.0.0-macos-arm64-cli.tar.gz` and
`carbon-1.0.0-macos-x64-cli.tar.gz`. Extract one, then place the `carbon` executable on your
`PATH` if you want to invoke it from any shell.

## Linux downloads

AppImage and Debian (`.deb`) desktop packages are available for x64 and ARM64. Linux desktop
packages require the distribution-provided WebKitGTK runtime (and its GTK system dependencies) at
runtime; install WebKitGTK using your distribution's package manager before starting Carbon.

The matching CLI tarballs are `carbon-1.0.0-linux-x64-cli.tar.gz` and
`carbon-1.0.0-linux-arm64-cli.tar.gz`. Extract one, mark `carbon` executable if needed, and place
it on your `PATH`.

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
