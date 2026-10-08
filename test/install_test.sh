#!/bin/bash
# Runs install.sh twice against a throwaway HOME with OPS_DRY_RUN=1, so nothing
# is loaded into launchd and no app is registered. Checks what a first install
# creates and that a second install keeps the user's edits.

set -uo pipefail

ENGINE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FAKE_HOME="$(mktemp -d)"
trap 'rm -rf "$FAKE_HOME"' EXIT

# Keep Go's caches where they are, so the build neither starts cold nor downloads.
export GOCACHE GOMODCACHE GOPATH
GOCACHE="$(go env GOCACHE)" GOMODCACHE="$(go env GOMODCACHE)" GOPATH="$(go env GOPATH)"

fail=0
check() {
    if eval "$2"; then printf 'ok   %s\n' "$1"; else printf 'FAIL %s\n' "$1"; fail=1; fi
}

install() {
    HOME="$FAKE_HOME" OPS_DRY_RUN=1 INSTALL_SKILL=1 bash "$ENGINE/install.sh" >"$FAKE_HOME/install.log" 2>&1
}

install
status=$?
JOBS="$FAKE_HOME/personal-ops"
PLIST="$FAKE_HOME/Library/LaunchAgents/dev.personal-ops.daemon.plist"

check "first install succeeds" '[ "$status" -eq 0 ] || { cat "$FAKE_HOME/install.log"; false; }'
check "config created from the example" '[ -f "$FAKE_HOME/.config/personal-ops/config.env" ]'
for job in nightly-research transcript-miner weekly-review platform-audit; do
    check "template $job copied" '[ -f "$JOBS/$job/job.env" ] && [ -f "$JOBS/$job/run.sh" ]'
done
check "authoring guide copied" '[ -f "$JOBS/CLAUDE.md" ]'
check "plist points at the user's jobs" 'grep -q "<string>$JOBS</string>" "$PLIST"'
check "plist names the engine binary" 'grep -q "<string>$ENGINE/bin/jobctl</string>" "$PLIST"'
check "plist is valid" 'plutil -lint "$PLIST" >/dev/null'
check "skill linked" '[ "$(readlink "$FAKE_HOME/.claude/skills/personal-ops")" = "$ENGINE/skills/personal-ops" ]'
check "jobctl linked" '[ -x "$FAKE_HOME/.local/bin/jobctl" ]'

research_prompt="$(OPS_ROOT="$ENGINE" OPS_ARTIFACT_DIR="$FAKE_HOME/ops/nightly-research" \
    bash "$JOBS/nightly-research/run.sh" --dry-run 2>&1)"
check "research dry run picks a lane" 'printf "%s" "$research_prompt" | grep -q "Tonight.s lane: "'
check "research sandbox settings are valid JSON" \
    'printf "%s" "$research_prompt" | sed -n "/^--- settings ---$/,\$p" | tail -n +2 | jq -e ".sandbox.failIfUnavailable == true" >/dev/null'

echo "my own lane" >"$JOBS/nightly-research/lanes/mine.md"
echo "# edited" >>"$JOBS/transcript-miner/job.env"
install
status=$?
check "second install succeeds" '[ "$status" -eq 0 ]'
check "second install keeps the user's lane" '[ -f "$JOBS/nightly-research/lanes/mine.md" ]'
check "second install keeps the user's job edits" 'tail -1 "$JOBS/transcript-miner/job.env" | grep -q "# edited"'

exit "$fail"
