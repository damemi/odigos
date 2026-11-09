#!/bin/sh
# git filter-branch --index-filter: rewrites every path of an odigos-enterprise commit to its place in odigos.
# Rules are applied top to bottom; a path is dropped when it maps to nothing.
git ls-files -s | awk -F '\t' -v ent_top="$ENT_TOP_FILE" -v oss_top="$OSS_TOP_FILE" '
BEGIN {
  while ((getline l < ent_top) > 0) ent[l] = 1
  while ((getline l < oss_top) > 0) oss[l] = 1
}
{
  p = $2
  n = split(p, parts, "/")
  top = parts[1]
  # Already moved to odigos by earlier PRs (mcp, odigosauth, common-enterprise, distros, on-prem token tooling).
  if (p ~ /^(mcp|odigosauth|common-enterprise|distros|onprem-token-generator|onprem-token-reminders)\//) next
  # Central and the cloud connectors stay in odigos-enterprise.
  else if (p ~ /^(central-backend|central-proxy|central-ui|centralized-[^\/]+|connectors|connector-[^\/]+)\//) next
  else if (p ~ /^tests\/e2e\/central-[^\/]+\//) next
  # Old enterprise copies of odigos paths, deleted from enterprise since: dropping them keeps them out of odigos path history.
  else if (!(top in ent) && (top in oss)) next
  else if (n == 1 && !(top in ent)) next
  # The protected docs site must stay outside docs/, which the public Mintlify site publishes in full.
  else if (p ~ /^docs\//) p = "enterprise-docs/" substr(p, 6)
  # Folders that clash with odigos folders of the same name keep their inner layout under <dir>/enterprise/.
  else if (p ~ /^(odiglet|instrumentor|collector|tests|scripts)\//) { split(p, a, "/"); p = a[1] "/enterprise/" substr(p, length(a[1]) + 2) }
  # Repo-level config is parked under .enterprise/ so enterprise workflows stay inactive until ported.
  else if (p ~ /^(\.github|\.devcontainer|\.vscode)\//) p = ".enterprise/" p
  else if (n == 1) p = ".enterprise/" p
  print $1 "\t" p
}' > "$GIT_INDEX_FILE.map"
git read-tree --empty
git update-index --index-info < "$GIT_INDEX_FILE.map"
rm -f "$GIT_INDEX_FILE.map"
