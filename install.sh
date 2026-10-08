#!/bin/bash
# Install or update personal ops. Safe to run again: it rebuilds the engine,
# reinstalls the background service, and leaves your jobs and settings alone.
#
#   curl -fsSL https://raw.githubusercontent.com/MiniCodeMonkey/personal-ops-kit/main/install.sh | bash
#   ./install.sh                 from a clone
#
# Your settings live in ~/.config/personal-ops/config.env (created on first run
# from config.example.env). Environment overrides for a single run:
#   PERSONAL_OPS_HOME    where the engine is cloned (default ~/.personal-ops-kit)
#   OPS_JOBS, OPS_ARTIFACTS, OPS_NOTIFICATIONS   as in config.env
#   INSTALL_SKILL=1|0    install the Claude Code skill without asking
#   OPS_DRY_RUN=1        do everything except load the service and register the
#                        notification app (for testing against a temporary HOME)

set -euo pipefail

REPO_URL="${PERSONAL_OPS_REPO:-https://github.com/MiniCodeMonkey/personal-ops-kit.git}"
CONFIG="$HOME/.config/personal-ops/config.env"
SKILLS_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills"

say() { printf '\033[1m==>\033[0m %s\n' "$*"; }
die() {
    printf '\033[31merror:\033[0m %s\n' "$*" >&2
    exit 1
}

# ---------------------------------------------------------------- prerequisites

[ "$(uname -s)" = "Darwin" ] || die "personal ops runs on macOS only (it uses launchd and the macOS sandbox)."

if ! xcode-select -p >/dev/null 2>&1; then
    xcode-select --install >/dev/null 2>&1 || true
    die "the Xcode Command Line Tools are needed. A macOS installer window should have opened; finish it, then run this again."
fi

if ! command -v claude >/dev/null 2>&1; then
    die "Claude Code is not installed. Install it with:
    curl -fsSL https://claude.ai/install.sh | bash
then open a new terminal, run 'claude' once to log in, and run this again."
fi

# Homebrew is the easy way to get Go; the engine is a Go program built locally.
need_brew() {
    command -v brew >/dev/null 2>&1 && return 0
    die "$1 is needed and Homebrew is not installed. Install Homebrew from https://brew.sh (or $1 yourself), then run this again."
}
for tool in go jq; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        need_brew "$tool"
        say "installing $tool with Homebrew"
        brew install "$tool"
    fi
done

# ---------------------------------------------------------------------- engine

# Run from a clone: use it. Piped from curl: clone or update PERSONAL_OPS_HOME.
SCRIPT_DIR=""
if [ -n "${BASH_SOURCE[0]:-}" ] && [ -f "${BASH_SOURCE[0]}" ]; then
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fi
if [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/cmd/jobctl/main.go" ]; then
    ENGINE="$SCRIPT_DIR"
else
    ENGINE="${PERSONAL_OPS_HOME:-$HOME/.personal-ops-kit}"
    if [ -d "$ENGINE/.git" ]; then
        say "updating $ENGINE"
        git -C "$ENGINE" pull --ff-only --quiet
    else
        say "downloading personal ops to $ENGINE"
        git clone --quiet "$REPO_URL" "$ENGINE"
    fi
fi

say "building"
make -C "$ENGINE" build >/dev/null

# -------------------------------------------------------------- your settings

if [ ! -f "$CONFIG" ]; then
    mkdir -p "$(dirname "$CONFIG")"
    cp "$ENGINE/config.example.env" "$CONFIG"
    echo "    created $CONFIG"
fi
# Values already in the environment win over the file, for one-off overrides.
env_jobs="${OPS_JOBS:-}" env_artifacts="${OPS_ARTIFACTS:-}" env_notify="${OPS_NOTIFICATIONS:-}"
# shellcheck source=/dev/null
. "$CONFIG"
JOBS="${env_jobs:-${OPS_JOBS:-$HOME/personal-ops}}"
ARTIFACTS="${env_artifacts:-${OPS_ARTIFACTS:-$HOME/ops}}"
NOTIFICATIONS="${env_notify:-${OPS_NOTIFICATIONS:-1}}"

# ------------------------------------------------------------------- your jobs

# Built-in jobs are copied once and then belong to you: a job folder that already
# exists is never overwritten. The authoring guide is engine documentation, so it
# is refreshed every time.
mkdir -p "$JOBS"
for dir in "$ENGINE"/templates/*/; do
    name="$(basename "$dir")"
    if [ -e "$JOBS/$name" ]; then
        echo "    kept your $name"
    else
        cp -R "$dir" "$JOBS/$name"
        echo "    added $name"
    fi
done
cp "$ENGINE/templates/CLAUDE.md" "$JOBS/CLAUDE.md"

# jobctl on PATH, as a link into the engine so updates carry through.
mkdir -p "$HOME/.local/bin"
ln -sfn "$ENGINE/bin/jobctl" "$HOME/.local/bin/jobctl"

# ---------------------------------------------------------- Claude Code skill

# The skill lets you say "make me a personal ops job that ..." in any Claude Code
# session. Linked, not copied, so it updates with the engine.
want_skill="${INSTALL_SKILL:-}"
if [ -z "$want_skill" ]; then
    want_skill=1
    if [ -r /dev/tty ] && [ -z "${OPS_DRY_RUN:-}" ]; then
        printf 'Install the personal-ops skill into Claude Code so you can ask Claude to write jobs for you? [Y/n] '
        read -r answer </dev/tty || answer=""
        case "$answer" in [nN]*) want_skill=0 ;; esac
    fi
fi
if [ "$want_skill" = "1" ]; then
    mkdir -p "$SKILLS_DIR"
    ln -sfn "$ENGINE/skills/personal-ops" "$SKILLS_DIR/personal-ops"
    echo "    skill linked at $SKILLS_DIR/personal-ops"
fi

# ------------------------------------------------------------------- service

say "installing the background service"
if [ -n "${OPS_DRY_RUN:-}" ]; then
    export OPS_SKIP_LAUNCHCTL=1
fi
OPS_ROOT="$ENGINE" OPS_ARTIFACTS="$ARTIFACTS" OPS_JOBS="$JOBS" OPS_NOTIFICATIONS="$NOTIFICATIONS" \
    "$ENGINE/bin/jobctl" install-daemon

cat <<EOF

Personal ops is installed.

  Dashboard     http://127.0.0.1:7777   (or: jobctl open)
  Your jobs     $JOBS
  Output        $ARTIFACTS
  Settings      $CONFIG

Next steps:
  1. Edit your research topics in $JOBS/nightly-research/lanes/
  2. Try a job now:      jobctl run transcript-miner
  3. Make your own:      open Claude Code and say
                         "make me a personal ops job that ..."

If 'jobctl' is not found, add ~/.local/bin to your PATH or use $ENGINE/bin/jobctl.
Update later:  jobctl update
Uninstall:     $ENGINE/uninstall.sh
EOF
