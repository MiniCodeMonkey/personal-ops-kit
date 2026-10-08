# Writing a personal ops job

A jobs folder holds the user's jobs, one folder per job (`OPS_JOBS` in
`~/.config/personal-ops/config.env`, default `~/personal-ops`). The personal ops
daemon runs each `run.sh` on its schedule and shows the output in the dashboard
at http://127.0.0.1:7777. A job is three files; there is no registry and no
engine code to touch. New and changed jobs go in the user's jobs folder. The
engine (`~/.personal-ops-kit`, whose `templates/` holds the built-ins copied on
install) stays untouched.

## Design first: code does the work, the model does the judgment

Write every step that has a right answer as plain code: fetching, parsing,
filtering, counting, diffing, deduplicating, deciding whether anything changed.
Call Claude only for the steps that need judgment or prose: ranking, summarizing,
explaining, writing the memo. The best jobs here are mostly shell (or a small
script) with one short model pass at the end.

- Decide in code whether there is anything new. Nothing new ends the run at
  `no-change` with no model call.
- Hand the model a small, prepared digest, not raw pages or whole files.
- Ask the model for a fixed output shape (JSON or KEY=value), then validate it in
  code before acting on it.
- Keep state between runs in `$OPS_STATE_DIR` (a seen-list, a cursor) so each run
  only looks at what is new.

`transcript-miner` and `weekly-review` are the reference shape: plain-code
distill, one `claude -p` pass with no tools, validated output. `nightly-research`
is the shape for a job where the model must use tools; copy its sandbox settings.
`platform-audit` is pure code with no model at all.

## The three files

**`<name>/job.env`**: read by the daemon and sourceable by bash. Only `KEY=value`
lines, quotes and `$HOME` expansion.

```sh
JOB_NAME="my-job"                 # must equal the folder name
JOB_DESCRIPTION="What it does, in one line"
SCHEDULE="0 8 * * *"              # five-field cron, local time
JOB_MODEL="sonnet"                # passed to run.sh as $OPS_MODEL
JOB_TIMEOUT=1800                  # seconds; the whole run is killed after this
CATCHUP_WINDOW="12h"              # a run missed while asleep still runs within this
ARTIFACT_GLOB="*.md"              # which output files the dashboard lists
JOB_ICON="beaker"                 # optional, a name from the engine's internal/ui/icons
JOB_GATED=0                       # 1 = skip when weekly Claude usage is high
GATE_THRESHOLD=60                 # with JOB_GATED=1: skip at or above this 7-day %
CLAUDE_PROFILE_DIR="$HOME/.claude-work"  # optional: run on another Claude Code profile
CLAUDE_PROJECT_DIR="$HOME/notes"  # optional: where "Open in Claude Code" starts
```

Gate a job when it spends a lot of quota (long sessions, Opus). The gate also
skips on battery when the lid is closed or nobody is at the machine.
`CLAUDE_PROFILE_DIR` becomes `CLAUDE_CONFIG_DIR` for the run, so the job uses that
profile's login, settings and MCP servers, and the gate reads that profile's
usage. Leave it out to use the default profile; pointing it at `~/.claude` breaks
the default profile's login.

**`<name>/about.md`**: one short paragraph on what the job does, then a
**How it works** list saying which steps are plain code and which call the model,
and what ends a run at `no-change`. Shown on the job's dashboard page.

**`<name>/run.sh`**: does the work. Start with `#!/bin/bash` and
`set -uo pipefail`, and keep it bash 3.2 compatible (macOS's `/bin/bash`): no
associative arrays, no `${var,,}`, no here-document inside `$(...)`. The runner
provides:

| Variable | Meaning |
| :-- | :-- |
| `OPS_ROOT` | the engine; helpers are in `$OPS_ROOT/bin` (`jobctl`, `lanepick`) |
| `OPS_JOB_DIR` | this job's folder |
| `OPS_ARTIFACT_DIR` | where output belongs (`~/ops/<name>`) |
| `OPS_STATE_DIR` | scratch that survives between runs |
| `OPS_RESULT_FILE` | where to declare the outcome |
| `OPS_MODEL` | `JOB_MODEL` from job.env |
| `OPS_RUN_ID`, `OPS_LOG_FILE`, `OPS_JOBS`, `OPS_ARTIFACTS` | this run, the jobs folder, the output root |

Everything run.sh prints goes to the run log on the dashboard.

## Declaring the outcome

Write `KEY=value` lines to `$OPS_RESULT_FILE` before exiting 0:

```sh
cat > "$OPS_RESULT_FILE" <<EOF
outcome=ok
headline=The finding itself, under 110 characters
artifact=2026-10-08.md
detail=One sentence of context
EOF
```

| Outcome | Use when | Notifies |
| :-- | :-- | :-- |
| `ok` | produced something worth reading | yes |
| `warning` | produced it, with a defect worth flagging | yes |
| `no-change` | ran fine, nothing new | no |
| `failed` | could not do the job | yes |

Use `no-change` for quiet days so notifications stay worth reading. A non-zero
exit is always recorded as failed. `artifact` is a file name inside
`$OPS_ARTIFACT_DIR` matching `ARTIFACT_GLOB`; it is what a notification click opens.

## Calling Claude

For summarizing or judging prepared text, give the model no tools:

```sh
printf '%s' "$PROMPT" | claude -p --model "$OPS_MODEL" --output-format text --tools "" > "$OPS_STATE_DIR/raw.txt"
```

When the model needs tools (shell, web), run it from a scratch folder under
`$OPS_ARTIFACT_DIR` with a `--settings` file that turns on the Claude Code sandbox,
copied from `nightly-research/run.sh`: `failIfUnavailable: true`,
`allowUnsandboxedCommands: false`, writes limited to the job's folders, credential
files denied. Pass `--tools` and `--allowedTools` naming exactly the tools the job
needs. Treat anything fetched from the web or email as untrusted input to the
model, and give that model only read tools.

Prompts start by saying the run is unattended and nobody can answer questions.

## Shared entries (optional)

A job that tracks ongoing items (an open question, a price to watch) can append
them to the shared entries log, and `weekly-review` folds every job's entries
into its Monday memo:

```sh
"$OPS_ROOT/bin/jobctl" entries append --kind price-drop --id "$item" \
  --status open --title "One human sentence, at most 140 characters" --data '{"price": 12}'
"$OPS_ROOT/bin/jobctl" entries fold --job my-job --open-only   # current state as JSON
```

`--status` is `open`, `closed` or `event`. Keep `id` stable for the same subject
so a later `closed` line closes it.

## Done means

1. `jobctl reload`: the daemon reads job.env files at startup.
2. `jobctl list` shows the job with its schedule and no load error.
3. `jobctl run <name> --local` finishes with the outcome you expect, and its
   output file opens from the dashboard.
