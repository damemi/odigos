package engine

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// d7DBAccess scores against the database accesses a transaction makes, tracking
// two compact, bounded facets — never raw SQL text:
//
//   - accesses: the set of (system, namespace, table, operation) touched.
//     Catches a new table or a new operation on a known table (a read path that
//     suddenly DELETEs).
//   - query_shapes: each statement's canonical {operation, sorted unique
//     tables} signature. Catches new join / co-occurrence shapes. When a
//     statement's table can't be parsed the signature falls back to a
//     fixed-size hash of the normalized text so it stays bounded.
//
// JSONB shape:
//
//	{"accesses":[{"system":"postgresql","ns":"appdb","table":"users","op":"SELECT"}],
//	 "query_shapes":[{"op":"SELECT","tables":"orders,users"}]}
type d7DBAccess struct{}

type dbAccess struct {
	System string `json:"system"`
	NS     string `json:"ns"`
	Table  string `json:"table"`
	Op     string `json:"op"`
}

// queryShape is the canonical per-statement signature. Tables is the sorted,
// unique, comma-joined table list (one string so the struct stays a comparable
// map key), or "#<hash>" for an unparseable statement.
type queryShape struct {
	Op     string `json:"op"`
	Tables string `json:"tables"`
}

type d7Data struct {
	Accesses    []dbAccess   `json:"accesses"`
	QueryShapes []queryShape `json:"query_shapes"`
}

func (d7DBAccess) Class() repo.DeviationClass { return repo.D7DBAccess }

func (s d7DBAccess) Score(t *Trace, current json.RawMessage) (bool, json.RawMessage, error) {
	return s.decodeBaseline(current).score(t)
}

// decodeBaseline implements baselineDecoder.
func (d7DBAccess) decodeBaseline(raw json.RawMessage) decodedBaseline {
	d, _ := decodeD7(raw)
	return d
}

// score implements decodedBaseline; it only reads baseline.
func (baseline d7Data) score(t *Trace) (bool, json.RawMessage, error) {
	// Nothing learned yet (cold start / re-learn): nothing can deviate.
	if len(baseline.Accesses) == 0 && len(baseline.QueryShapes) == 0 {
		return false, nil, nil
	}

	knownAcc := make(map[dbAccess]struct{}, len(baseline.Accesses))
	for _, a := range baseline.Accesses {
		knownAcc[a] = struct{}{}
	}
	knownShape := make(map[queryShape]struct{}, len(baseline.QueryShapes))
	for _, s := range baseline.QueryShapes {
		knownShape[s] = struct{}{}
	}

	accesses, shapes := dbFactsFromTrace(t)

	var newAcc []dbAccess
	for _, a := range accesses {
		if _, ok := knownAcc[a]; !ok {
			newAcc = append(newAcc, a)
			knownAcc[a] = struct{}{}
		}
	}
	var newShapes []queryShape
	for _, s := range shapes {
		if _, ok := knownShape[s]; !ok {
			newShapes = append(newShapes, s)
			knownShape[s] = struct{}{}
		}
	}
	if len(newAcc) == 0 && len(newShapes) == 0 {
		return false, nil, nil
	}
	ev, err := json.Marshal(map[string]any{
		"new_accesses":     newAcc,
		"new_query_shapes": newShapes,
	})
	if err != nil {
		return true, nil, err
	}
	return true, ev, nil
}

func (d7DBAccess) Merge(t *Trace, current json.RawMessage) (json.RawMessage, error) {
	baseline, _ := decodeD7(current)
	accSet := make(map[dbAccess]struct{}, len(baseline.Accesses))
	for _, a := range baseline.Accesses {
		accSet[a] = struct{}{}
	}
	shapeSet := make(map[queryShape]struct{}, len(baseline.QueryShapes))
	for _, s := range baseline.QueryShapes {
		shapeSet[s] = struct{}{}
	}

	accesses, shapes := dbFactsFromTrace(t)
	for _, a := range accesses {
		accSet[a] = struct{}{}
	}
	for _, s := range shapes {
		shapeSet[s] = struct{}{}
	}

	mergedAcc := make([]dbAccess, 0, len(accSet))
	for a := range accSet {
		mergedAcc = append(mergedAcc, a)
	}
	sort.Slice(mergedAcc, func(i, j int) bool { return lessDBAccess(mergedAcc[i], mergedAcc[j]) })

	mergedShapes := make([]queryShape, 0, len(shapeSet))
	for s := range shapeSet {
		mergedShapes = append(mergedShapes, s)
	}
	sort.Slice(mergedShapes, func(i, j int) bool {
		if mergedShapes[i].Op != mergedShapes[j].Op {
			return mergedShapes[i].Op < mergedShapes[j].Op
		}
		return mergedShapes[i].Tables < mergedShapes[j].Tables
	})

	return json.Marshal(d7Data{Accesses: mergedAcc, QueryShapes: mergedShapes})
}

// Explain implements Explainer: it re-derives each span's db facts with the
// same helper Score uses (dbFactsForSpan) and highlights the span(s) whose
// access or query shape matches the evidence's new set, surfacing the offending
// statement as an attribute highlight.
func (d7DBAccess) Explain(evidence json.RawMessage, spans []SpanView) Finding {
	f := Finding{
		Class:       repo.D7DBAccess,
		Kind:        FindingStructural,
		Title:       titleFor(repo.D7DBAccess),
		RawEvidence: evidence,
	}

	var ev struct {
		NewAccesses    []dbAccess   `json:"new_accesses"`
		NewQueryShapes []queryShape `json:"new_query_shapes"`
	}
	_ = json.Unmarshal(evidence, &ev)

	newAcc := make(map[dbAccess]struct{}, len(ev.NewAccesses))
	for _, a := range ev.NewAccesses {
		newAcc[a] = struct{}{}
	}
	newShape := make(map[queryShape]struct{}, len(ev.NewQueryShapes))
	for _, s := range ev.NewQueryShapes {
		newShape[s] = struct{}{}
	}

	// Attribute each new access/shape back to the span that produced it, using
	// the same derivation the scorer uses.
	for _, v := range spans {
		syntheticSpan := &Span{SpanID: v.SpanID, Attrs: v.AttrMap()}
		accesses, shapes := dbFactsForSpan(syntheticSpan)
		matched := false
		for _, a := range accesses {
			if _, ok := newAcc[a]; ok {
				matched = true
				break
			}
		}
		if !matched {
			for _, s := range shapes {
				if _, ok := newShape[s]; ok {
					matched = true
					break
				}
			}
		}
		if !matched {
			continue
		}
		f.SpanHighlights = append(f.SpanHighlights, SpanHighlight{
			SpanID:   v.SpanID,
			Severity: "high",
			Reason:   "database access not seen during learning",
		})
		if stmt := firstAttr(v, "db.query.text", "db.statement"); stmt != "" {
			f.AttrHighlights = append(f.AttrHighlights, AttrHighlight{
				SpanID: v.SpanID,
				Key:    "db.query.text",
				Value:  stmt,
			})
		}
	}

	f.Summary = summarizeD7(ev.NewAccesses, ev.NewQueryShapes)
	return f
}

// d7DestructiveOps are SQL operations that modify or destroy data/schema rather
// than read it; a newly-appearing one on a read-only transaction is a strong
// "impact" signal.
var d7DestructiveOps = map[string]bool{
	"DELETE": true, "DROP": true, "TRUNCATE": true, "ALTER": true, "UPDATE": true,
}

// d7SensitiveTablesKey is the enricher-list key for the D7 sensitive-table
// keywords; the default list lives in the risk catalog.
const d7SensitiveTablesKey repo.EnricherListKey = "sensitive_tables"

// d7SchemaCatalogKey is the enricher-list key for the D7 schema-catalog table
// names (information_schema, pg_catalog, …); the default list lives in the risk
// catalog. A new access to one of these is schema-enumeration reconnaissance.
const d7SchemaCatalogKey repo.EnricherListKey = "schema_catalog_tables"

// d7InjectionShapeMinTables is the table count in a NEW query shape at or above
// which we treat it as a possible injection / data-harvesting join.
const d7InjectionShapeMinTables = 3

// EnrichRisk implements RiskEnricher: it escalates a D7 deviation beyond a
// benign new read. The base D7 signal stays low while these add-on signals push
// the score up only for dangerous sub-cases — a destructive operation, access
// to a sensitive table, a new multi-table (join) query shape, or access to a
// schema-catalog table (schema enumeration) — reusing the scorer's own
// dbAccess/queryShape types from the evidence.
func (d7DBAccess) EnrichRisk(evidence json.RawMessage, cfg enrichConfig) []risk.Signal {
	if len(evidence) == 0 {
		return nil
	}
	var ev struct {
		NewAccesses    []dbAccess   `json:"new_accesses"`
		NewQueryShapes []queryShape `json:"new_query_shapes"`
	}
	if err := json.Unmarshal(evidence, &ev); err != nil {
		return nil
	}
	sensitiveKeywords := cfg.list(d7SensitiveTablesKey, risk.DefaultList(d7SensitiveTablesKey))
	catalogKeywords := cfg.list(d7SchemaCatalogKey, risk.DefaultList(d7SchemaCatalogKey))

	var destructiveOps, sensitiveTables, multiTableJoins, schemaReconTables []string
	seenKeys := map[string]bool{}
	addOnce := func(bucket *[]string, dedupeKey, label string) {
		if label == "" || seenKeys[dedupeKey] {
			return
		}
		seenKeys[dedupeKey] = true
		*bucket = append(*bucket, label)
	}
	for _, access := range ev.NewAccesses {
		if op := strings.ToUpper(strings.TrimSpace(access.Op)); d7DestructiveOps[op] {
			addOnce(&destructiveOps, "d:"+op+" "+access.Table, strings.TrimSpace(op+" "+access.Table))
		}
		if isSensitiveTable(access.Table, sensitiveKeywords) {
			addOnce(&sensitiveTables, "s:"+access.Table, access.Table)
		}
		if isSensitiveTable(access.Table, catalogKeywords) {
			addOnce(&schemaReconTables, "r:"+access.Table, access.Table)
		}
	}
	for _, shape := range ev.NewQueryShapes {
		// A "#"-prefixed Tables value is an opaque statement hash, not a real
		// table list — skip it (see buildQueryShape above).
		if shape.Tables == "" || strings.HasPrefix(shape.Tables, "#") {
			continue
		}
		if strings.Count(shape.Tables, ",")+1 >= d7InjectionShapeMinTables {
			addOnce(&multiTableJoins, "j:"+shape.Op+shape.Tables, strings.TrimSpace(shape.Op+" ["+shape.Tables+"]"))
		}
	}

	var signals []risk.Signal
	if len(destructiveOps) > 0 {
		signals = append(signals, enricherSignal("d7_destructive_op", risk.CategoryImpact,
			"destructive DB operation not seen in learning: "+strings.Join(destructiveOps, ", ")))
	}
	if len(sensitiveTables) > 0 {
		signals = append(signals, enricherSignal("d7_sensitive_table", risk.CategoryExfiltration,
			"new access to sensitive table(s): "+strings.Join(sensitiveTables, ", ")))
	}
	if len(multiTableJoins) > 0 {
		signals = append(signals, enricherSignal("d7_injection_shape", risk.CategoryInjection,
			"new multi-table query shape (possible injection / data harvesting): "+strings.Join(multiTableJoins, ", ")))
	}
	if len(schemaReconTables) > 0 {
		signals = append(signals, enricherSignal("d7_schema_recon", risk.CategoryInjection,
			"new access to DB schema-catalog table(s) (possible schema enumeration): "+strings.Join(schemaReconTables, ", ")))
	}
	return signals
}

// isSensitiveTable reports whether a table name contains any of the given
// sensitive-data keywords (case-insensitive substring match, so
// "user_credentials" matches "credential").
func isSensitiveTable(table string, keywords []string) bool {
	lowered := strings.ToLower(table)
	for _, keyword := range keywords {
		if strings.Contains(lowered, keyword) {
			return true
		}
	}
	return false
}

func summarizeD7(accesses []dbAccess, shapes []queryShape) string {
	var parts []string
	if len(accesses) > 0 {
		labels := make([]string, 0, len(accesses))
		for _, a := range accesses {
			switch {
			case a.Op != "" && a.Table != "":
				labels = append(labels, fmt.Sprintf("%s on %s", a.Op, a.Table))
			case a.Table != "":
				labels = append(labels, a.Table)
			case a.Op != "":
				labels = append(labels, a.Op)
			default:
				labels = append(labels, a.System)
			}
		}
		parts = append(parts, "new access: "+strings.Join(dedupe(labels), ", "))
	}
	if len(shapes) > 0 {
		labels := make([]string, 0, len(shapes))
		for _, s := range shapes {
			if s.Tables != "" && !strings.HasPrefix(s.Tables, "#") {
				labels = append(labels, fmt.Sprintf("%s [%s]", s.Op, s.Tables))
			} else {
				labels = append(labels, s.Op)
			}
		}
		parts = append(parts, "new query shape: "+strings.Join(dedupe(labels), ", "))
	}
	if len(parts) == 0 {
		return "A database access not seen during learning occurred."
	}
	return "The anomalous trace made a " + strings.Join(parts, "; ") + "."
}

// firstAttr returns the first non-empty value among keys on a span view.
func firstAttr(v SpanView, keys ...string) string {
	m := v.AttrMap()
	for _, k := range keys {
		if val := m[k]; val != "" {
			return val
		}
	}
	return ""
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func lessDBAccess(a, b dbAccess) bool {
	if a.System != b.System {
		return a.System < b.System
	}
	if a.NS != b.NS {
		return a.NS < b.NS
	}
	if a.Table != b.Table {
		return a.Table < b.Table
	}
	return a.Op < b.Op
}

func decodeD7(raw json.RawMessage) (d7Data, error) {
	if len(raw) == 0 {
		return d7Data{}, nil
	}
	var d d7Data
	if err := json.Unmarshal(raw, &d); err != nil {
		// Unparseable for the current shape: treat as empty so the class
		// re-learns rather than erroring.
		return d7Data{}, err
	}
	return d, nil
}

// dbFactsFromTrace extracts the accesses and per-statement query shapes from
// every DB span in the trace. Used by Score and Merge.
func dbFactsFromTrace(t *Trace) ([]dbAccess, []queryShape) {
	var accesses []dbAccess
	var shapes []queryShape
	for _, s := range t.Spans {
		acc, shs := dbFactsForSpan(s)
		accesses = append(accesses, acc...)
		shapes = append(shapes, shs...)
	}
	return accesses, shapes
}

// dbFactsForSpan returns the (access, shape) facts a single span contributes,
// or nil for a non-DB span. Extracted so the D7 explainer can attribute a
// deviating access/shape back to the exact span, using the same derivation as
// scoring.
func dbFactsForSpan(s *Span) ([]dbAccess, []queryShape) {
	if !isDBSpan(s) {
		return nil, nil
	}
	system := firstNonEmpty(s.Attrs[semconv.DBSystem], s.Attrs[semconv.LegacyDBSystemName])
	ns := firstNonEmpty(s.Attrs[semconv.DBNamespace], s.Attrs[semconv.LegacyDBName])
	queryText := firstNonEmpty(s.Attrs[semconv.DBQueryText], s.Attrs[semconv.LegacyDBStatement])

	op := s.Attrs[semconv.DBOperationName]
	if op == "" {
		// db.operation.name isn't guaranteed (the gateway's
		// odigossqldboperationprocessor may not be in the pipeline), so
		// derive it here with the same keyword logic.
		op = detectSQLOperation(queryText)
	}

	tables := tablesForSpan(s, queryText)
	var accesses []dbAccess
	if len(tables) == 0 {
		// Opaque DB call (e.g. BEGIN/COMMIT, a redis PING): still learn
		// that this transaction talks to this system/namespace.
		accesses = append(accesses, dbAccess{System: system, NS: ns, Op: op})
	} else {
		for _, tbl := range tables {
			accesses = append(accesses, dbAccess{System: system, NS: ns, Table: tbl, Op: op})
		}
	}
	return accesses, []queryShape{buildQueryShape(op, tables, queryText)}
}

func isDBSpan(s *Span) bool {
	if s == nil {
		return false
	}
	return s.Attrs[semconv.DBSystem] != "" ||
		s.Attrs[semconv.LegacyDBSystemName] != "" ||
		s.Attrs[semconv.DBQueryText] != "" ||
		s.Attrs[semconv.LegacyDBStatement] != "" ||
		s.Attrs[semconv.DBCollectionName] != ""
}

// tablesForSpan prefers the standardized db.collection.name (NoSQL and some
// SQL instrumentations); otherwise it parses table names out of the SQL text.
func tablesForSpan(s *Span, queryText string) []string {
	if c := s.Attrs[semconv.DBCollectionName]; c != "" {
		return []string{strings.ToLower(c)}
	}
	if queryText == "" {
		return nil
	}
	return parseSQLTables(queryText)
}

// buildQueryShape returns the canonical signature for one statement: the
// operation plus the sorted, unique table list. When no table could be parsed
// (a tableless statement like `SELECT 1`, or a shape the heuristic doesn't
// understand) the Tables field becomes a "#"-prefixed hash of the normalized
// text instead — bounding it to 16 chars and marking it opaque, while staying
// stable across literals so the statement won't re-trigger after learning.
func buildQueryShape(op string, tables []string, queryText string) queryShape {
	if len(tables) > 0 {
		return queryShape{Op: op, Tables: strings.Join(sortedUnique(tables), ",")}
	}
	if queryText == "" {
		return queryShape{Op: op}
	}
	return queryShape{Op: op, Tables: "#" + shortHash(normalizeQuery(queryText))}
}

// detectSQLOperation mirrors the gateway's DetectSQLOperationName: the first
// word maps to a known DML/DDL verb, else "". Returns "" (not "UNKNOWN") so an
// undetectable op simply doesn't constrain the access key.
func detectSQLOperation(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	first := query
	for i := 0; i < len(query); i++ {
		if c := query[i]; c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			first = query[:i]
			break
		}
	}
	switch strings.ToUpper(first) {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER":
		return strings.ToUpper(first)
	default:
		return ""
	}
}

// sqlTableKeywords precede a table name in the statements we care about.
var sqlTableKeywords = map[string]struct{}{
	"FROM": {}, "JOIN": {}, "INTO": {}, "UPDATE": {},
}

// sqlNonTableTokens are words that can immediately follow a table keyword but
// are not tables, so we skip them. The classic case is SQLAlchemy 2.0's
// "insertmanyvalues": INSERT INTO t (...) SELECT ... FROM (VALUES (...)) — the
// token after FROM is "(VALUES", which must not be learned as a table.
var sqlNonTableTokens = map[string]struct{}{
	"SELECT": {}, "VALUES": {}, "SET": {}, "WHERE": {}, "LATERAL": {},
}

// parseSQLTables is a lightweight heuristic: it captures the token following
// FROM/JOIN/INTO/UPDATE. It is NOT a full SQL parser — CTEs, subqueries, and
// comma-separated FROM lists beyond the first table are not covered.
func parseSQLTables(query string) []string {
	fields := strings.Fields(query)
	var tables []string
	expect := false
	for _, f := range fields {
		if expect {
			expect = false
			// Clean punctuation first so "(VALUES" / "(SELECT" / "(" are
			// matched against the bare keyword set (and "(" collapses to "").
			tbl := cleanTableToken(f)
			if tbl == "" {
				continue
			}
			if _, skip := sqlNonTableTokens[strings.ToUpper(tbl)]; skip {
				continue
			}
			tables = append(tables, strings.ToLower(tbl))
			continue
		}
		if _, ok := sqlTableKeywords[strings.ToUpper(cleanTableToken(f))]; ok {
			expect = true
		}
	}
	return sortedUnique(tables)
}

func cleanTableToken(tok string) string {
	return strings.Trim(tok, "(),;`\"'[]")
}

var (
	reSQLString = regexp.MustCompile(`'[^']*'`)
	reSQLNumber = regexp.MustCompile(`\b\d+\b`)
	reSQLWS     = regexp.MustCompile(`\s+`)
)

// normalizeQuery is only used to produce the opaque-statement hash: lowercase,
// strip string/number literals, collapse whitespace. We hash the result rather
// than store it.
func normalizeQuery(q string) string {
	q = strings.ToLower(q)
	q = reSQLString.ReplaceAllString(q, "?")
	q = reSQLNumber.ReplaceAllString(q, "?")
	q = reSQLWS.ReplaceAllString(q, " ")
	return strings.TrimSpace(q)
}

// shortHash returns the first 8 bytes (16 hex chars) of a SHA-1 of s — a
// non-cryptographic fingerprint for bounding opaque query shapes, not a
// security primitive. 64 bits easily avoids collisions across the few tableless
// statements a transaction makes.
func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:8]) // 16 hex chars
}

func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
