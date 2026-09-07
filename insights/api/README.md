# Insights API

The API is defined by one OpenAPI spec: [`openapi.yaml`](./openapi.yaml).
Both the Go server wire types and the TypeScript client are generated from it,
so the contract has a single source of truth.

- Go: `internal/api/http/apitypes/models.gen.go` (checked in). Handlers use these
  as the wire types; `internal/api/http/apimap` maps repo domain structs to them.
- TypeScript: `client/api.d.ts`, consumed by the frontend.

The running service also serves the spec: `GET /api/v1/openapi.yaml` (raw) and
`GET /api/v1/docs` (Swagger UI).

## Regenerate

```sh
make -C .. api-gen        # both Go models and the TS client
make -C .. api-gen-go     # Go models only (uses api/oapi-codegen.yaml)
make -C .. api-gen-ts     # TS client only
```

The Go models are checked in; CI runs `make api-verify` to fail the build if
they drift from the spec. Regenerate and commit `models.gen.go` after any spec
change.

## Consuming from the frontend

`client/api.d.ts` is **types only** (`openapi-typescript` output: `paths`,
`operations`, `components`) - not a runtime SDK. The frontend turns it into a
typed client by pairing it with [`openapi-fetch`](https://openapi-ts.dev/openapi-fetch/)
(same author, ~1 KB, no generated code):

```ts
import createClient from "openapi-fetch";
import type { paths } from "./api"; // the generated api.d.ts

const api = createClient<paths>({ baseUrl: "/api/v1" });

// Fully typed path, params, and response - no hand-written types.
const { data, error } = await api.GET("/api/v1/findings", {
  params: { query: { window_hours: 168 } },
});
```

**Distribution (the UI lives in the separate OSS `odigos` repo):** the frontend
owns generation - it runs `openapi-typescript` against the spec rather than
importing a file from this repo, so this repo stays unaware of the UI and the
types are always current. Two sources:

- The running service serves it: `openapi-typescript http://<insights>/api/v1/openapi.yaml -o src/api.d.ts`.
- Or a pinned copy of [`openapi.yaml`](./openapi.yaml) (git subtree/submodule) generated at build time.

Before the backend endpoint exists, the frontend can develop against a mock of
the contract: `make -C .. api-mock` serves a Prism mock on `:4010`.

## Which API when (UI screens)

Simple map from product screens to endpoints. Rule of thumb: **lists are cheap,
detail on click**.

### Findings

- **Findings table (open / dismissed / accepted tabs)** → `GET /findings?window_hours=` (or `from`/`to`) `&status=&service=&namespace=`
- **Anomalies-only view** → `GET /anomalies` (same filters)
- **Violations-only view** → `GET /guardrails/violations`
- **User clicks an anomaly row** → `GET /anomalies/{transaction_id}/{signature}` (investigate: evidence + risk + class_findings + compare traces)
- **Bulk accept/dismiss** → `POST /anomalies` with `{resolution, items}`

### Transaction detail

- **Page load: title + identity** → `GET /transactions/{transaction_id}`
- **Baseline classes table** → `GET /transactions/{transaction_id}/baseline`
- **Example traces list** → `GET /transactions/{transaction_id}/observations?sample_reason=example`
- **Anomaly issues on this txn** → `GET /anomalies?…` (filter client-side by `transaction_id`) or dedicated list when added
- **User clicks “view trace”** → `GET /transactions/{transaction_id}/observations/{trace_id}` (load OTLP here, not on page load)
- **Promote / reset baseline** → POST under `…/baseline/…`

### Transaction directory

- **Service overview / all transactions** → `GET /transactions` (has volume + last_seen)
- **Drill into one service** → `GET /transactions?namespace=&service=`

### Anomaly investigate / compare

1. `GET /anomalies/{transaction_id}/{signature}` - full investigate page payload:
   evidence, risk, `class_findings` (title/summary/highlights), plus
   `anomaly_trace` and `baseline_trace` OTLP blobs for the side-by-side waterfall
2. **Blast-radius service map** → `GET /services/blast-radius?namespace=&service=&depth=`
   - the callers/callees (and virtual dependencies) around the anomalous
   service, from the gateway's servicegraph metrics. `depth` defaults to 2.
3. Extra samples (rare) → `GET …/observations` + `GET …/observations/{trace_id}`

### Violation investigate

1. `GET /guardrails/violations/evidence?scope_key=&rule_key=&offending=` - analog of
   the anomaly detail: violation fields, `summary`, span highlights, and
   `evidence_trace` (OTLP) when a sample was retained.

### Settings / admin

- Policies, learning policies, guardrails, system settings - CRUD under `/policies`, `/learning-policies`, `/guardrails`, `/system-settings`
- Storage diagnostics → `GET /storage-health`

### Do not

- Do not expect `raw_otlp` on `listObservations`
- Do not expect `evidence`, `class_findings`, or traces on `listAnomalies` or `listFindings`
- Do not call `GET /transactions` with a high limit just to resolve one id - use `GET /transactions/{transaction_id}`
- Do not re-fetch observations for the default investigate compare - they are on the detail response

Interactive docs: `GET /api/v1/docs` (Swagger UI) reads the same spec, including **Use for / Not for** notes on each operation.

## Versioning

`info.version` in [`openapi.yaml`](./openapi.yaml) is the **contract's** semver
(independent of the service image version). Bump it on every spec change:

- **major** - breaking: remove/rename a field, change a type, remove an endpoint,
  or add a `required` field.
- **minor** - additive and backwards-compatible: a new endpoint or a new
  optional field.
- **patch** - docs/descriptions only.

The `v1` in the URL path (`/api/v1/...`) carries the **major**: a breaking change
introduces `/api/v2` (keeping `v1` alive during migration) and bumps
`info.version` to `2.x`, so the path major and `info.version` major stay in
lockstep. Until there are external consumers, stay on `/api/v1` and bump the
minor for additions.

CI runs [`oasdiff`](https://github.com/oasdiff/oasdiff) against the base branch
and fails the build on a breaking change, so the version field stays meaningful.
