#!/bin/sh
# Re-runnable import of odigos-enterprise main into odigos with full history.
# Usage: import.sh <enterprise-checkout> <odigos-checkout> <new-branch>
# Produces <new-branch> in the odigos checkout: upstream/main + a merge commit of the rewritten enterprise history.
set -eu

ENT="$1"
OSS="$2"
BRANCH="$3"
HERE="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
WORK="$TMP/work"

git -C "$ENT" fetch -q origin main
git -C "$OSS" fetch -q upstream main

git clone -q --no-local --no-checkout "$ENT" "$WORK"
git -C "$WORK" fetch -q "$ENT" +refs/remotes/origin/main:refs/heads/imp
git -C "$WORK" checkout -q imp
SRC_SHA="$(git -C "$WORK" rev-parse HEAD)"

git -C "$WORK" ls-tree --name-only HEAD > "$TMP/ent-top.txt"
git -C "$OSS" ls-tree --name-only upstream/main > "$TMP/oss-top.txt"
export ENT_TOP_FILE="$TMP/ent-top.txt" OSS_TOP_FILE="$TMP/oss-top.txt"

FILTER_BRANCH_SQUELCH_WARNING=1 git -C "$WORK" filter-branch -f --prune-empty \
  --index-filter "$HERE/map-index.sh" \
  --msg-filter 'sed -E "s/\(#([0-9]+)\)/(odigos-io\/odigos-enterprise#\1)/g"' \
  imp > "$TMP/filter.log" 2>&1

git -C "$OSS" checkout -q -B "$BRANCH" upstream/main
git -C "$OSS" fetch -q "$WORK" imp
git -C "$OSS" merge -q --allow-unrelated-histories --no-ff \
  -m "chore: merge odigos-enterprise into odigos with history" \
  -m "Imported from odigos-io/odigos-enterprise@$SRC_SHA." \
  -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>" \
  FETCH_HEAD

echo "imported odigos-io/odigos-enterprise@$SRC_SHA into $BRANCH"
