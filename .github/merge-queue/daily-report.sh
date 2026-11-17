#!/usr/bin/env bash
# Summarizes the merge queue over the last HOURS hours and posts it to Slack.
# Prints the payload instead when SLACK_WEBHOOK_URL is unset, so it can be run locally with gh.
set -euo pipefail

REPO=${REPO:-odigos-io/odigos}
HOURS=${HOURS:-24}
SINCE=$(jq -rn --argjson h "$HOURS" 'now - $h * 3600 | todate')

PRS=$(gh api graphql --paginate -f q="repo:$REPO is:pr updated:>=$SINCE" -f query='
  query($q: String!, $endCursor: String) {
    search(query: $q, type: ISSUE, first: 50, after: $endCursor) {
      pageInfo { hasNextPage endCursor }
      nodes { ... on PullRequest {
        timelineItems(itemTypes: [ADDED_TO_MERGE_QUEUE_EVENT, REMOVED_FROM_MERGE_QUEUE_EVENT, MERGED_EVENT], last: 100) {
          nodes { __typename
            ... on AddedToMergeQueueEvent { createdAt }
            ... on RemovedFromMergeQueueEvent { createdAt reason }
            ... on MergedEvent { createdAt }
          } } } } } }' --jq '.data.search.nodes[]' | jq -s .)

# One entry per queue attempt that ended in the window.
ATTEMPTS=$(jq --arg since "$SINCE" '[.[] | .timelineItems.nodes | sort_by(.createdAt) as $ev
  | range(0; $ev | length - 1) as $i | select($ev[$i].__typename == "AddedToMergeQueueEvent")
  | $ev[$i + 1] as $end
  | select($end.__typename != "AddedToMergeQueueEvent" and $end.createdAt >= $since)
  | (($end.reason // "merged") | ascii_downcase) as $reason
  | {
      merged: ($reason == "merged" or $reason == "already_merged"),
      reason: $reason,
      minutes: ((($end.createdAt | fromdate) - ($ev[$i].createdAt | fromdate)) / 60 | floor)
    }]' <<<"$PRS")

FAILED_RUNS=$(gh api "repos/$REPO/actions/runs?event=merge_group&status=failure&created=>=$SINCE&per_page=100" --paginate \
  --jq '.workflow_runs[].id')

JOBS=$(for RUN_ID in $FAILED_RUNS; do
  # "<Workflow> CI" jobs only aggregate the other jobs' results for the required check.
  gh api "repos/$REPO/actions/runs/$RUN_ID/jobs?filter=latest&per_page=100" --jq '.jobs[]
    | select((.conclusion == "failure" or .conclusion == "timed_out") and (.name | endswith(" CI") | not))
    | {name, url: .html_url}'
done | jq -s .)

if [ "$(jq length <<<"$ATTEMPTS")" = 0 ] && [ "$(jq length <<<"$JOBS")" = 0 ]; then
  echo "No merge queue activity in the last $HOURS hours"
  exit 0
fi

PAYLOAD=$(jq -n --argjson attempts "$ATTEMPTS" --argjson jobs "$JOBS" --arg hours "$HOURS" --arg repo "$REPO" '
  def esc: gsub("&"; "&amp;") | gsub("<"; "&lt;") | gsub(">"; "&gt;");
  def pick(p): sort | if length == 0 then null else .[(length - 1) * p | floor] end;
  def dur: if . == null then "n/a" elif . < 60 then "\(.)m" else "\(. / 60 | floor)h \(. % 60)m" end;

  ($attempts | length) as $total
  | ($attempts | map(select(.merged))) as $merged
  | ($attempts | map(select(.merged | not))) as $removed
  | ($merged | map(.minutes)) as $minutes
  | {
      summary: "Attempts \($total) · Merged \($merged | length) · Removed \($removed | length) (\(if $total == 0 then 0 else ($removed | length) * 100 / $total | round end)%)",
      reasons: ($removed | group_by(.reason) | map("\(.[0].reason) \(length)") | join(", ")),
      time: "Time in queue (merged): median \($minutes | pick(0.5) | dur) · p90 \($minutes | pick(0.9) | dur)",
      jobs: ($jobs | group_by(.name) | map({name: .[0].name, url: .[-1].url, count: length})
        | sort_by(-.count) | .[:5]
        | map("• <\(.url)|\(.name | esc)>: \(.count)") | join("\n"))
    }
  | {
      text: "Merge queue report for the last \($hours)h",
      blocks: [
        {type: "section", text: {type: "mrkdwn", text: ":bar_chart: *<https://github.com/\($repo)/queue/main|Merge queue>, last \($hours)h*"}},
        {type: "section", text: {type: "mrkdwn", text: ([.summary, (if .reasons != "" then "Removed by reason: \(.reasons)" else empty end), .time] | join("\n"))}},
        {type: "section", text: {type: "mrkdwn", text: (if .jobs != "" then "*Top failing jobs:*\n\(.jobs)" else "No failed merge queue jobs" end)}}
      ]
    }')

if [ -z "${SLACK_WEBHOOK_URL:-}" ]; then
  echo "SLACK_WEBHOOK_URL is not set; payload:"
  echo "$PAYLOAD"
  exit 0
fi
curl -fsS -X POST -H 'Content-type: application/json' --data "$PAYLOAD" "$SLACK_WEBHOOK_URL"
