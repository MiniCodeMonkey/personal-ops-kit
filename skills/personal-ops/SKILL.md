---
name: personal-ops
description: Personal ops, scheduled Claude jobs that run locally on this Mac. Use to install personal ops, to create or change a personal ops job ("make me a job that..."), or to add a research lane.
---

# Personal ops

Personal ops is an engine (a launchd daemon plus `jobctl`) that runs the user's
jobs on a schedule. Two places matter, and they never mix:

- **Engine**: `~/.personal-ops-kit`. Read-only to you. Updated only by
  `jobctl update`.
- **Jobs folder**: the user's own, set by `OPS_JOBS` in
  `~/.config/personal-ops/config.env` (default `~/personal-ops`). Every job you
  write or edit lives here, one folder per job.

## Install

1. Run `curl -fsSL https://raw.githubusercontent.com/MiniCodeMonkey/personal-ops-kit/main/install.sh | bash`.
   When it stops on a missing prerequisite, follow its message (it names the exact
   fix) and run it again.
2. Done when the installer prints "Personal ops is installed" and `jobctl list`
   shows the built-in jobs. Tell the user the dashboard is http://127.0.0.1:7777.

## Create or change a job

1. Read `<jobs folder>/CLAUDE.md`. It is the job format: the three files,
   runner variables, outcomes, gating, profiles, and the sandbox pattern.
2. Read the built-in job closest in shape to the request, in the jobs folder.
3. Settle with the user anything you cannot infer: schedule, what counts as
   "nothing new", and which Claude profile and gate to use if they keep several
   profiles or worry about quota.
4. Design it code-first: list each step and mark it code or model. Every step
   with a right answer is code; the model gets only judgment and writing, over a
   small digest.
5. Write `job.env`, `about.md` and `run.sh` in `<jobs folder>/<name>/`.
6. Done when `jobctl reload`, then `jobctl list` shows the job without a load
   error, and `jobctl run <name> --local` ends with the outcome you expect.

## Add a research lane

Write `<jobs folder>/nightly-research/lanes/<slug>.md`: a short brief naming the
territory, what a good finding looks like, and the sources to prefer, using an
existing lane as the model. Done when
`~/.personal-ops-kit/bin/lanepick --lanes=<that lanes folder> --list` lists it.
