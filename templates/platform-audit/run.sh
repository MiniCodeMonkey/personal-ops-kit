#!/bin/bash
# Monthly report on the platform from its own run ledgers: the platform
# watching itself. Pure code, no LLM.

set -uo pipefail

OPS_ROOT="${OPS_ROOT:?run me through jobctl}"
OPS_STATE_DIR="${OPS_STATE_DIR:?}"
OPS_ARTIFACT_DIR="${OPS_ARTIFACT_DIR:?}"
OPS_RESULT_FILE="${OPS_RESULT_FILE:?}"

BINARY="$OPS_ROOT/bin/platformaudit"
JOBCTL="$OPS_ROOT/bin/jobctl"
OPEN="$OPS_STATE_DIR/open.json"
CANDIDATES="$OPS_STATE_DIR/candidates.jsonl"
MEMO="$OPS_ARTIFACT_DIR/$(date -v-1d +%Y-%m).md"

mkdir -p "$OPS_STATE_DIR" "$OPS_ARTIFACT_DIR"

declare_result() {
    {
        echo "outcome=$1"
        echo "headline=$2"
        # Guards are [ -z ] || echo, never [ -n ] && echo: when the optional arg is
        # absent the && form leaves this block -- and so this function, and so
        # run.sh's implicit exit status -- at 1, which the runner reads as a failed
        # run even though the job declared ok.
        [ -z "${3:-}" ] || echo "detail=$3"
    } > "$OPS_RESULT_FILE"
}

"$JOBCTL" entries fold --job platform-audit --open-only > "$OPEN"

if ! "$BINARY" -ops "${OPS_ARTIFACTS:-$HOME/ops}" -open "$OPEN" -out "$MEMO" > "$CANDIDATES"; then
    declare_result failed "Platform audit failed" "platformaudit exited non-zero; no memo written. See the run log."
    exit 1
fi

ADDED=0 DROPPED=0
while IFS= read -r c; do
    kind=$(jq -r .kind <<<"$c"); id=$(jq -r .id <<<"$c")
    status=$(jq -r .status <<<"$c"); title=$(jq -r .title <<<"$c")
    data=$(jq -c '.data // empty' <<<"$c")
    args=(--kind "$kind" --id "$id" --status "$status" --title "$title")
    [ -n "$data" ] && args+=(--data "$data")
    if "$JOBCTL" entries append "${args[@]}" --dry-run >/dev/null 2>&1 \
       && "$JOBCTL" entries append "${args[@]}"; then
        ADDED=$((ADDED + 1))
    else
        echo "$(date '+%H:%M:%S')  dropped invalid candidate: $id"
        DROPPED=$((DROPPED + 1))
    fi
done < "$CANDIDATES"

OPENS=$(grep -c '"status":"open"' "$CANDIDATES" || true)
CLOSES=$(grep -c '"status":"closed"' "$CANDIDATES" || true)
TITLE=$(jq -r 'select(.kind=="platform-audit") | .title' "$CANDIDATES" | head -1)
[ -z "$TITLE" ] && TITLE="Platform audit compiled"

if [ "$DROPPED" -gt 0 ]; then
    declare_result warning "$DROPPED candidate(s) failed validation" "$ADDED appended ($OPENS flag(s) opened, $CLOSES closed)."
elif jq -e 'select(.kind=="platform-flag" and .status=="open")' "$CANDIDATES" >/dev/null; then
    declare_result ok "$TITLE" "$OPENS flag(s) opened, $CLOSES closed. Memo: $MEMO"
else
    declare_result no-change "$TITLE" "$OPENS flag(s) opened, $CLOSES closed. Memo: $MEMO"
fi

# Success paths fall off the end here; exit explicitly so no trailing
# command's status can ever become the run's verdict.
exit 0
