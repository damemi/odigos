// Package engine is the scoring core of the insights service. It takes decoded
// traces, identifies their transaction, scores them against the per-class
// baselines stored in the repository, and either fires anomalies (when the risk
// score clears the policy threshold) or folds the trace into the baseline.
//
// A transaction is the *identity* of a class of traces (same
// service+namespace+operation+kind on the root span). Each transaction carries a
// set of per-class baselines describing its learned "normal"; the
// trace_observations table holds bounded representative traces kept for the UI:
//
//	example          — bounded set of normal-traffic samples per transaction
//	guardrail:<...>  — a trace that broke a service guardrail (evidence)
//	(anomaly)        — the trace that fired an anomaly
//
// # Deviation classes
//
// A DeviationClass (see repo) is one independent baseline dimension. Each learns,
// scores, and promotes on its own; a deviation becomes a risk signal weighed by
// class and magnitude into a 0-100 score (see risk.go). Implemented today: D1,
// D2, D3, D5, D7, D8. D4 (args/returns) and D6 (cross-attribute relations) are
// reserved constants wired as noop scorers — they ride the learning/promotion
// lifecycle but never deviate until a real scorer is written.
//
// Worked example — `POST /checkout` in user-service that calls inventory.reserve,
// payment.charge, stripe.com, and a Postgres DB. After ~1000 normal traces:
//
//	D1 — CALL EDGES (shape of the call tree)
//	  Baseline: {user.checkout→inventory.reserve, user.checkout→payment.charge,
//	             payment.charge→stripe.send}
//	  Deviates: a new edge, e.g. *→StringSubstitutor.replace.
//
//	D2 — EGRESS / PEERS (external {host,port} reached)
//	  Baseline: {stripe.com:443, users-db:5432, inventory-db:5432}
//	  Deviates: a new peer, e.g. attacker.com:1389.
//
//	D3 — LATENCY / SHAPE (per-span latency + span count)
//	  Baseline: {p50:48ms, p95:180ms, p99:210ms, span_count_mean:12.4}
//	  Deviates: 1800ms (8× p99), or 47 spans where the mean is 12.
//
//	D5 — LIBRARIES (reachable code.namespace/library/version)
//	  Baseline: {okhttp3@4.x, postgresql@42.x, redis.clients@4.x}
//	  Deviates: commons-text@1.10.0 loaded on a path that never used it.
//
//	D7 — DB ACCESS (db.system/table/operation touched)
//	  Baseline: {postgres:users:SELECT, postgres:orders:SELECT}
//	  Deviates: a new table/op, e.g. postgres:users:DELETE, or a `secrets` table.
//
//	D8 — PAYLOAD SIZE (request/response body-size histogram)
//	  Baseline: {req p95:2KB, resp p95:40KB}
//	  Deviates: a 5MB response where p95 is 40KB (possible bulk exfiltration).
//
// # Adding a deviation class
//
//  1. Add a constant to the DeviationClass block in repo.go and to
//     AllDeviationClasses.
//  2. Decide the JSON shape kept in BaselineClass.Data. If it is a set (unioned
//     members) or additive (summed counters), teach baseline_codec.go how to
//     split it into rows and rebuild it.
//  3. Add a scorer_dN.go implementing Scorer: given a trace and the baseline
//     row, report whether it deviates (plus an evidence blob) and how to fold a
//     normal trace back into the baseline.
//  4. Give the class a default weight and category in the risk catalog
//     (internal/risk) so the score accounts for it; add an enricher there if a
//     sub-case should escalate. No migration needed — class is a free-form
//     string and the stored value is opaque to the DB.
//
// # File map (one cohesive package, grouped by concern)
//
//	engine.go                orchestrates ingest → score → fire/fold
//	engine_admin.go          operator lifecycle actions (reset, accept, reload)
//
//	trace_decode.go          decode OTLP bytes into Trace/Span trees
//	trace_view.go            flatten a trace into UI-facing SpanViews
//	trace_transaction.go     derive a trace's transaction identity
//	trace_subtransaction.go  split a trace into per-service sub-transactions
//
//	scorer.go                Scorer/Explainer interfaces + class registry
//	scorer_dN.go             the individual deviation-class scorers
//	risk.go                  weigh deviation + enricher signals into a score
//	enrich.go                context enrichers that escalate specific sub-cases
//	finding.go               Finding output types + Explain dispatch
//
//	baseline_store.go        in-memory learned-population store
//	baseline_codec.go        per-class JSON ⇄ flat member/counter rows
//	baseline_merge.go        merge two baselines of the same class
//	baseline_flusher.go      periodic delta flush to the repository
//
//	learning_config.go       per-class promotion policies (loaded at startup)
//	promotion.go             learning → active promotion decision
//	alert_config.go          anomaly-firing policy snapshot + resolution
//	sampler.go               bounded example-trace sampling
//
//	guardrail*.go            service-guardrail enforcement: config snapshot,
//	                         per-trace evaluation, violation buffer, evidence
//
// Subpackages hold the self-contained, reusable leaves:
//
//	histogram/  exponential histogram (D3 latency, D8 payload sizes)
//	semconv/    OTel semantic-convention attribute-key resolution
package engine
