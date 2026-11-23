package interrogation

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/database"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/odigos-io/odigos/insights/internal/db/postgres/pgtest"
)

// ourIndexes are the indexes migration 000004 adds to ADK's tables.
var ourIndexes = []string{"interrogation_open_workload_idx", "interrogation_created_idx", "events_session_timestamp_idx"}

// statementLog records every statement gorm runs.
type statementLog struct {
	logger.Interface
	mu         sync.Mutex
	statements []string
}

func (l *statementLog) LogMode(logger.LogLevel) logger.Interface { return l }

func (l *statementLog) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statements = append(l.statements, sql)
}

func (l *statementLog) ddl() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, s := range l.statements {
		verb, _, _ := strings.Cut(strings.TrimSpace(s), " ")
		switch strings.ToUpper(verb) {
		case "CREATE", "ALTER", "DROP", "COMMENT":
			out = append(out, s)
		}
	}
	return out
}

// autoMigrate runs ADK's own migration on s's pool and returns the DDL it issued.
func autoMigrate(t *testing.T, s *Sessions) []string {
	t.Helper()
	log := &statementLog{Interface: logger.Discard}
	svc, err := database.NewSessionServiceFromDB(s.db.Session(&gorm.Session{Logger: log}))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(svc); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	if len(log.statements) == 0 {
		t.Fatal("no statement recorded during AutoMigrate")
	}
	return log.ddl()
}

func adkCatalog(t *testing.T, s *Sessions, query string) []string {
	t.Helper()
	var out []string
	if err := s.db.Raw(query).Scan(&out).Error; err != nil {
		t.Fatal(err)
	}
	return out
}

const (
	adkColumns = `
SELECT concat_ws(' ', table_name, ordinal_position, column_name, data_type, datetime_precision, is_nullable, column_default)
FROM information_schema.columns WHERE table_schema = 'adk'
ORDER BY table_name, ordinal_position`
	adkConstraints = `
SELECT concat_ws(' ', conrelid::regclass, conname, pg_get_constraintdef(oid))
FROM pg_constraint WHERE connamespace = 'adk'::regnamespace
ORDER BY conrelid::regclass::text, conname`
	adkIndexes = `
SELECT concat_ws(' ', c.relname, CASE WHEN i.indisvalid AND i.indisready THEN 'valid' ELSE 'invalid' END, pg_get_indexdef(c.oid))
FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
WHERE c.relnamespace = 'adk'::regnamespace
ORDER BY c.relname`
)

// TestADKSchemaMatches fails when ADK's tables in migration 000004 differ from
// what the pinned adk-go expects. Run it on every adk-go upgrade.
func TestADKSchemaMatches(t *testing.T) {
	s := openTestSessions(t, pgtest.NewDatabase(t))

	t.Run("tables are the ones AutoMigrate creates", func(t *testing.T) {
		dsn := pgtest.NewEmptyDatabase(t)
		if _, err := pgtest.Connect(t, dsn).Exec(context.Background(), "CREATE SCHEMA "+schema); err != nil {
			t.Fatal(err)
		}
		fresh := openTestSessions(t, dsn)
		autoMigrate(t, fresh)
		for _, q := range []string{adkColumns, adkConstraints} {
			if got, want := adkCatalog(t, s, q), adkCatalog(t, fresh, q); !slices.Equal(got, want) {
				t.Errorf("migration 000004:\n%s\nAutoMigrate:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		}
	})

	t.Run("AutoMigrate changes nothing", func(t *testing.T) {
		var noop []string
		for _, column := range []struct{ table, name string }{
			{"sessions", "create_time"},
			{"sessions", "update_time"},
			{"events", "timestamp"},
			{"app_states", "update_time"},
			{"user_states", "update_time"},
		} {
			// AutoMigrate re-applies these types on every run, also to a schema it
			// created itself.
			noop = append(noop, fmt.Sprintf(`ALTER TABLE %q ALTER COLUMN %q TYPE timestamptz(6) USING %[2]q::timestamptz(6)`, column.table, column.name))
		}

		indexesBefore := adkCatalog(t, s, adkIndexes)
		for run := range 2 {
			for _, stmt := range autoMigrate(t, s) {
				if !slices.Contains(noop, stmt) {
					t.Errorf("AutoMigrate run %d issued %s", run+1, stmt)
				}
			}
		}
		indexesAfter := adkCatalog(t, s, adkIndexes)
		if !slices.Equal(indexesBefore, indexesAfter) {
			t.Errorf("indexes before AutoMigrate:\n%s\nafter:\n%s", strings.Join(indexesBefore, "\n"), strings.Join(indexesAfter, "\n"))
		}
		for _, name := range ourIndexes {
			if !slices.ContainsFunc(indexesAfter, func(l string) bool { return strings.HasPrefix(l, name+" valid ") }) {
				t.Errorf("index %s missing or not valid after AutoMigrate", name)
			}
		}
	})

	t.Run("service reads and writes schema adk", func(t *testing.T) {
		ctx := context.Background()
		created, err := s.Service().Create(ctx, &session.CreateRequest{
			AppName: "interrogation", UserID: "run-1", SessionID: "run-1",
			State: map[string]any{"workload": "ns/Deployment/app", "open": true},
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := s.Service().Get(ctx, &session.GetRequest{AppName: "interrogation", UserID: "run-1", SessionID: "run-1"})
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Session.ID() != created.Session.ID() {
			t.Errorf("got session %q, want %q", got.Session.ID(), created.Session.ID())
		}
		var workload string
		if err := s.db.Raw(`SELECT state->>'workload' FROM adk.sessions WHERE app_name = 'interrogation' AND id = 'run-1'`).
			Row().Scan(&workload); err != nil {
			t.Fatalf("read adk.sessions: %v", err)
		}
		if workload != "ns/Deployment/app" {
			t.Errorf("adk.sessions state.workload = %q", workload)
		}
	})
}
