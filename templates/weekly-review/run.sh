#!/bin/bash
# Monday-morning memo: one model pass over the week's collected state (open
# loops, stale items, events, and the transcript miner's open suggestions),
# folded and digested by weeklyreview in plain code, then narrated.
# An empty digest ends the run at no-change with zero model calls.

set -uo pipefail

OPS_ROOT="${OPS_ROOT:?run me through jobctl}"
OPS_STATE_DIR="${OPS_STATE_DIR:?}"
OPS_ARTIFACT_DIR="${OPS_ARTIFACT_DIR:?}"
OPS_RESULT_FILE="${OPS_RESULT_FILE:?}"
MODEL="${OPS_MODEL:-sonnet}"
# Test seam: a test can inject a stub here so it never calls the real model.
CLAUDE="${WEEKLY_REVIEW_CLAUDE:-claude}"

JOBCTL="$OPS_ROOT/bin/jobctl"
WEEKLYREVIEW="$OPS_ROOT/bin/weeklyreview"
TODAY="$(date +%Y-%m-%d)"
OPEN="$OPS_STATE_DIR/open.json"
RECENT="$OPS_STATE_DIR/recent.json"
DIGEST="$OPS_STATE_DIR/digest.txt"
RAW="$OPS_STATE_DIR/raw.md"

mkdir -p "$OPS_ARTIFACT_DIR" "$OPS_STATE_DIR"

declare_result() {
    {
        echo "outcome=$1"
        echo "headline=$2"
        # Guards are [ -z ] || echo, never [ -n ] && echo: when the optional arg is
        # absent the && form leaves this block -- and so this function, and so
        # run.sh's implicit exit status -- at 1, which the runner reads as a failed
        # run even though the job declared ok.
        [ -z "${3:-}" ] || echo "artifact=$3"
        [ -z "${4:-}" ] || echo "detail=$4"
    } > "$OPS_RESULT_FILE"
}

# --- Cursor + run timestamp, captured before either fold ---------------------
SINCE=""
if [ -s "$OPS_STATE_DIR/last_compiled" ]; then
    SINCE="$(tr -d ' \t\n\r' < "$OPS_STATE_DIR/last_compiled")"
fi
RUN_START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# weeklyreview's -since flag always needs an RFC3339 header value, even on
# the first-ever run when there is no cursor to reuse yet.
if [ -n "$SINCE" ]; then
    HEADER_SINCE="$SINCE"
else
    HEADER_SINCE="$(date -u -v-7d +%Y-%m-%dT%H:%M:%SZ)"
fi

# --- Fold current state -------------------------------------------------------
"$JOBCTL" entries fold --open-only > "$OPEN"
if [ -n "$SINCE" ]; then
    "$JOBCTL" entries fold --since "$SINCE" > "$RECENT"
else
    "$JOBCTL" entries fold --since 7d > "$RECENT"
fi

# --- Digest --------------------------------------------------------------------
"$WEEKLYREVIEW" -open "$OPEN" -recent "$RECENT" -jobs "${OPS_JOBS:-$HOME/personal-ops}" -since "$HEADER_SINCE" > "$DIGEST"
DIGEST_STATUS=$?

if [ "$DIGEST_STATUS" -eq 3 ]; then
    declare_result no-change "A quiet week: nothing tracked or suggested"
    exit 0
elif [ "$DIGEST_STATUS" -ne 0 ]; then
    declare_result failed "Digest compilation failed" "" "weeklyreview exited $DIGEST_STATUS; see the run log."
    exit 1
fi

# --- One model pass ------------------------------------------------------------
# read -d '' rather than $(cat <<EOF ...): bash 3.2 (macOS's /bin/bash)
# mis-parses apostrophes inside a heredoc that feeds a command substitution.
read -r -d '' PROMPT_HEADER <<'PROMPT_EOF' || true
You are writing the user's Monday-morning weekly review from the digest below.
It composes everything their local jobs tracked this week. Address the reader as
"you" throughout; never the third person.

Sections, in order:
## The week -- 3-5 sentences weaving together what happened across your jobs
and notable closures. This cross-signal narrative is the whole point: connect
the signals, never just restate them.
## Open loops -- every open item from the digest, grouped sensibly, oldest and
most important first, each with its age. You may reorder; you may NOT drop any.
## Stale -- each stale item with the "still open?" question implied; suggest
closing what looks dead.
## Events -- the week's recorded events (for example sessions per project), lightly annotated.
## Suggestions -- the transcript miner's open suggestions verbatim by title,
with one line noting how to dismiss (the digest's data is authoritative).

Every number must come from the digest; invent nothing. An empty or thin week
gets an honest short memo, not padding. Output only the memo markdown, no
preamble, starting with "## The week".
PROMPT_EOF

PROMPT="$PROMPT_HEADER

--- DIGEST ---
$(cat "$DIGEST")"

if ! printf '%s' "$PROMPT" | caffeinate -s "$CLAUDE" -p --model "$MODEL" --output-format text --tools "" > "$RAW" 2>&1 || [ ! -s "$RAW" ]; then
    declare_result failed "The weekly review model call failed" "" "Digest was ready but the model call produced nothing; last_compiled was not advanced."
    exit 1
fi

# --- Strip accidental code fences into the memo ---------------------------------
sed '/^```/d' "$RAW" > "$OPS_ARTIFACT_DIR/$TODAY.md"

if [ ! -s "$OPS_ARTIFACT_DIR/$TODAY.md" ]; then
    rm -f "$OPS_ARTIFACT_DIR/$TODAY.md"
    declare_result failed "The weekly review memo was empty after cleanup" "" "Raw model output left in state for inspection."
    exit 1
fi

# --- Advance the cursor only now that a memo actually exists --------------------
printf '%s' "$RUN_START" > "$OPS_STATE_DIR/last_compiled"

HEADLINE=$(grep -v '^#' "$OPS_ARTIFACT_DIR/$TODAY.md" | grep -v '^[[:space:]]*$' | head -1 | cut -c1-110)
[ -z "$HEADLINE" ] && HEADLINE="Weekly review compiled"

COUNTS=$(grep -m1 '^open:' "$DIGEST" || true)

declare_result ok "$HEADLINE" "$TODAY.md" "$COUNTS"

# Success paths fall off the end here; exit explicitly so no trailing
# command's status can ever become the run's verdict.
exit 0
