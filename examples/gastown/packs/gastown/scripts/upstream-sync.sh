#!/usr/bin/env bash
# upstream-sync — merge upstream (gastownhall/gascity) into origin/main and rebuild.
#
# Expects the "upstream" remote to be configured in the rig repo.
# Runs as an exec order (no LLM, no agent, no wisp).
set -euo pipefail

CITY="${GC_CITY_ROOT:-.}"

# Find the gascity rig path.
RIG_PATH=$(gc rig list --json 2>/dev/null | jq -r '.[] | select(.name == "gascity") | .path' 2>/dev/null) || {
    echo "upstream-sync: failed to find gascity rig" >&2
    exit 1
}

if [ -z "$RIG_PATH" ] || [ ! -d "$RIG_PATH" ]; then
    echo "upstream-sync: gascity rig not found at '$RIG_PATH'" >&2
    exit 1
fi

cd "$RIG_PATH"

# Verify upstream remote exists.
if ! git remote get-url upstream &>/dev/null; then
    echo "upstream-sync: no 'upstream' remote configured in $RIG_PATH" >&2
    exit 1
fi

# Fetch upstream.
git fetch upstream

# Check if upstream has commits not yet in origin/main.
if git merge-base --is-ancestor upstream/main origin/main 2>/dev/null; then
    echo "upstream-sync: already up to date"
    exit 0
fi

# Merge upstream/main into a temporary local branch, then push.
# We avoid checking out main directly — it may be checked out by a worktree.
TEMP_BRANCH="gc/upstream-sync-$$"

cleanup() {
    # Best-effort cleanup of temp branch on failure.
    git checkout --detach 2>/dev/null || true
    git branch -D "$TEMP_BRANCH" 2>/dev/null || true
}
trap cleanup EXIT

git fetch origin main

git checkout -b "$TEMP_BRANCH" origin/main
git merge upstream/main --no-edit -m "chore: merge upstream gastownhall/gascity/main"

# Push merged result to origin/main.
git push origin "$TEMP_BRANCH":main

# Rebuild and install.
make install

echo "upstream-sync: merged upstream/main and rebuilt gc"
