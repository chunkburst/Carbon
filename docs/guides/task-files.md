---
title: Task files and configuration
---

# Task files and configuration

Carbon stores its private task graph below a selected Home. Source repositories remain source
repositories; Carbon-managed task files, locks, session records, and run evidence live under
`.carbon/` roots that Carbon controls.

## Storage model

```text
<home>/.carbon/
  home.json
  projects/<project-id>/.carbon/       # default standalone project store
  clusters/<cluster-id>/.carbon/       # optional shared-pool store
```

A standalone project has its own task-store configuration with a fixed `project_id`. A cluster
store may contain tasks for several member projects; `task.project_id` records project ownership.
Use a cluster only when a shared pool is intentional.

## Task document principles

Tasks are Markdown with structured frontmatter plus a human-readable body. Carbon, not a caller,
assigns task IDs. Keep identifiers stable and use explicit project IDs, dependency IDs, and version
tokens returned by the API. Do not manufacture a data path from a project name or source directory.

Useful task content is concise and reviewable:

```markdown
## Outcome

Describe the completed behavior.

## Acceptance checks

- State the exact test, review, or observable result.
```

Use MCP/API operations to update tasks so transitions, sessions, notes, evidence, and optimistic
versions stay consistent. Direct task-file edits can bypass those checks and are not a normal agent
workflow.

## Presentation configuration

Built-in/emoji project and cluster tokens are stored in
`<home>/.carbon/catalog-presentation.json`. Custom image metadata and normalized PNG blobs are
separate under `<home>/.carbon/catalog-assets/`; they do not change the token document. See
[catalog presentation assets](/reference/http-api#catalog-presentation-assets).

## Locking and recovery

`.carbon/write.lock` is an advisory OS lock, not proof that a process owns a file. Carbon checks
path containment, acquires the lock, writes temporary data safely, atomically publishes it, and
re-reads critical configuration. Use `carbon home doctor` to inspect a Home rather than deleting or
editing managed files by hand.
