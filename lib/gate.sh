#!/usr/bin/env bash
# Decides whether a gated job should run now: first whether the machine can
# physically finish it, then whether the week's Claude quota can afford it.
#
# Exit 0 = go, exit 10 = skip (with a reason on stdout), exit 1 = broken setup.
#
# Only the 7-day window is checked. The 5-hour window is rolling, so an evening
# run's window expires around midnight and cannot compete with the next
# morning's work. The weekly cap is the only limit a scheduled job can actually
# steal from tomorrow.
#
# The reading comes from the profile the job spends: $CLAUDE_CONFIG_DIR when a
# job pins one, otherwise the default profile. Two sources, in order:
#
#   1. .claude.json's cachedUsageUtilization. Claude Code writes this itself
#      with a fetchedAtMs stamp, so it is scoped to one account and its age is
#      a real measurement rather than a guess.
#   2. usage-snapshot.json, if a status line command writes one. Optional.
#
# Tunables (environment): WEEKLY_THRESHOLD (default 60), MAX_SNAPSHOT_AGE
# (seconds, default 18h), REQUIRE_AC=0 to skip the power check.

set -uo pipefail

# The default profile keeps .claude.json in $HOME; a pinned profile keeps it
# inside its own directory.
PROFILE="${CLAUDE_PROFILE_DIR:-${CLAUDE_CONFIG_DIR:-$HOME/.claude}}"
if [ -n "${CLAUDE_PROFILE_DIR:-}${CLAUDE_CONFIG_DIR:-}" ]; then
    DEFAULT_CONFIG_JSON="$PROFILE/.claude.json"
else
    DEFAULT_CONFIG_JSON="$HOME/.claude.json"
fi
CONFIG_JSON="${CLAUDE_CONFIG_JSON:-$DEFAULT_CONFIG_JSON}"
SNAPSHOT="${USAGE_SNAPSHOT:-$PROFILE/usage-snapshot.json}"
# Skip the run when the week is already this far consumed. Raise it if gated jobs
# skip nights you wanted; lower it if they eat into your working week.
THRESHOLD="${WEEKLY_THRESHOLD:-60}"
# A reading older than this tells us too little to trust.
MAX_AGE_SECONDS="${MAX_SNAPSHOT_AGE:-64800}" # 18 hours

# Power comes before quota because a run the machine sleeps through cannot
# finish, however much quota is left. But battery alone is not the killer: run.sh's caffeinate -si holds off
# idle sleep on either power source. What caffeinate cannot survive is the lid
# closing (clamshell sleep is unconditional without an external display), and a
# battery run on an unattended machine is one lid-close away from that plus a
# pointless drain. So on battery we only skip when the machine looks
# unattended: lid closed, or no input for IDLE_LIMIT seconds. An open laptop
# with someone at it runs. Checked here rather than in run.sh so the runner
# records the night as gated instead of failed.
#
# REQUIRE_AC=0 disables the check, for testing.
IDLE_LIMIT="${BATTERY_IDLE_LIMIT:-300}"
if [ "${REQUIRE_AC:-1}" = "1" ]; then
    if ! pmset -g ps 2>/dev/null | grep -q "'AC Power'"; then
        if ioreg -r -k AppleClamshellState -d 4 2>/dev/null \
            | grep -q '"AppleClamshellState" = Yes'; then
            echo "skip: on battery with the lid closed"
            exit 10
        fi
        # HIDIdleTime is nanoseconds since the last keyboard/mouse input. If we
        # cannot read it we cannot claim anyone is at the machine, so skip.
        idle=$(ioreg -c IOHIDSystem 2>/dev/null \
            | awk -F' = ' '/"HIDIdleTime"/ {print int($2 / 1000000000); exit}')
        if [ -z "$idle" ]; then
            echo "skip: on battery and unable to read idle time"
            exit 10
        fi
        if [ "$idle" -ge "$IDLE_LIMIT" ]; then
            echo "skip: on battery and idle for ${idle}s (limit ${IDLE_LIMIT}s)"
            exit 10
        fi
    fi
fi

if ! command -v jq >/dev/null 2>&1; then
    echo "gate: jq not found on PATH" >&2
    exit 1
fi

now=$(date +%s)

# Each reader prints "<recorded_at> <used_percentage> <resets_at_epoch>" on one
# line, or nothing when this source has no usable reading. resets_at is 0 when
# the source does not carry one.

read_config() {
    [ -f "$CONFIG_JSON" ] || return 0
    jq -r '
        (.cachedUsageUtilization // empty) as $c
        | ($c.utilization.seven_day // empty) as $w
        | select($c.fetchedAtMs != null and $w.utilization != null)
        | [ ($c.fetchedAtMs / 1000 | floor),
            $w.utilization,
            ( $w.resets_at
              | if . == null then 0
                else ( try ( sub("\\.[0-9]+"; "")
                           | sub("\\+00:00$"; "Z")
                           | fromdateiso8601 ) catch 0 )
                end )
          ] | @tsv
    ' "$CONFIG_JSON" 2>/dev/null
}

read_snapshot() {
    [ -f "$SNAPSHOT" ] || return 0
    # A payload carrying seven_day but no five_hour comes from a session whose
    # rate-limit state is partial, which in practice means stale -- that is the
    # shape the 60% ghost writer had. Require both windows before trusting it.
    jq -r '
        (.seven_day // empty) as $w
        | select(.recorded_at != null and .five_hour != null
                 and $w.used_percentage != null)
        | [ .recorded_at, $w.used_percentage, ($w.resets_at // 0) ] | @tsv
    ' "$SNAPSHOT" 2>/dev/null
}

reading=$(read_config)
source="cachedUsageUtilization"
if [ -z "$reading" ]; then
    reading=$(read_snapshot)
    source="status-line snapshot"
fi

if [ -z "$reading" ]; then
    echo "skip: no usable 7-day reading in $CONFIG_JSON or $SNAPSHOT (has a session run in this profile yet?)"
    exit 10
fi

recorded_at=$(printf '%s' "$reading" | cut -f1)
weekly_used=$(printf '%s' "$reading" | cut -f2)
weekly_resets=$(printf '%s' "$reading" | cut -f3)

age=$((now - recorded_at))

# A reset that has already passed means the recorded percentage describes a
# window that no longer exists. We are at the start of a fresh week, so the
# reading cannot be evidence against running.
if [ "$weekly_resets" -gt 0 ] && [ "$weekly_resets" -lt "$now" ] 2>/dev/null; then
    echo "go: 7-day window reset at $(date -r "$weekly_resets" '+%Y-%m-%d %H:%M') -- fresh week"
    exit 0
fi

if [ "$age" -gt "$MAX_AGE_SECONDS" ]; then
    printf 'skip: %s is %dh old (limit %dh) -- refusing to guess at remaining quota\n' \
        "$source" "$((age / 3600))" "$((MAX_AGE_SECONDS / 3600))"
    exit 10
fi

# Integer compare; used_percentage is a float.
weekly_int=$(printf '%.0f' "$weekly_used" 2>/dev/null || echo 100)

if [ "$weekly_int" -ge "$THRESHOLD" ]; then
    printf 'skip: 7-day usage at %s%% (threshold %s%%, %s %dm old)\n' \
        "$weekly_int" "$THRESHOLD" "$source" "$((age / 60))"
    exit 10
fi

printf 'go: 7-day usage at %s%% (threshold %s%%), %s %dm old\n' \
    "$weekly_int" "$THRESHOLD" "$source" "$((age / 60))"
exit 0
