# Task workflow

This repo tracks its work with **Carbon** — the task graph lives as files under `.carbon/`.
Every non-trivial change is a task, driven through its lifecycle. Drive tasks with Carbon's
MCP tools (as an agent) or the web UI (as a human); both go through the same rules.

## Lifecycle

States are defined in `.carbon/config.yaml` (default `backlog → in_progress → in_review →
done | canceled`; closed: `done`, `canceled`). Transitions are free except two gates:

- **deps gate** — a task can't leave the initial state until every dependency is closed
  (its `ready` flag reflects this).
- **checks gate** — moving into a closed state auto-runs the task's `cmd` checks and
  **refuses** if any fail. Manual checks (no `cmd`) must be attested first.

## The agent loop

1. **Identify** — call `identity`; use its exact bound actor for session writes.
2. **Find work** — list ready tasks in the initial state ("what can I start now").
3. **Begin** — call `begin` with `expected_actor` and a unique `idempotency_key`. This
   claims the task, enters the working state, and returns the session id.
4. **Build + heartbeat** — make the change and periodically report concise progress with
   `heartbeat` (status, never chain-of-thought).
5. **Note decisions** — add a short provenance note at each meaningful decision.
6. **Run checks** — run the task's checks before handoff.
7. **Finish** — call `finish` with a useful review summary. This ends the session and moves
   the task to review; it does **not** claim the work is verified or close the task.
8. **Close after review** — transition to a closed state; the checks gate runs and refuses
   on failure.

If work is abandoned, call `cancel` with a reason. This ends the session and releases the
assignment while leaving the task open. Legacy clients may still use `claim` + `transition`,
but their work is not session-observable.

## Note discipline

Provenance is the task's memory. Add a **concise** note (a sentence or two) whenever you:

- make or change a design decision (and why the alternative was rejected),
- deviate from the task's stated intent,
- discover a constraint (a frozen spec, an API limitation),
- hand off, block, or pause (what's left, what's blocking),
- finish — a closing note naming the files touched, the key decision, and how you verified.

Don't note the obvious ("started coding", restating the title) or anything the diff already
says plainly. If a reader of only the provenance couldn't follow the story, you under-noted;
if every entry restates the obvious, you over-noted.

## Authoring tasks

- **Title** — imperative and specific.
- **Body** — intent plus a short checklist; link specs instead of duplicating them. Markdown
  is supported (code blocks are highlighted).
- **Checks** — prefer real commands that actually gate quality; use a `manual` check for what
  a command can't verify. A task carrying a pending manual check parks until it's attested.
- **Deps** — wire real ordering so `ready` means it. Deps must already exist at create time.
- Ids are engine-assigned and **monotonic** — deleting a task file never recycles its id.

### Body style

Write for the next reader — human or agent. Be concise and token-efficient, but structured:

- Open with **one line** of intent (what + why), no heading.
- Add short `##` sections only when the task has real structure — e.g. **Scope**,
  **Dependencies**, **Acceptance**. Skip them for small tasks.
- Use `inline code` for identifiers, paths, and symbols (`OrderTotals`, `src/x.go`, `quote()`).
- Prefer tight bullets over paragraphs. Link specs; don't restate them.

```md
Real delivery pricing on the flat `Order.shipping` field.

## Scope
- Tables: `shipping_zones`, `shipping_rates`. Public: `ShippingRateCalculator`.

## Dependencies
- Prereq `Orders\Public` totals; **blocks** Notifications.
```
