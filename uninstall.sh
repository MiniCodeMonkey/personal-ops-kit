#!/bin/bash
# Remove the personal ops background service, notification app, skill link and
# jobctl link. Your jobs, settings and output are kept.

set -uo pipefail

ENGINE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG="$HOME/.config/personal-ops/config.env"
SKILL="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills/personal-ops"

launchctl bootout "gui/$(id -u)/dev.personal-ops.daemon" 2>/dev/null || true
rm -f "$HOME/Library/LaunchAgents/dev.personal-ops.daemon.plist"
rm -rf "$HOME/Applications/Personal Ops.app"

# Only remove links that point into this engine; anything else is not ours.
for link in "$SKILL" "$HOME/.local/bin/jobctl"; do
    case "$(readlink "$link" 2>/dev/null)" in
    "$ENGINE"/*) rm -f "$link" ;;
    esac
done

cat <<MSG

Personal ops is uninstalled. Kept, in case you want them:
  your jobs, settings and output: see $CONFIG
  the engine at $ENGINE
Delete those folders yourself to remove everything.
MSG
