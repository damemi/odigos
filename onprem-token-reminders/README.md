# onprem-token-reminders

Keeps Slack informed about expiring on-prem tokens. Two commands, no dependencies beyond the Go standard library.

| Command | Runs from | Does |
|---|---|---|
| `register` | `Generate On-Prem Token` workflow, every run | Opens a tracker issue for the token and schedules the reminders that are due within two days; with `notify` off the tracker still exists and its reminders are `muted` |
| `sweep` | `On-Prem Token Reminders` workflow, daily at 06:17 UTC | Hands Slack the reminders due within the next two days, backfills reminders added since a token was registered, posts overdue ones, closes trackers of expired tokens, cancels reminders of trackers closed by hand |

## How it works

- **The timer is Slack, but only for the next two days.** `chat.scheduleMessage` delivers at the exact second; a reminder is handed to Slack only when it is due within 48 hours (the sweep runs daily, two days covers the cadence plus cron drift). Everything further out stays `pending` in the tracker, so Slack's queue never holds more than a couple of days of reminders, tokens of any length and any number of tokens are covered, and Slack's own limits (120-day horizon, 30 messages per 5-minute window per channel) are never approached. A reminder Slack refuses stays pending and is retried by the next sweep.
- **The tracker is a GitHub issue per token**, labelled `onprem-token`, created in this repository. Its body shows the token's metadata and carries the machine state in an HTML comment (`<!-- onprem-token-state … -->`). Do not edit that block by hand.
- **Reminders:** 7 days, 24 hours and 1 hour before `exp`. Each is `pending` (not due soon), `scheduled` (Slack holds it, id recorded), `sent` (its time passed while pending because a sweep was missed, so the sweep posted it directly), `skipped` (already past when the token was registered, or the token expired), `cancelled`, or `muted` (issued with `notify` off: the token is tracked on the board but never announced).
- **Closing a tracker issue by hand** (token rotated early, customer gone) makes the next sweep delete the reminders Slack still holds.
- **Adding a reminder** is one line in `reminderOffsets` (`main.go`). New tokens get it from `register`, and the next daily sweep backfills it into every open tracker: `muted` if the token was issued with `notify` off, `skipped` if its moment has already passed (it is never posted as overdue), otherwise `pending` like any other.
- **The board.** Trackers join the org project [On-prem tokens](https://github.com/orgs/odigos-io/projects/4) through its label rule; the tool fills `Issued` and `Expires` and keeps `Status` in step: `Active` while more than 30 days remain, `Expires soon` once a token is within 30 days of expiry (flipped by the daily sweep), `Expired` set by the project's own close rule. Writing Projects v2 fields needs `organization_projects: write`, so both workflows mint a short-lived token through `ci-core/sts` (identity `onprem-token-reminders`, policy `.github/chainguard/onprem-token-reminders.sts.yaml` in this repo; the org permission rides along on the repo-scoped token). That step is `continue-on-error`: without `PROJECT_TOKEN` the board is skipped and the summary says so, and the next sweep syncs any tracker that never reached the board.
- **Messages never contain the token.** They carry audience, expiry, profiles, who issued it, the run and the tracker issue.
- **Fail-closed:** a missing secret or an unreadable expiry fails the step; the sweep processes every tracker before reporting the first error.

## Configuration

| Variable | Used by | Meaning |
|---|---|---|
| `SLACK_BOT_TOKEN` | both | Bot token (`xoxb-…`) with `chat:write` and `chat:write.public`; repo secret `SLACK_ONPREM_TOKENS_BOT_TOKEN` |
| `SLACK_CHANNEL_ID` | both | Channel ID, set in the workflow `env` |
| `SLACK_MENTIONS` | both | Optional comma-separated Slack user IDs tagged at the end of every reminder (`cc @…`) |
| `GITHUB_TOKEN`, `GITHUB_REPOSITORY` | both | Provided by Actions; needs `issues: write` |
| `TRACKER_LABEL` | both | Defaults to `onprem-token` (created on first use) |
| `AUD`, `IAT`, `EXP`, `PROFILES`, `ISSUED_BY`, `RUN_URL` | `register` | Token metadata; `IAT` and `EXP` are the unix issue and expiry times decoded from the JWT (`IAT` falls back to the run time) |
| `NOTIFY` | `register` | `false` mutes the reminders; anything else schedules them. The tracker is created either way |
| `PROJECT_TOKEN`, `PROJECT_NUMBER`, `PROJECT_OWNER` | both | Board access: an octo-sts token with `organization_projects: write`, the project number, and its owner (defaults to the repository owner). Unset token = board skipped |

## Development

```bash
cd onprem-token-reminders
go test ./... -race
```

Tests run against in-memory fakes of the Slack and GitHub APIs; nothing talks to the network.
