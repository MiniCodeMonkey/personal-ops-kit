# personal ops

Scheduled jobs for your Mac, written with and run by Claude Code.

## The idea

- **It runs on a schedule, like cron, on your own computer.** Unlike Claude Code
  routines or scheduled cloud agents, nothing runs in the cloud. Jobs see your
  files, your apps and your Claude Code setup.
- **Jobs are mostly plain code.** Fetching, filtering, counting and deciding
  whether anything changed is ordinary code. Claude is called only for the parts
  where it helps: judging, summarizing, writing. A quiet day costs no tokens.
- **Jobs can act on your computer, inside a partial sandbox.** A job's own script
  runs as you, like any script you run. When a job gives Claude tools, Claude Code
  runs with its sandbox on: every shell command runs under the macOS sandbox
  (Seatbelt), file writes are limited to the job's own folders, credential files
  such as `~/.ssh` and `~/.aws` and credential environment variables are
  unreadable, and ssh cannot leave the machine. Web access stays open and Claude
  can read most of your files. Close to full autonomy, with the dangerous parts
  fenced off.
- **Jobs can use a specific Claude account.** If you keep several Claude Code
  profiles (say personal and work), each job can pick one.
- **Jobs can be gated on usage.** For example "only run if my weekly Claude usage
  is below 60%". The built-in research job does this, so it skips a night rather
  than eat into the quota you need tomorrow.

Install the engine once and keep your jobs in your own folder, which can be its
own private git repository. Do not fork this repo: forks of public repositories
cannot be private, and updates come from `jobctl update`.

## Install

You need a Mac with [Claude Code](https://claude.ai/code) installed and logged in.

```bash
curl -fsSL https://raw.githubusercontent.com/MiniCodeMonkey/personal-ops-kit/main/install.sh | bash
```

The installer checks prerequisites (and installs Go and jq with Homebrew if
needed), puts the engine in `~/.personal-ops-kit`, copies the built-in jobs into
`~/personal-ops`, starts a background service, and offers to install a Claude
Code skill. Then open the dashboard at http://127.0.0.1:7777.

Or just ask Claude Code: "install personal ops from github.com/MiniCodeMonkey/personal-ops-kit".

## Built-in jobs

| Job | What it does | Model use |
| :-- | :-- | :-- |
| `nightly-research` | Each evening, researches one of your topics ("lanes") and leaves an HTML memo. Gated on weekly usage. | One sandboxed session with shell and web |
| `transcript-miner` | Mondays, reads your Claude Code sessions for corrections you keep repeating and workflows worth turning into a skill. | One pass over a digest built in code |
| `weekly-review` | Mondays, one memo folding together everything your jobs tracked this week. | One pass over a digest built in code |
| `platform-audit` | Monthly, reports which jobs ran, failed or went quiet. | None |

They are yours once installed: edit them in `~/personal-ops`. Updates never
overwrite them.

## Add a job

Just ask Claude. With the skill installed, in any Claude Code session:

> Make me a personal ops job that checks my local library's new arrivals every
> Friday and tells me about anything by authors I have read before.

Claude reads the job format in `~/personal-ops/CLAUDE.md`, writes the job into
`~/personal-ops/<name>/`, and test-runs it.

By hand, a job is a folder with three files: `job.env` (schedule, model, gate,
profile), `about.md` (shown on the dashboard) and `run.sh` (the work). The format
is documented in [`templates/CLAUDE.md`](templates/CLAUDE.md). After adding or
changing a `job.env`, run `jobctl reload`.

## Add research lanes

Each file in `~/personal-ops/nightly-research/lanes/` is one topic. The file name
is the lane's name and the text is the brief: what territory to cover, what a good
finding looks like, which sources to trust. The job rotates through lanes, picking
the one that has waited longest.

```markdown
---
weight: 2        # optional: higher comes round more often
months: 4-9      # optional: only in season
---
Vegetable gardening in a cool, wet climate. ...
```

Three examples ship with it: a hobby, industry news, and AI tooling. Edit or
delete them freely.

## Gates and profiles

In a job's `job.env`:

```sh
JOB_GATED=1                              # check usage before running
GATE_THRESHOLD=60                        # skip at or above 60% of the weekly limit
CLAUDE_PROFILE_DIR="$HOME/.claude-work"  # run on this Claude Code profile
```

The gate reads the usage Claude Code itself records for that profile, and also
skips when the laptop is on battery with the lid closed. Leave
`CLAUDE_PROFILE_DIR` out to use your default profile.

## Everyday commands

```bash
jobctl list                  # jobs, schedules, last runs
jobctl run <job>             # run one now (--force skips the gate)
jobctl logs <job>            # the latest run's output
jobctl open                  # the dashboard
jobctl reload                # after editing a job.env
jobctl update                # get the latest engine
```

Settings (where jobs and output live, notifications on or off) are in
`~/.config/personal-ops/config.env`.

## Uninstall

```bash
~/.personal-ops-kit/uninstall.sh
```

This removes the background service, the notification app and the links. Your
jobs, settings and output stay until you delete them.

## License

MIT
