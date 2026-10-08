#!/bin/bash
# Nightly self-directed research, inside the OS sandbox.
#
# The runner has already checked the quota gate (JOB_GATED in job.env) and will
# kill the run after JOB_TIMEOUT. This script picks tonight's lane in plain code,
# hands a self-contained brief to a headless Claude Code session, and checks the
# memo it leaves.
#
# Usage (normally started by jobctl):
#   jobctl run nightly-research               a normal run
#   jobctl run nightly-research --force       skip the quota gate
#   bash run.sh --dry-run                     print the prompt and settings, run nothing
#   bash run.sh --lane=ai-tooling             override tonight's lane
#
# Must parse under bash 3.2, the only bash macOS ships: no $'\t' inside a
# parameter expansion, and no here-document nested inside $(...).

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OPS_ROOT="${OPS_ROOT:-$HOME/.personal-ops-kit}"
OUT_DIR="${OPS_ARTIFACT_DIR:-$HOME/ops/nightly-research}"
RESULT_FILE="${OPS_RESULT_FILE:-}"
MODEL="${OPS_MODEL:-opus}"
LEDGER="$OUT_DIR/ledger.jsonl"
WORK_DIR="$OUT_DIR/scratch"
LANEPICK="$OPS_ROOT/bin/lanepick"

# Strip Anthropic and cloud credentials from every subprocess the session starts,
# and pin filesystem isolation on regardless of any other settings source.
export CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1
# No agent socket, so there is no forwarded ssh key to find.
unset SSH_AUTH_SOCK

DRY_RUN=0
LANE_OVERRIDE=""
for arg in "$@"; do
    case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --lane=*) LANE_OVERRIDE="${arg#--lane=}" ;;
    *)
        echo "unknown argument: $arg" >&2
        exit 64
        ;;
    esac
done

log() { printf '[%s] %s\n' "$(date '+%H:%M:%S')" "$*"; }

# ------------------------------------------------------------------------- lane

if [ -n "$LANE_OVERRIDE" ]; then
    selection="$("$LANEPICK" --lanes="$HERE/lanes" --ledger="$LEDGER" --slug="$LANE_OVERRIDE")"
else
    selection="$("$LANEPICK" --lanes="$HERE/lanes" --ledger="$LEDGER")"
fi
if [ -z "$selection" ]; then
    log "lane selection produced nothing -- not running"
    exit 1
fi

LANE_SLUG="$(printf '%s\n' "$selection" | head -1)"
LANE_BRIEF="$(printf '%s\n' "$selection" | tail -n +2)"
TODAY="$(date '+%Y-%m-%d')"
MEMO="$OUT_DIR/$TODAY-$LANE_SLUG.html"

log "lane: $LANE_SLUG"
log "memo: $MEMO"
log "model: $MODEL"

RECENT="(none yet -- this is the first run)"
if [ -s "$LEDGER" ]; then
    RECENT="$(grep '"lane"' "$LEDGER" | tail -n 25)"
fi
NOW_HUMAN="$(date '+%Y-%m-%d %H:%M %Z')"

# ------------------------------------------------------ scratch, prompt, settings

PROMPT_FILE="$(mktemp "${TMPDIR:-/tmp}/nightly-research-prompt.XXXXXX")"
SETTINGS_FILE="$(mktemp "${TMPDIR:-/tmp}/nightly-research-settings.XXXXXX")"
trap 'rm -f "$PROMPT_FILE" "$SETTINGS_FILE"' EXIT

PROFILE="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
ALLOW_WRITE="\"$WORK_DIR\", \"$OUT_DIR\""
[ -z "$RESULT_FILE" ] || ALLOW_WRITE="$ALLOW_WRITE, \"$RESULT_FILE\""

# The safety layer. The boundary is the operating system (Seatbelt), not a list
# of blessed shell commands: it applies to every sandboxed command and all its
# children, which is why Bash can be allowed at all.
#
#   failIfUnavailable        default is to warn and run UNSANDBOXED; must be true.
#   allowUnsandboxedCommands default lets unsandboxable commands fall back to the
#                            permission flow; must be false.
#   credentials.files        there is no built-in credential deny list; only the
#                            paths named here are protected.
#   filesystem.allowWrite    writes are confined to the working directory plus
#                            these paths, which is why the run works from scratch.
#
# allowedDomains ["*"] leaves web egress open for research. Add deniedDomains to
# block hosts outright.
cat >"$SETTINGS_FILE" <<SETTINGS_END
{
  "permissions": { "defaultMode": "default" },
  "sandbox": {
    "enabled": true,
    "failIfUnavailable": true,
    "allowUnsandboxedCommands": false,
    "autoAllowBashIfSandboxed": true,
    "network": { "allowedDomains": ["*"] },
    "filesystem": {
      "allowWrite": [$ALLOW_WRITE]
    },
    "credentials": {
      "files": [
        { "path": "~/.ssh", "mode": "deny" },
        { "path": "~/.aws", "mode": "deny" },
        { "path": "~/.kube", "mode": "deny" },
        { "path": "~/.config/gcloud", "mode": "deny" },
        { "path": "~/.config/gh", "mode": "deny" },
        { "path": "~/.config/op", "mode": "deny" },
        { "path": "~/.docker", "mode": "deny" },
        { "path": "~/.netrc", "mode": "deny" },
        { "path": "~/.claude.json", "mode": "deny" },
        { "path": "$PROFILE/.credentials.json", "mode": "deny" },
        { "path": "$PROFILE/.claude.json", "mode": "deny" }
      ],
      "envVars": [
        { "name": "SSH_AUTH_SOCK", "mode": "deny" },
        { "name": "ANTHROPIC_API_KEY", "mode": "deny" },
        { "name": "AWS_ACCESS_KEY_ID", "mode": "deny" },
        { "name": "AWS_SECRET_ACCESS_KEY", "mode": "deny" },
        { "name": "AWS_SESSION_TOKEN", "mode": "deny" },
        { "name": "GITHUB_TOKEN", "mode": "deny" },
        { "name": "GH_TOKEN", "mode": "deny" }
      ]
    }
  }
}
SETTINGS_END

cat >"$PROMPT_FILE" <<PROMPT_END
You are running unattended, from a scheduled job, at $NOW_HUMAN. Nobody is
watching and nobody can answer a question. Every decision is yours. Finish the
work and produce the deliverable without stopping to check in.

## Tonight's lane: $LANE_SLUG

$LANE_BRIEF

## Your working environment

You have a full shell inside an operating system sandbox. Use it: compute things
rather than estimating them, parse data, run the numbers in python. A memo with a
real calculation in it beats one with a plausible guess.

- Your working directory is $WORK_DIR. Write scratch files there freely.
- Network access works for research. Credential files and credential environment
  variables are unreadable, and ssh cannot leave the sandbox; spend the evening on
  the research itself.

## Your job, in order

1. Pick exactly ONE concrete question inside tonight's lane, narrow enough to
   answer tonight and specific enough that the answer changes what the reader
   would do. Note what surfaced it (a page, a changelog, a dataset, or the lane
   brief alone) as you pick it.

   Choose a fresh topic. These are recent memos; judge repetition on the topic,
   not the lane name:

$RECENT

2. Research it properly. You may dispatch at most 3 subagents in parallel for
   independent strands; pass model sonnet to them. Use WebSearch and WebFetch for
   sources and the shell for anything you can compute. Prefer one verified fact
   over three plausible ones, and say which is which.

3. Write the memo as one self-contained HTML page at exactly this path:
   $MEMO

   Start from the template at $WORK_DIR/template.html: read it, copy it, fill the
   marked regions, and keep its stylesheet. The provenance block comes first and
   says plainly where the question came from; mark a question you chose yourself
   with <span class="self-directed">Self-directed</span>.

   The page opens offline from disk: inline everything, with no external fonts,
   stylesheets, scripts or images. Figures are hand-written inline SVG using the
   template's .svg-* and .s1 to .s5 classes, and chart only numbers you actually
   obtained. Verify every tag you opened is closed.

   House style: double hyphens (--) where you would use a dash.

4. Write your verdict to ${RESULT_FILE:-the result file} as KEY=value lines and
   nothing else:

   outcome=ok
   headline=<under 110 characters: the finding itself>
   detail=<one sentence of context>

   Use outcome=warning if the memo has a defect worth flagging. The headline is
   what the notification shows.

Files you write belong under $OUT_DIR. The memo at $MEMO is the deliverable;
finish with it written.
PROMPT_END

if [ "$DRY_RUN" -eq 1 ]; then
    log "dry run -- prompt follows"
    cat "$PROMPT_FILE"
    echo "--- settings ---"
    cat "$SETTINGS_FILE"
    exit 0
fi

# Start each night from an empty scratch directory, and refuse to clear any other
# path.
case "$WORK_DIR" in
"$OUT_DIR"/scratch) rm -rf "$WORK_DIR" ;;
*)
    echo "refusing to clear unexpected scratch path: $WORK_DIR" >&2
    exit 1
    ;;
esac
mkdir -p "$WORK_DIR" || exit 1
cp "$HERE/template.html" "$WORK_DIR/template.html" || exit 1

# Bash is allowed outright because the sandbox, not this list, is the boundary.
TOOLS="Bash,Read,Glob,Grep,WebSearch,WebFetch,Write,Edit,TodoWrite,Task,Agent"

log "starting claude (sandboxed, holding wake)"
start=$(date +%s)
cd "$WORK_DIR" || exit 1

# caffeinate -si holds off idle and system sleep for exactly this command, without
# keeping the display on or deferring the screen lock.
caffeinate -si claude \
    -p "$(cat "$PROMPT_FILE")" \
    --model "$MODEL" \
    --output-format text \
    --tools "$TOOLS" \
    --allowedTools ${TOOLS//,/ } \
    --settings "$SETTINGS_FILE" \
    </dev/null
claude_status=$?
log "claude exited $claude_status after $(($(date +%s) - start))s"

if [ ! -s "$MEMO" ]; then
    log "NO MEMO PRODUCED -- see log above"
    exit 1
fi
if ! grep -qi '</html>' "$MEMO"; then
    log "memo has no closing </html> -- the page is probably truncated"
    exit 1
fi

bytes="$(wc -c <"$MEMO" | tr -d ' ')"
figures="$(grep -c -i '<svg' "$MEMO" | tr -d ' ')"
external="$(grep -o -E '(src|href)="https?://[^"]*"' "$MEMO" | grep -c -v -E 'w3\.org' | tr -d ' ')"
log "memo written ($bytes bytes, $figures figures, $external external references)"

if [ -n "$RESULT_FILE" ]; then
    if [ ! -s "$RESULT_FILE" ]; then
        log "no verdict written -- filling one in from the memo"
        {
            if [ "$external" -gt 0 ]; then
                echo "outcome=warning"
                echo "detail=The memo references $external external resource(s), so it is not self-contained."
            else
                echo "outcome=ok"
            fi
            echo "headline=$LANE_SLUG memo ($bytes bytes, $figures figures)"
        } >"$RESULT_FILE"
    fi
    # lanepick scores the next night from the lane in the ledger, and the dashboard
    # finds the memo from the artifact, so both are set here where they are known.
    grep -q '^lane=' "$RESULT_FILE" || echo "lane=$LANE_SLUG" >>"$RESULT_FILE"
    grep -q '^artifact=' "$RESULT_FILE" || echo "artifact=$(basename "$MEMO")" >>"$RESULT_FILE"
fi

exit 0
