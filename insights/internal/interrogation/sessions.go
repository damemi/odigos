package interrogation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/adk/v2/platform"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/database"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/odigos-io/odigos/insights/internal/repo"
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

const (
	// AppName is the ADK app of a run's session, ControlAppName that of its
	// control session. Both sessions of a run have the run's id as their user
	// and session id.
	AppName        = "interrogation"
	ControlAppName = "interrogation.ctl"

	// openWorkloadIndex is the unique index of migration 000004 on the
	// workload of every open run.
	openWorkloadIndex = "interrogation_open_workload_idx"

	// writeAttempts bounds how often a write refused by Postgres as a
	// serialization failure is tried.
	writeAttempts = 5

	eventAuthor = "insights"

	// MaxOpen is how many runs may be open at once.
	MaxOpen = 10
	// MaxPerDay is how many runs created in the last 24 hours may still
	// exist when another starts. It bounds the runs that called the model.
	// A run closed before a leader picked it up counts only until retention
	// deletes it, an hour after it closed.
	MaxPerDay = 200
)

// What a stop does with the run's temporary probes.
const (
	ChangesKeep   = "keep"
	ChangesRevert = "revert"
)

var (
	// ErrSourceBusy means the workload already has an open interrogation.
	ErrSourceBusy = errors.New("workload has an open interrogation")
	// ErrAtCapacity means MaxOpen runs are open.
	ErrAtCapacity = errors.New("too many open interrogations")
	// ErrDailyLimit means MaxPerDay runs created in the last 24 hours exist.
	ErrDailyLimit = errors.New("too many recent interrogations")
	// ErrNotRunning means the interrogation has ended, has a stop recorded,
	// or is past its deadline.
	ErrNotRunning = errors.New("interrogation is not running")
	// ErrOverTotal means an extension would take the run's time limit over
	// the most allowed.
	ErrOverTotal = errors.New("interrogation time limit over the total allowed")
)

// Stop is a stop as a run's control session records it. Changes is
// ChangesKeep or ChangesRevert.
type Stop = repo.InterrogationStop

// Closed is an ending the workflow did not record itself, stored under the
// run session's key "closed".
type Closed = repo.InterrogationClosed

// control is the state of a run's control session.
type control = repo.InterrogationControl

// runSession is what the closes read of a run's session state. Run is left
// undecoded: a run whose state a newer binary wrote, met after a rollback,
// must still be closed.
type runSession struct {
	Open bool `json:"open"`
	// Run is the workflow's state, absent while the run is queued.
	Run json.RawMessage `json:"run"`
}

// runDeadline is what Extend reads of a run's session state.
type runDeadline struct {
	Open    bool                      `json:"open"`
	Request repo.InterrogationRequest `json:"request"`
	Run     *struct {
		StartedAt time.Time `json:"started_at"`
	} `json:"run"`
}

// Sessions is ADK's session store on the insights Postgres database. Its
// methods are every write of ours to a run's sessions; the workflow's runner
// writes the run's session through Service.
type Sessions struct {
	db   *gorm.DB
	svc  session.Service
	pool *pgxpool.Pool
	// maxOpen and maxPerDay are MaxOpen and MaxPerDay; tests of other
	// behaviour lift them.
	maxOpen, maxPerDay int
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
	return &Sessions{db: db, svc: svc, pool: pool, maxOpen: MaxOpen, maxPerDay: MaxPerDay}, nil
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

// Start stores a new run, queued: its control session, then its session with
// the workload, open and the request, in one transaction. When MaxPerDay runs
// created in the last 24 hours exist it fails with ErrDailyLimit, when MaxOpen
// runs are open with ErrAtCapacity, and when the workload has an open run with
// ErrSourceBusy; each time it stores neither session.
func (s *Sessions) Start(ctx context.Context, id string, req repo.InterrogationRequest) error {
	// Starts take one lock in turn, and read committed lets a start count the
	// runs that others committed while it waited for the lock.
	readCommitted := &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	err := s.inTx(ctx, readCommitted, func(tx *gorm.DB, svc session.Service) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('interrogation.start'))`).Error; err != nil {
			return err
		}
		var day, open int
		if err := tx.Raw(`SELECT count(*) FROM adk.sessions WHERE app_name = ? AND create_time > now() - interval '24 hours'`,
			AppName).Row().Scan(&day); err != nil {
			return err
		}
		if day >= s.maxPerDay {
			return ErrDailyLimit
		}
		if err := tx.Raw(`SELECT count(*) FROM adk.sessions WHERE app_name = ? AND state->>'open' = 'true'`,
			AppName).Row().Scan(&open); err != nil {
			return err
		}
		if open >= s.maxOpen {
			return ErrAtCapacity
		}
		// The control session exists from the start because the extend and
		// the probe insert lock its row.
		if _, err := svc.Create(ctx, &session.CreateRequest{AppName: ControlAppName, UserID: id, SessionID: id}); err != nil {
			return err
		}
		_, err := svc.Create(ctx, &session.CreateRequest{
			AppName: AppName, UserID: id, SessionID: id,
			State: map[string]any{"workload": req.Source.Workload(), "open": true, "request": req},
		})
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == openWorkloadIndex {
		return fmt.Errorf("%w: %w", ErrSourceBusy, err)
	}
	if err != nil {
		return fmt.Errorf("start interrogation %s: %w", id, err)
	}
	return nil
}

// CloseQueued closes a run no leader has picked up: one that is open and has
// no workflow state. It reports false when the run is not queued; the caller
// then reads the run again and answers from what it finds.
func (s *Sessions) CloseQueued(ctx context.Context, id string, c Closed) (bool, error) {
	var closed bool
	err := s.inTx(ctx, nil, func(_ *gorm.DB, svc session.Service) error {
		closed = false
		sess, err := getSession(ctx, svc, AppName, id)
		if err != nil {
			return err
		}
		var run runSession
		if err := decodeState(sess, &run); err != nil {
			return err
		}
		if !run.Open || run.Run != nil {
			return nil
		}
		if err := appendState(ctx, svc, sess, map[string]any{"open": false, "closed": c}); err != nil {
			return err
		}
		closed = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("close queued interrogation %s: %w", id, err)
	}
	return closed, nil
}

// Stop records stop in the run's control session, whatever state the run is
// in. The first stop keeps its time and actor; a revert overrides a keep, and
// not the reverse.
func (s *Sessions) Stop(ctx context.Context, id string, stop Stop) error {
	err := s.inTx(ctx, nil, func(_ *gorm.DB, svc session.Service) error {
		sess, err := getSession(ctx, svc, ControlAppName, id)
		if err != nil {
			return err
		}
		var cur control
		if err := decodeState(sess, &cur); err != nil {
			return err
		}
		next := stop
		switch {
		case cur.Stop == nil:
		case stop.Changes == ChangesRevert && cur.Stop.Changes != ChangesRevert:
			next.Actor, next.At = cur.Stop.Actor, cur.Stop.At
		default:
			return nil
		}
		return appendState(ctx, svc, sess, map[string]any{"stop": next})
	})
	if err != nil {
		return fmt.Errorf("stop interrogation %s: %w", id, err)
	}
	return nil
}

// Extend adds seconds to an open run's time limit in its control session,
// and in the same transaction moves the expiry of the run's temporary probes
// to the new deadline, so the two never disagree. It fails with
// ErrNotRunning when the run is closed, has a stop recorded or is past its
// deadline at now, and with ErrOverTotal when the limit with every extension
// would exceed maxTotal seconds.
func (s *Sessions) Extend(ctx context.Context, id string, seconds, maxTotal int, now time.Time) error {
	// Read committed, so the probe update sees a probe committed after the
	// transaction began. The row locks, taken in the order the probe insert
	// takes them, replace the snapshot check repeatable read would give.
	readCommitted := &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	err := s.inTx(ctx, readCommitted, func(tx *gorm.DB, svc session.Service) error {
		var raw []byte
		err := tx.Raw(`SELECT state FROM adk.sessions WHERE app_name = ? AND user_id = ? AND id = ? FOR UPDATE`,
			AppName, id, id).Row().Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %q", session.ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		var run runDeadline
		if err := json.Unmarshal(raw, &run); err != nil {
			return err
		}
		if !run.Open {
			return ErrNotRunning
		}
		if err := tx.Exec(`SELECT 1 FROM adk.sessions WHERE app_name = ? AND user_id = ? AND id = ? FOR UPDATE`,
			ControlAppName, id, id).Error; err != nil {
			return err
		}

		sess, err := getSession(ctx, svc, ControlAppName, id)
		if err != nil {
			return err
		}
		var cur control
		if err := decodeState(sess, &cur); err != nil {
			return err
		}
		if cur.Stop != nil {
			return ErrNotRunning
		}
		limit := run.Request.Budget.TimeLimitSeconds
		// A run that has not started, or has no limit, has no deadline yet and
		// so neither do its probes.
		var deadline time.Time
		if run.Run != nil && !run.Run.StartedAt.IsZero() && limit > 0 {
			if !run.Run.StartedAt.Add(time.Duration(limit+cur.ExtendSeconds) * time.Second).After(now) {
				return ErrNotRunning
			}
			deadline = run.Run.StartedAt.Add(time.Duration(limit+cur.ExtendSeconds+seconds) * time.Second)
		}
		if limit+cur.ExtendSeconds+seconds > maxTotal {
			return ErrOverTotal
		}
		if err := appendState(ctx, svc, sess, map[string]any{"extend_seconds": cur.ExtendSeconds + seconds}); err != nil {
			return err
		}
		if deadline.IsZero() {
			return nil
		}
		return tx.Exec(`
UPDATE interrogation_requested_probes
SET expires_at = LEAST(?::timestamptz, ?::timestamptz + interval '24 hours'), updated_at = ?
WHERE interrogation_id = ? AND state = 'approved' AND expires_at > ?`,
			deadline, now, now, id, now).Error
	})
	if err != nil {
		return fmt.Errorf("extend interrogation %s: %w", id, err)
	}
	return nil
}

// Control reads the stop and extension a run's control session holds now.
func (s *Sessions) Control(ctx context.Context, id string) (Control, error) {
	sess, err := getSession(ctx, s.svc, ControlAppName, id)
	if err != nil {
		return Control{}, fmt.Errorf("read control of interrogation %s: %w", id, err)
	}
	var cur control
	if err := decodeState(sess, &cur); err != nil {
		return Control{}, fmt.Errorf("read control of interrogation %s: %w", id, err)
	}
	return Control{Stop: cur.Stop, ExtendedSeconds: cur.ExtendSeconds}, nil
}

// CloseOverdue closes an open run the workflow could not close: one event
// that clears open and records c. A closed run is left as it is. When ADK
// cannot load the session any more, nothing can be appended to it, and the
// same change is made with one UPDATE of its row.
func (s *Sessions) CloseOverdue(ctx context.Context, id string, c Closed) error {
	var readErr error
	err := s.inTx(ctx, nil, func(_ *gorm.DB, svc session.Service) error {
		sess, err := getSession(ctx, svc, AppName, id)
		if err != nil {
			readErr = err
			return err
		}
		readErr = nil
		var run runSession
		if err := decodeState(sess, &run); err != nil {
			return err
		}
		if !run.Open {
			return nil
		}
		return appendState(ctx, svc, sess, map[string]any{"open": false, "closed": c})
	})
	if err == nil {
		return nil
	}
	if readErr == nil || !undecodable(ctx, readErr) {
		return fmt.Errorf("close overdue interrogation %s: %w", id, err)
	}

	closed, err := json.Marshal(c)
	if err != nil {
		return err
	}
	// update_time moves forward so that every copy a runner still holds is
	// refused as stale.
	if err := s.db.WithContext(ctx).Exec(`
UPDATE adk.sessions
SET state = state || jsonb_build_object('open', false, 'closed', ?::jsonb),
	update_time = greatest(update_time + interval '1 microsecond', ?)
WHERE app_name = ? AND user_id = ? AND id = ? AND state->>'open' = 'true'`,
		string(closed), platform.Now(ctx), AppName, id, id).Error; err != nil {
		return fmt.Errorf("close unreadable interrogation %s: %w", id, err)
	}
	return nil
}

// inTx runs fn in one transaction on the ADK pool, with a session service on
// that transaction. A read and an append in one transaction make Postgres
// refuse a writer whose read another write overtook (SQLSTATE 40001 at
// repeatable read), which ADK's own check, comparing writers' wall clocks,
// misses. fn runs again on that refusal only, with jittered backoff.
func (s *Sessions) inTx(ctx context.Context, opts *sql.TxOptions, fn func(tx *gorm.DB, svc session.Service) error) error {
	for attempt := 1; ; attempt++ {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			svc, err := database.NewSessionServiceFromDB(tx)
			if err != nil {
				return err
			}
			return fn(tx, svc)
		}, opts)
		if attempt == writeAttempts || !serializationFailure(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(rand.N(time.Duration(attempt) * 20 * time.Millisecond)):
		}
	}
}

// appendState appends an event that sets delta in sess, a copy read in the
// same transaction. ADK refuses an append when the stored update time is
// later than the appender's copy, and takes update times from each writer's
// clock, so the event is stamped after the copy: whoever holds an older copy
// is refused whatever its clock says.
func appendState(ctx context.Context, svc session.Service, sess session.Session, delta map[string]any) error {
	ev := session.NewEvent(ctx, platform.NewUUID(ctx))
	ev.Author = eventAuthor
	if after := sess.LastUpdateTime().Add(time.Microsecond); ev.Timestamp.Before(after) {
		ev.Timestamp = after
	}
	ev.Actions.StateDelta = delta
	return svc.AppendEvent(ctx, sess, ev)
}

// getSession reads a session with its last event only: the writes here need
// its state, which ADK keeps on the session row.
func getSession(ctx context.Context, svc session.Service, app, id string) (session.Session, error) {
	got, err := svc.Get(ctx, &session.GetRequest{AppName: app, UserID: id, SessionID: id, NumRecentEvents: 1})
	if err != nil {
		return nil, err
	}
	return got.Session, nil
}

// decodeState decodes the state of sess into v by the keys' JSON names.
func decodeState(sess session.Session, v any) error {
	b, err := json.Marshal(maps.Collect(sess.State().All()))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func serializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40001"
}

// undecodable reports whether a failed read of a session failed on what is
// stored rather than on reaching it.
func undecodable(ctx context.Context, err error) bool {
	var pgErr *pgconn.PgError
	return ctx.Err() == nil && !errors.Is(err, session.ErrNotFound) && !errors.As(err, &pgErr)
}

func closeDB(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}
