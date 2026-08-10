---
layout: home

hero:
  name: Carbon
  text: Durable local task coordination
  tagline: A shared task system for people and coding agents, with standalone projects by default.
  image:
    src: /logo.svg
    alt: Carbon
  actions:
    - theme: brand
      text: Get started
      link: /introduction
    - theme: alt
      text: Connect an agent
      link: /agents/
    - theme: alt
      text: GitHub
      link: https://github.com/chunkburst/Carbon

features:
  - title: Standalone projects first
    details: Each registered project gets an isolated Carbon task store. No cluster is required to begin.
  - title: Optional shared pools
    details: Add a cluster only when related projects intentionally need one shared task pool and cross-project planning.
  - title: Stable MCP v2
    details: Keep one active project in a Home session and switch explicitly with the stable v2 capability contract.
  - title: Safe custom catalog images
    details: Project and cluster images are target-bound, normalized, content-addressed PNG assets under the Home.
  - title: Local by design
    details: Carbon runs on loopback, writes atomically, and keeps task data independent from source repositories.
---

## Start with a Home and a project

```sh
carbon home init --home /work/carbon-home
carbon web --home /work/carbon-home
```

Register a standalone project in the desktop app or through the Carbon catalog API, select it, and
create work. Use a cluster later only for a deliberately shared task pool.

## Connect your agent

```sh
carbon serve --actor agent:codex --client codex \
  --home /work/carbon-home --project-session --compat-layer v2
```

Read [Installation](/installation), complete the [Quickstart](/quickstart), or open an
[agent-specific guide](/agents/).
