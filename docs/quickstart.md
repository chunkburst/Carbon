---
title: Quickstart
---

# Quickstart

This walkthrough starts with one standalone project. You can add a shared cluster later if your
projects genuinely need one shared task pool.

## 1. Create a Carbon Home

Choose a directory owned by you, then initialize its private Carbon area:

```sh
carbon home init --home /work/carbon-home
carbon web --home /work/carbon-home
```

Open the `CARBON_WEB_URL` printed by the second command. The desktop app can perform the same
steps without a terminal.

## 2. Register a standalone project

In the Home catalog, choose **Add project**, select the source directory, and keep it as a
standalone project. Carbon assigns a stable `project_id` and creates an isolated task store below
the Home. It does not add task data to the source directory.

Create a task in that selected project. Its work, sessions, checks, views, templates, and trash
stay in the project's own store.

## 3. Connect an agent

Use the generated Connect-page configuration, or start the MCP server directly:

```sh
carbon serve --actor agent:claude --client claude \
  --home /work/carbon-home --project-session --compat-layer v2
```

The agent selects the existing project (or creates one), confirms it with `identity`, then follows
`begin`, `heartbeat`, `note`, `run_checks`, and `finish`. That active project remains sticky until
an explicit `select_project`; ordinary task tools cannot override it per call.

## 4. Use custom project appearance when useful

Choose a built-in/emoji token or upload a small PNG, JPEG, or WebP icon in the project catalog.
Carbon normalizes custom input to a content-addressed PNG and binds it to the project. Removing the
image returns the display to its token/default. See [HTTP API: catalog assets](/reference/http-api#catalog-presentation-assets).

## 5. Add a cluster only for shared planning

If several projects must share tasks, create a cluster and register those projects inside it. That
cluster receives one shared task store; project-owned tasks still keep `project_id`. This choice is
explicit and does not migrate or combine existing standalone stores automatically.

## 6. Verify and protect the Home

```sh
carbon home doctor --home /work/carbon-home
carbon snapshot create --home /work/carbon-home
```

Snapshots are local and immutable. A remote upload requires an enabled encrypted profile and an
explicit confirmation. Read [Security](https://github.com/chunkburst/Carbon/blob/main/SECURITY.md)
before configuring remote storage.
