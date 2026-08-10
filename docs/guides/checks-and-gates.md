---
title: Checks and gates
---

# Checks and gates

Carbon task checks are explicit gates, not background automation. They make a task ready for
review only after its dependency and check conditions are satisfied.

## Check types

| Type | Behavior |
| --- | --- |
| Command check | Runs a documented command through the configured shell. |
| Manual check | Requires an explicit `attest` pass/fail result. |

The check list lives with the task. `run_checks` runs command checks and skips manual checks;
`attest` records the outcome of a manual check. A failed or stale required check prevents the
transition that requires it.

## Shell selection

Carbon uses `CARBON_SHELL` when set, otherwise its platform default shell. Set it explicitly on a
machine where the normal shell is unavailable:

```sh
CARBON_SHELL=pwsh carbon serve --actor agent:ci --home /work/carbon-home --project project_site
```

Checks are arbitrary commands in a trusted source repository. Do not run checks from an untrusted
task or repository, and do not put credentials in a command, task body, note, or check output.

## Dependency and review gates

Dependencies must already exist and must meet their own terminal conditions before a dependent task
can pass a dependency gate. `finish` requests review; it does not bypass gates or close the task.
When a task is reopened, required checks are evaluated again according to its transition policy.

## Evidence and blockers

Use a blocker explanation for the concrete condition preventing progress. Use structured Evidence
for a commit, artifact, test run, or link that supports a claim. These v2 mutations require a
current `expected_version`, so a stale agent cannot overwrite later evidence or an updated blocker.

## Practical handoff

1. Run the smallest relevant checks while implementing.
2. Run the task's required checks before `finish`.
3. Record result, command, and limitation in a concise note or structured Evidence.
4. If blocked, set the exact blocker and finish/cancel the session according to the task state.
