#!/usr/bin/env bash
# Tests for lib/gate.sh. Run directly or via `make test`.
#
# The case that matters most is "ghost writer ignored": a stale session that
# rewrites the snapshot with a pre-reset reading and no five_hour must not win.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GATE="$HERE/gate.sh"

pass=0
fail=0

# run <name> <expected_exit> <expected_substring> -- runs the gate against the
# fixtures currently in $WORK.
run() {
    local name="$1" want_exit="$2" want_text="$3" out status
    out=$(REQUIRE_AC=0 \
          CLAUDE_PROFILE_DIR="$WORK" \
          CLAUDE_CONFIG_JSON="$WORK/.claude.json" \
          USAGE_SNAPSHOT="$WORK/usage-snapshot.json" \
          bash "$GATE" 2>&1)
    status=$?
    if [ "$status" -ne "$want_exit" ]; then
        printf 'FAIL %s: exit %d, want %d (%s)\n' "$name" "$status" "$want_exit" "$out"
        fail=$((fail + 1))
        return
    fi
    case "$out" in
        *"$want_text"*) ;;
        *)
            printf 'FAIL %s: output %q does not contain %q\n' "$name" "$out" "$want_text"
            fail=$((fail + 1))
            return
            ;;
    esac
    printf 'ok   %s\n' "$name"
    pass=$((pass + 1))
}

# Each case gets its own fixture directory; they are all removed on exit.
ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
case_n=0
setup() {
    case_n=$((case_n + 1))
    WORK="$ROOT/case$case_n"
    mkdir -p "$WORK"
}

# write_config <utilization> <resets_at_iso_or_null> <fetched_epoch>
write_config() {
    local util="$1" resets="$2" fetched="$3" resets_json
    if [ "$resets" = "null" ]; then resets_json=null; else resets_json="\"$resets\""; fi
    cat > "$WORK/.claude.json" <<JSON
{"cachedUsageUtilization": {
   "fetchedAtMs": ${fetched}000,
   "accountUuid": "test",
   "utilization": {"seven_day": {"utilization": $util, "resets_at": $resets_json}}}}
JSON
}

# write_snapshot <used> <resets_epoch> <recorded_epoch> <five_hour_json>
write_snapshot() {
    cat > "$WORK/usage-snapshot.json" <<JSON
{"recorded_at": $3, "version": "test",
 "five_hour": $4,
 "seven_day": {"used_percentage": $1, "resets_at": $2}}
JSON
}

now=$(date +%s)
future=$((now + 200000))
past=$((now - 3600))
iso_future=$(date -u -r "$future" '+%Y-%m-%dT%H:%M:%S.000000+00:00')
iso_past=$(date -u -r "$past" '+%Y-%m-%dT%H:%M:%S.000000+00:00')
full_five='{"used_percentage": 10, "resets_at": 1}'

setup
write_config 2 "$iso_future" "$now"
run "fresh reading under threshold runs" 0 "go: 7-day usage at 2%"

setup
write_config 61 "$iso_future" "$now"
run "over threshold skips" 10 "skip: 7-day usage at 61%"

setup
write_config 60 "$iso_future" "$now"
run "at threshold skips" 10 "skip: 7-day usage at 60%"

# The regression. A live 2% in the profile's own cache, and the ghost writer's
# partial pre-reset 60% in the shared snapshot. The night must run.
setup
write_config 2 "$iso_future" "$now"
write_snapshot 60 "$future" "$now" null
run "ghost writer ignored when cache is present" 0 "go: 7-day usage at 2%"

# Same ghost, no cache to fall back on. A partial payload is not evidence.
setup
write_snapshot 60 "$future" "$now" null
run "partial snapshot alone is not a reading" 10 "no usable 7-day reading"

# A complete snapshot still works when the cache is missing.
setup
write_snapshot 3 "$future" "$now" "$full_five"
run "complete snapshot is a usable fallback" 0 "status-line snapshot"

setup
write_config 95 "$iso_past" "$now"
run "window already rolled over runs" 0 "fresh week"

setup
write_config 2 "$iso_future" "$((now - 90000))"
run "reading older than the limit skips" 10 "refusing to guess"

setup
write_config 2 null "$now"
run "missing resets_at still reads the percentage" 0 "go: 7-day usage at 2%"

setup
run "no sources at all skips" 10 "no usable 7-day reading"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
