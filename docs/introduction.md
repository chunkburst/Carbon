---
title: Introduction
---

# Introduction

Carbon is a local task system for people and coding agents. It stores durable task state in a
Carbon Home, keeps source repositories separate from Carbon-managed files, and provides the same
scope rules through the desktop UI, HTTP API, SSE, and MCP.

## Start with a standalone project

The default model is intentionally small: select a Home, register one project, and work in that
project's isolated task store. Carbon assigns a stable `project_id`, observes the source path, and
stores task data beneath the Home:

```text
<home>/.carbon/
  home.json
  projects/<project-id>/.carbon/
```

This keeps a project's tasks, sessions, views, templates, trash, and checks together without
requiring a shared pool. A project source folder remains ordinary source code; Carbon does not
scatter its data into it.

## Add a cluster only for shared work

A cluster is optional. Choose one when several projects should intentionally share one task pool
and plan work together. Its data lives at:

```text
<home>/.carbon/clusters/<cluster-id>/.carbon/
```

Projects in that cluster retain `task.project_id` ownership. A project-specific cluster connection
is narrowed to that project; a cluster connection can perform documented shared-pool planning.
Standalone and cluster scopes stay separate.

## What agents receive

Carbon's stable MCP v2 gives an agent a fixed actor and Home authority with one sticky active
project. An agent creates or explicitly selects that project, starts with `identity`, works through
a durable session, records notes and evidence, runs checks, and finishes into review.
The transport version may be `apiVersion: "v1"`; MCP v2 is the independently stable feature
contract.

```sh
carbon serve --actor agent:claude --client claude \
  --home /work/carbon-home --project-session --compat-layer v2
```

See [the agent loop](/guides/agent-loop) and the [MCP reference](/reference/mcp-tools).

## Catalog appearance without unsafe uploads

Projects and optional clusters can use built-in/emoji tokens or a custom image. A custom image is
bound to its target and Home, must be a small valid PNG/JPEG/WebP upload, and is normalized to a
metadata-free PNG under `.carbon/catalog-assets`. See [the HTTP API](/reference/http-api) for the
raw `PUT`, `GET`, and `DELETE` contract.

## Local-first trust model

Carbon has no authentication because it is a local single-user service. `carbon web` listens only
on loopback. Do not expose it directly; when another machine needs access, use an
operator-controlled SSH local forward or VPN tunnel that terminates on loopback. Read the
[security policy](https://github.com/chunkburst/Carbon/blob/main/SECURITY.md) before enabling
checks or backup uploads.
