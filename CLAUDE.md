# personal-ops-kit (the engine)

This repository is the engine: `jobctl` (daemon, scheduler, runner, dashboard),
helper binaries, `lib/gate.sh`, the installer, and built-in job templates. Users
install it once and keep their jobs in their own folder, so the job format in
`templates/CLAUDE.md` is a public contract: extend it, keep old `job.env` keys
and runner variables working.

- Authoring or changing a job, including a built-in template: `templates/CLAUDE.md`.
- A user's own job never belongs in this repository; it goes in their jobs folder.
- `make test` runs the Go tests, `lib/gate_test.sh` and `test/install_test.sh`.
- `templates/*/` are copied into new installs only; existing users keep their
  copies, so a template fix reaches them only if they re-copy it.

## Gotchas that fail silently

- macOS ships bash 3.2: no `$'\t'` inside a parameter expansion, and no
  here-document nested inside `$(...)`. Editing a running bash script can corrupt
  it; write a new file and `mv` it into place.
- Claude Code sandbox settings default the wrong way for unattended runs:
  `failIfUnavailable` must be `true` and `allowUnsandboxedCommands` `false`.
  There is no built-in credential deny list; only `credentials.files` entries are
  protected.
- `Write(path)` permission rules do nothing (only `Edit(path)` applies to file
  tools), and an absolute path in a rule needs a leading double slash:
  `Edit(//Users/...)`.
- Never set `CLAUDE_CONFIG_DIR` to the default `~/.claude`: `claude` then fails to
  find its credentials and rewrites `~/.claude.json` from scratch. Leave it unset
  for the default profile.
- launchd starts the daemon with only the PATH in its plist; the installer puts
  the directory holding `claude` first.
- `make css` (Tailwind, downloaded on demand) is needed only after changing
  classes in `internal/ui/templates`; the compiled CSS is committed.
