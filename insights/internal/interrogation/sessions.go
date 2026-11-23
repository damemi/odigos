package interrogation

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/database"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	// schema holds ADK's session tables, whose names ADK fixes.
	schema = "adk"

	maxConns = 4
	// maxConnIdleTime is short because the pool is used in bursts (a start, a
	// stop) on every replica; pgxpool would otherwise keep an idle connection
	// for 30 minutes.
	maxConnIdleTime = time.Minute
)

// Sessions is ADK's session store on the insights Postgres database.
type Sessions struct {
	db   *gorm.DB
	svc  session.Service
	pool *pgxpool.Pool
}

// OpenSessions opens ADK's session store on its own pool. Tables live in the
// adk schema and transactions are repeatable read unless one asks otherwise:
// at read committed two writers of one session lose state silently.
func OpenSessions(ctx context.Context, dsn string) (*Sessions, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	cfg.MaxConns, cfg.MinConns = maxConns, 0
	cfg.MaxConnIdleTime = min(cfg.MaxConnIdleTime, maxConnIdleTime)
	// ADK's unqualified table names resolve to its schema. The connection's
	// own path, from the DSN, role or database, follows so our statements on
	// this pool still reach the insights tables.
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SELECT set_config('search_path', $1 || ', ' || current_setting('search_path'), false)`, schema)
		return err
	}
	params := cfg.ConnConfig.RuntimeParams
	params["default_transaction_isolation"] = "repeatable read"
	params["application_name"] = "odigos-insights-adk"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open adk pool: %w", err)
	}
	// gorm's own ping takes no context, so an unreachable server would block
	// until the OS gives up on the connect.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("open adk pool: %w", err)
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: stdlib.OpenDBFromPool(pool)}), &gorm.Config{
		Logger:               logger.Discard,
		DisableAutomaticPing: true,
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("open adk session store: %w", err)
	}
	svc, err := database.NewSessionServiceFromDB(db)
	if err != nil {
		closeDB(db)
		pool.Close()
		return nil, fmt.Errorf("open adk session store: %w", err)
	}
	return &Sessions{db: db, svc: svc, pool: pool}, nil
}

// Close releases the pool.
func (s *Sessions) Close() {
	closeDB(s.db)
	s.pool.Close()
}

// Service is ADK's session service on this store.
func (s *Sessions) Service() session.Service {
	return s.svc
}

func closeDB(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}
