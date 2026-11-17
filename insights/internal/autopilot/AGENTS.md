# AGENTS.md - Autopilot

The autopilot loop (`loop/`) analyzes per-transaction call graphs with an LLM and turns the recommendations into InstrumentationRules. It runs only on the insights leader (see `insights/AGENTS.md`).

## Invariants

- **Level-based, not edge-triggered.** Every tick compares desired state with what has actually been achieved and acts on the difference. Never persist progress for work that did not succeed: a failed, skipped, or deferred step must leave its input pending for a later tick.
- **Evidence is acknowledged only by a successful run.** The delta base is the newest `tx_runs` row with `AnalysisStatus = succeeded` at the current `AnalysisRevision`. Older rows whose apply failed are also excluded. Failed rows are audit only. Pending edges = observed - acknowledged.
- **Bumping `schema.PromptRevision` or `schema.RequestVersion`, or changing the model alias, changes the analysis revision** and causes one full re-analysis of every transaction. Bump it on purpose, since it costs tokens.
- **Write order on success:** recommendations to `tx_recommendations` first, then the `tx_runs` row that acknowledges the evidence. A crash in between repeats the call, which is safe because recommendation inserts are idempotent.
- **`tx_recommendations` is the desired probe set**, kept for dry-run and auto-apply alike. It holds one row per (workload, Language, Target), and the earliest `FirstSeen` sets the order for probe caps.
- **Rules are reconciled, never merged.** `reconcileRules` (end of every tick, and on any InstrumentationRule event through `RuleEvents`) sets each workload's `odig-int-*` rule to exactly the stored probes, minus probes another enabled rule already provides:
  - It creates the rule if it is missing (a deleted rule comes back).
  - It reverts manual edits.
  - It deletes the rule when no probes remain or the workload's InstrumentationConfig is gone.
  - It never touches a rule with `spec.disabled`: disabling the rule is how a user opts a workload out.
  - In dry-run or mode `off` it only deletes rules for removed workloads.
  - Probe `AddedAt` comes from the stored `FirstSeen`, so rebuilding a rule gives the same spec and an up-to-date rule is not written again.
- **Only the loop goroutine writes rules.** The controller's `Reconcile` only signals the loop.
- **Retry state is in memory** (`retryTracker`). Losing it on restart or leader change only causes one early retry. Quota, budget, and auth errors pause every model call. A request that fails the same way 3 times waits until its input changes.
- **Migrations:** the autopilot schema is applied by the insights migrate Job (`internal/db/clickhouse/migrate/autopilot`). Keep `;` out of SQL comments, and keep `SchemaReady` in step with the tables and columns the loop needs.

## Follow-ups

Track work here until it has a ticket. Remove an entry when it lands.

- **Evidence never expires (staleness).** `tx_call_trie_structure` has no timestamp and only grows, and acknowledged edges and stored recommendations never age out. A function that is renamed or deleted in a later deploy keeps its recommendation, and its probe stays applied for good. Proposed fix:
  - Add `LastSeen` to trie rows, refreshed on every flush, plus a ClickHouse TTL.
  - Carry `LastSeen` into `tx_recommendations`.
  - Have the rule reconciler drop probes whose function has not been observed within a window (e.g. 30 days).
  This touches the trie flusher and the exporter's data contract, so it needs its own ticket.
- **Old-revision recommendations.** After a revision bump, recommendations from the previous revision stay in the desired set until the full re-analysis replaces them. Decide whether the reconciler should eventually prefer current-revision rows, without shrinking rules while the re-analysis is still running.
- **Per-probe rejection.** Users can only opt out of a whole autopilot rule (`spec.disabled`). Add a way to reject a single recommended probe (for example a `Rejected` row or flag that the reconciler honors) so it is neither re-applied nor re-recommended.
- **Probe cap (CORE-1940).** `maxProbesPerWorkload` is not on this branch. Once merged, apply the cap in `buildDesiredProbes` (oldest `FirstSeen` wins) as well as to skip analysis, so rules never exceed it.
- **Batched lookups.** `processSource` makes one `LastSuccessfulRun` query per transaction (N+1). Replace it with one `argMax` query per source.
