#!/bin/bash
# Mine the week's Claude transcripts. Three phases:
#   verify  -- close open suggestions whose artifact now exists (pure code)
#   distill -- transcripts -> bounded digest + per-project activity (pure code)
#   judge   -- ONE model pass over the digest -> entries + weekly memo
# An empty digest ends the run at no-change with zero model calls.

set -uo pipefail

OPS_ROOT="${OPS_ROOT:?run me through jobctl}"
OPS_STATE_DIR="${OPS_STATE_DIR:?}"
OPS_ARTIFACT_DIR="${OPS_ARTIFACT_DIR:?}"
OPS_RESULT_FILE="${OPS_RESULT_FILE:?}"
MODEL="${OPS_MODEL:-sonnet}"

MINER="$OPS_ROOT/bin/transcriptminer"
JOBCTL="$OPS_ROOT/bin/jobctl"
CLAUDE="claude"
TODAY="$(date +%Y-%m-%d)"
WEEK="$(date +%G-W%V)"
MEMO="$OPS_ARTIFACT_DIR/$TODAY.md"
DIGEST="$OPS_STATE_DIR/digest.txt"
ACTIVITY="$OPS_STATE_DIR/activity.json"
RAW="$OPS_STATE_DIR/model-output.json"

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

# --- Phase 1: close adopted suggestions -------------------------------------
CLOSED=0
while IFS= read -r line; do
    kind=$(jq -r .kind <<<"$line"); id=$(jq -r .id <<<"$line")
    if "$JOBCTL" entries append --kind "$kind" --id "$id" --status closed \
        --title "adopted" --data '{"reason":"adopted"}'; then
        CLOSED=$((CLOSED + 1))
    fi
done < <("$JOBCTL" entries fold --job transcript-miner --open-only | "$MINER" verify -home "$HOME")
echo "$(date '+%H:%M:%S')  verify: $CLOSED suggestion(s) adopted"

# Snapshot the cursor before distill advances it, so a judge-phase failure
# below can roll it back and let next week's distill re-scan these sessions
# instead of permanently losing them. No-op (harmlessly) on the first-ever run.
cp "$OPS_STATE_DIR/last_mined" "$OPS_STATE_DIR/last_mined.prev" 2>/dev/null || true

# --- Phase 2: distill --------------------------------------------------------
if ! "$MINER" distill -home "$HOME" -state "$OPS_STATE_DIR" -out "$DIGEST" -activity "$ACTIVITY"; then
    declare_result failed "Transcript distillation failed" "" "distill exited non-zero; the cursor was not advanced past unread sessions."
    exit 1
fi

# Session-activity events go in whether or not the digest has minable text --
# a week of sessions whose turns were all filtered is still activity.
EVENTS=0
while IFS=$'\t' read -r project sessions turns; do
    if "$JOBCTL" entries append --kind session-activity \
        --id "session-activity#$project#$WEEK" --status event \
        --title "$project: $sessions session(s), $turns turn(s) this week" \
        --data "{\"sessions\":$sessions,\"turns\":$turns}"; then
        EVENTS=$((EVENTS + 1))
    fi
done < <(jq -r 'to_entries[] | [.key, (.value.sessions|tostring), (.value.turns|tostring)] | @tsv' "$ACTIVITY")
echo "$(date '+%H:%M:%S')  distill: $EVENTS project(s) active"

if [ ! -s "$DIGEST" ]; then
    rm -f "$OPS_STATE_DIR/last_mined.prev"
    declare_result no-change "A quiet week in the transcripts" "" \
        "$EVENTS project(s) had sessions but no minable turns survived the filters; $CLOSED adopted."
    exit 0
fi
head -1 "$DIGEST"

# --- Phase 3: one model pass -------------------------------------------------
PRIOR=$("$JOBCTL" entries fold --job transcript-miner \
        | jq -c '[.items[] | select(.kind != "session-activity") | {kind, id, status, title}]')

# Belt-and-suspenders against the model disobeying "never emit a closed id":
# a set of ids already decided (adopted or dismissed), checked exactly
# (no substring matching) before any candidate is appended.
CLOSED_IDS=$(jq -r '[.[] | select(.status=="closed") | .id] | join("\n")' <<<"$PRIOR")

PROMPT="You are mining one week of the user's Claude Code transcripts for durable
configuration they should adopt. The digest below contains only human-typed turns,
deduplicated, tagged [project session-date], with repeat counts precomputed (×N).

Find exactly two signal types:
- repeated-correction: an instruction or correction given in 2 or more independent
  sessions (the ×N counts and repeated similar lines are your evidence) that should
  become a persistent memory or CLAUDE.md rule.
- workflow-skill-candidate: a multi-step procedure the user walked Claude through in
  2 or more sessions that should become a custom skill.

Rules, all binding:
- Only subjects with at least 2 independent occurrences in different sessions.
- PRIOR ENTRIES below lists every subject already tracked, with status. If a subject
  matches an existing id, reuse that id exactly. NEVER emit a subject whose id is
  status closed -- closed means adopted or dismissed, either way decided.
- At most 10 new suggestions, ranked by repetition count, strongest first.
- Each entry: kind, id (kind + '#' + short-kebab-slug), title (max 140 chars, citing
  the count and one concrete example), data {count, projects, example}.

Output STRICT JSON, no markdown fences, no prose, exactly this shape:
{\"entries\":[{\"kind\":\"...\",\"id\":\"...\",\"title\":\"...\",\"data\":{\"count\":2,\"projects\":[\"p\"],\"example\":\"...\"}}],
 \"memo\":\"<markdown: one section per suggestion with the evidence quotes; honest and short>\"}
An empty entries array with a one-line memo is a valid and welcome answer.

--- PRIOR ENTRIES (JSON) ---
$PRIOR

--- DIGEST ---
$(cat "$DIGEST")"

# caffeinate -s: hold off system sleep for exactly this call (AC power only).
if ! printf '%s' "$PROMPT" | caffeinate -s "$CLAUDE" -p --model "$MODEL" --output-format text --tools "" > "$RAW" 2>&1 || [ ! -s "$RAW" ]; then
    [ -f "$OPS_STATE_DIR/last_mined.prev" ] && mv "$OPS_STATE_DIR/last_mined.prev" "$OPS_STATE_DIR/last_mined"
    declare_result failed "The mining pass failed" "" "Digest was ready but the model call produced nothing; nothing was appended."
    exit 1
fi

# Strip accidental code fences, then validate the shape before trusting it.
CLEANED=$(sed -e 's/^```json$//' -e 's/^```$//' "$RAW")
if ! jq -e '(.entries | type == "array") and (.memo | type == "string")' <<<"$CLEANED" >/dev/null 2>&1; then
    [ -f "$OPS_STATE_DIR/last_mined.prev" ] && mv "$OPS_STATE_DIR/last_mined.prev" "$OPS_STATE_DIR/last_mined"
    declare_result failed "Model output was not the expected JSON" "$TODAY-raw.txt" "Raw output kept for inspection."
    cp "$RAW" "$OPS_ARTIFACT_DIR/$TODAY-raw.txt"
    exit 1
fi

ADDED=0 DROPPED=0
while IFS= read -r entry; do
    kind=$(jq -r .kind <<<"$entry"); id=$(jq -r .id <<<"$entry")
    title=$(jq -r .title <<<"$entry"); data=$(jq -c .data <<<"$entry")
    if grep -Fxq "$id" <<<"$CLOSED_IDS"; then
        echo "$(date '+%H:%M:%S')  dropped closed-id candidate: $id"
        DROPPED=$((DROPPED + 1))
        continue
    fi
    if "$JOBCTL" entries append --kind "$kind" --id "$id" --status open \
        --title "$title" --data "$data" --dry-run >/dev/null 2>&1; then
        "$JOBCTL" entries append --kind "$kind" --id "$id" --status open \
            --title "$title" --data "$data"
        ADDED=$((ADDED + 1))
    else
        echo "$(date '+%H:%M:%S')  dropped invalid candidate: $id"
        DROPPED=$((DROPPED + 1))
    fi
done < <(jq -c '.entries[]' <<<"$CLEANED")

{
    jq -r .memo <<<"$CLEANED"
    echo
    echo "---"
    echo "To dismiss a suggestion so it is never raised again:"
    echo '`jobctl entries append --job transcript-miner --kind <kind> --id <id> --status closed --title "dismissed" --data '"'"'{"reason":"dismissed"}'"'"'`'
} > "$MEMO"

echo "$(date '+%H:%M:%S')  judge: $ADDED suggestion(s), $DROPPED dropped, $CLOSED adopted"

# Judge phase succeeded (however it comes out) -- the cursor advance stands.
rm -f "$OPS_STATE_DIR/last_mined.prev"

if [ "$ADDED" -eq 0 ] && [ "$DROPPED" -eq 0 ] && [ "$CLOSED" -eq 0 ]; then
    declare_result no-change "Nothing new worth adopting this week" "$TODAY.md" "Digest mined; no repeated signals found."
elif [ "$DROPPED" -gt 0 ]; then
    declare_result warning "$ADDED suggestion(s), $DROPPED candidate(s) failed validation" "$TODAY.md" "Dropped candidates are logged above."
else
    TOP=$(jq -r '.entries[0].title // empty' <<<"$CLEANED" | cut -c1-110)
    [ -z "$TOP" ] && TOP="$ADDED suggestion(s) this week ($CLOSED adopted)"
    declare_result ok "$TOP" "$TODAY.md" "$ADDED suggestion(s) appended, $CLOSED adopted."
fi

# Success paths fall off the end here; exit explicitly so no trailing
# command's status can ever become the run's verdict.
exit 0
