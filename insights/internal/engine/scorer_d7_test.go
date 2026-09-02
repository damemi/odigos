package engine

import (
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// dbTrace builds a trace with a single DB client span carrying the given attrs.
func dbTrace(attrs map[string]string) *Trace {
	return mkTrace(
		withRoot("POST /checkout", "checkout-svc", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "query", tracepb.Span_SPAN_KIND_CLIENT, attrs, "jdbc"),
	)
}

func learnD7(t *testing.T, traces ...*Trace) (baseline []byte) {
	t.Helper()
	var raw []byte
	for _, tr := range traces {
		merged, err := (d7DBAccess{}).Merge(tr, raw)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		raw = merged
	}
	return raw
}

func TestD7_EmptyBaselineNeverDeviates(t *testing.T) {
	tr := dbTrace(map[string]string{
		"db.system": "postgresql", "db.query.text": "SELECT * FROM users",
	})
	if dev, _, _ := (d7DBAccess{}).Score(tr, nil); dev {
		t.Errorf("empty baseline must not deviate")
	}
}

func TestD7_NewTableDeviates(t *testing.T) {
	baseline := learnD7(t, dbTrace(map[string]string{
		"db.system": "postgresql", "db.namespace": "app",
		"db.query.text": "SELECT * FROM users WHERE id = 1",
	}))

	if dev, _, _ := (d7DBAccess{}).Score(dbTrace(map[string]string{
		"db.system": "postgresql", "db.namespace": "app",
		"db.query.text": "SELECT * FROM users WHERE id = 7",
	}), baseline); dev {
		t.Errorf("same table with a different literal must not deviate")
	}

	attack := dbTrace(map[string]string{
		"db.system": "postgresql", "db.namespace": "app",
		"db.query.text": "SELECT * FROM admins WHERE id = 1",
	})
	dev, ev, _ := (d7DBAccess{}).Score(attack, baseline)
	if !dev {
		t.Fatalf("expected deviation for a never-before-seen table")
	}
	if !contains(string(ev), "admins") {
		t.Errorf("evidence missing the new table: %s", ev)
	}
}

func TestD7_NewOperationOnKnownTableDeviates(t *testing.T) {
	baseline := learnD7(t, dbTrace(map[string]string{
		"db.system": "postgresql", "db.query.text": "SELECT * FROM users",
	}))

	attack := dbTrace(map[string]string{
		"db.system": "postgresql", "db.query.text": "DELETE FROM users",
	})
	dev, ev, _ := (d7DBAccess{}).Score(attack, baseline)
	if !dev {
		t.Fatalf("expected deviation for a new operation on a known table")
	}
	if !contains(string(ev), "DELETE") {
		t.Errorf("evidence missing the new operation: %s", ev)
	}
}

func TestD7_NewSystemDeviates(t *testing.T) {
	baseline := learnD7(t, dbTrace(map[string]string{
		"db.system": "postgresql", "db.collection.name": "users",
	}))

	attack := dbTrace(map[string]string{
		"db.system": "redis", "db.collection.name": "users",
	})
	if dev, _, _ := (d7DBAccess{}).Score(attack, baseline); !dev {
		t.Fatalf("expected deviation for a new database system")
	}
}

// db.operation.name (set by the gateway processor) wins over the parsed verb.
func TestD7_OperationNameAttributeUsed(t *testing.T) {
	baseline := learnD7(t, dbTrace(map[string]string{
		"db.system": "postgresql", "db.collection.name": "users",
		"db.operation.name": "SELECT",
	}))

	attack := dbTrace(map[string]string{
		"db.system": "postgresql", "db.collection.name": "users",
		"db.operation.name": "DELETE",
	})
	if dev, _, _ := (d7DBAccess{}).Score(attack, baseline); !dev {
		t.Fatalf("expected deviation when db.operation.name changes")
	}
}

// A join of two known tables is a new co-occurrence (query) shape even though
// neither table is new on its own.
func TestD7_NewJoinShapeDeviates(t *testing.T) {
	baseline := learnD7(t,
		dbTrace(map[string]string{"db.system": "postgresql", "db.query.text": "SELECT * FROM users"}),
		dbTrace(map[string]string{"db.system": "postgresql", "db.query.text": "SELECT * FROM orders"}),
	)

	join := dbTrace(map[string]string{
		"db.system":     "postgresql",
		"db.query.text": "SELECT * FROM users JOIN orders ON users.id = orders.user_id",
	})
	dev, ev, _ := (d7DBAccess{}).Score(join, baseline)
	if !dev {
		t.Fatalf("expected deviation for a new join shape over known tables")
	}
	if !contains(string(ev), "orders,users") {
		t.Errorf("evidence missing the canonical join shape: %s", ev)
	}
}

// Opaque statements (no parseable table) collapse to a stable bounded hash, so
// repeats of the same statement don't deviate.
func TestD7_OpaqueStatementStable(t *testing.T) {
	baseline := learnD7(t, dbTrace(map[string]string{
		"db.system": "postgresql", "db.query.text": "BEGIN",
	}))
	if dev, _, _ := (d7DBAccess{}).Score(dbTrace(map[string]string{
		"db.system": "postgresql", "db.query.text": "BEGIN",
	}), baseline); dev {
		t.Errorf("identical opaque statement must not deviate")
	}
}

func TestD7_LegacyAttributesFallback(t *testing.T) {
	// pre-v1.27 names: db.statement / db.name / db.system.name
	baseline := learnD7(t, dbTrace(map[string]string{
		"db.system.name": "mysql", "db.name": "shop",
		"db.statement": "SELECT * FROM products",
	}))

	attack := dbTrace(map[string]string{
		"db.system.name": "mysql", "db.name": "shop",
		"db.statement": "SELECT * FROM secrets",
	})
	if dev, _, _ := (d7DBAccess{}).Score(attack, baseline); !dev {
		t.Fatalf("expected deviation via legacy db.* attributes")
	}
}

func TestD7_NonDBTraceProducesNothing(t *testing.T) {
	tr := mkTrace(
		withRoot("GET /", "web", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "GET /api", tracepb.Span_SPAN_KIND_CLIENT,
			map[string]string{"server.address": "api.internal"}, "okhttp3"),
	)
	accesses, shapes := dbFactsFromTrace(tr)
	if len(accesses) != 0 || len(shapes) != 0 {
		t.Errorf("non-DB trace should yield no db facts, got %d accesses %d shapes", len(accesses), len(shapes))
	}
}

func TestParseSQLTables(t *testing.T) {
	cases := map[string][]string{
		"SELECT * FROM users WHERE id = 1":                        {"users"},
		"select id from Orders o":                                 {"orders"},
		"INSERT INTO audit_log (a) VALUES (1)":                    {"audit_log"},
		"UPDATE accounts SET balance = 0":                         {"accounts"},
		"DELETE FROM sessions":                                    {"sessions"},
		"SELECT * FROM users u JOIN orders o ON u.id = o.user_id": {"orders", "users"},
		"SELECT * FROM `users`":                                   {"users"},
		// SQLAlchemy 2.0 insertmanyvalues: FROM (VALUES ...) must not yield "values".
		"INSERT INTO health_logs (a, b) SELECT p0, p1 FROM (VALUES (1, 2)) AS sen(p0, p1)": {"health_logs"},
	}
	for q, want := range cases {
		got := parseSQLTables(q)
		if !equalStrings(got, want) {
			t.Errorf("parseSQLTables(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestDetectSQLOperation(t *testing.T) {
	cases := map[string]string{
		"SELECT 1":            "SELECT",
		"  insert into x ...": "INSERT",
		"UPDATE x SET a=1":    "UPDATE",
		"delete from x":       "DELETE",
		"BEGIN":               "",
		"":                    "",
	}
	for q, want := range cases {
		if got := detectSQLOperation(q); got != want {
			t.Errorf("detectSQLOperation(%q) = %q, want %q", q, got, want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
