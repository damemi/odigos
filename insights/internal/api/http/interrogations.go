package http

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/adk/v2/session"

	"github.com/odigos-io/odigos/insights/internal/api/http/apimap"
	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/interrogation"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// Limits and defaults of interrogations; GET /interrogations/options serves
// them to the start form.
const (
	interrogationMaxOpen          = interrogation.MaxOpen
	interrogationMaxPerDay        = interrogation.MaxPerDay
	interrogationMaxRounds        = 5
	interrogationMaxTransactions  = 20
	interrogationDefaultProbes    = 5
	interrogationMaxProbes        = 20
	interrogationDefaultTimeLimit = 3600
	interrogationMinTimeLimit     = 300
	interrogationMaxTimeLimit     = 14400
	interrogationDefaultExtend    = 1800
	interrogationMinExtend        = 60
	interrogationMaxExtend        = 14400
	interrogationMaxTotalTime     = 28800
	interrogationMaxQuestion      = 2000
	interrogationMaxActor         = 200
	// interrogationQueueTimeout is how long a run may wait for a leader to
	// pick it up before the next start on its workload takes the workload.
	interrogationQueueTimeout = 5 * time.Minute
	// defaultMaxProbesPerWorkload is the loop's cap when none is configured.
	defaultMaxProbesPerWorkload = 20

	defaultInterrogationLimit       = 50
	maxInterrogationLimit           = 200
	defaultInterrogationReadLimit   = 200
	maxInterrogationReadLimit       = 1000
	interrogationCapacityRetryAfter = "30"
	// maxInterrogationBody is far above the largest valid request: a 2000
	// character question, 20 transaction ids and an actor.
	maxInterrogationBody = 64 << 10

	goalObservability = "observability"
	goalSecurity      = "security"
	changeKindProbes  = "custom_instrumentation"
)

// Values of details.reason in an interrogation error.
const (
	reasonSourceBusy      = "source_busy"
	reasonAtCapacity      = "at_capacity"
	reasonNotEnabled      = "not_enabled"
	reasonNotReady        = "not_ready"
	reasonNotSupportedYet = "not_supported_yet"
	reasonNoEvidence      = "no_evidence"
	reasonInvalidArgument = "invalid_argument"
	reasonNotRunning      = "not_running"
)

// interrogationOption is a value of an option set: whether this build has
// its behaviour, and whether the start form preselects it.
type interrogationOption struct {
	value     string
	supported bool
	def       bool
}

var (
	interrogationGoals = []interrogationOption{
		{value: goalObservability, supported: true, def: true},
		{value: goalSecurity},
	}
	interrogationChangeKinds = []interrogationOption{
		{value: changeKindProbes, supported: true, def: true},
		{value: "head_sampling"},
		{value: "payload_collection"},
		{value: "kernel_spans"},
	}
)

func findOption(opts []interrogationOption, value string) (interrogationOption, bool) {
	i := slices.IndexFunc(opts, func(o interrogationOption) bool { return o.value == value })
	if i < 0 {
		return interrogationOption{}, false
	}
	return opts[i], true
}

// InterrogationSessions writes a run's sessions; *interrogation.Sessions in
// the process.
type InterrogationSessions interface {
	Start(ctx context.Context, id string, req repo.InterrogationRequest) error
	CloseQueued(ctx context.Context, id string, c interrogation.Closed) (bool, error)
	Stop(ctx context.Context, id string, stop interrogation.Stop) error
	Extend(ctx context.Context, id string, seconds, maxTotal int, now time.Time) error
}

// InterrogationsConfig is what the interrogation endpoints need from the
// process.
type InterrogationsConfig struct {
	// Enabled follows the autopilot feature: interrogations use its model
	// gateway and its rules.
	Enabled bool
	// MaxProbesPerWorkload is the loop's per-workload probe cap; zero means
	// the loop's default.
	MaxProbesPerWorkload int
	// Sessions is the store of runs. Until it is set, the writes of a run
	// answer 503 not_ready.
	Sessions InterrogationSessions

	now func() time.Time
}

// SetInterrogations serves the interrogation endpoints. Until it is called
// they answer 503 not_ready; with Enabled false, 503 not_enabled.
func (a *API) SetInterrogations(cfg InterrogationsConfig) {
	if cfg.MaxProbesPerWorkload < 1 {
		cfg.MaxProbesPerWorkload = defaultMaxProbesPerWorkload
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	a.interrogations.Store(&cfg)
}

// interrogationError is an error the API reports with details.reason, so a
// client can tell causes apart that share an HTTP status.
type interrogationError struct {
	status  int
	reason  string
	details map[string]any
	err     error
}

func (e *interrogationError) Error() string { return e.err.Error() }
func (e *interrogationError) Unwrap() error { return e.err }

var (
	errInterrogationNotFound = errors.New("interrogation not found")

	errInterrogationsNotEnabled = &interrogationError{
		status:  http.StatusServiceUnavailable,
		reason:  reasonNotEnabled,
		details: map[string]any{"requires": "insights.autopilot.enabled"},
		err:     errors.New("interrogations need insights.autopilot.enabled=true"),
	}
)

// notReady is a failure a retry is expected to get past: the store is not
// open or its tables are not migrated yet, or a write kept losing to another.
func notReady(err error) *interrogationError {
	return &interrogationError{
		status: http.StatusServiceUnavailable,
		reason: reasonNotReady,
		err:    fmt.Errorf("interrogations are not ready, try again shortly: %w", err),
	}
}

func invalidArgument(field, format string, a ...any) *interrogationError {
	return &interrogationError{
		status:  http.StatusUnprocessableEntity,
		reason:  reasonInvalidArgument,
		details: map[string]any{"field": field},
		err:     fmt.Errorf(format, a...),
	}
}

// invalidValue is invalidArgument for one element of a list.
func invalidValue(field, value, format string, a ...any) *interrogationError {
	err := invalidArgument(field, format, a...)
	err.details["value"] = value
	return err
}

// notSupported names an option that is in the contract and has no behaviour
// yet.
func notSupported(option string) *interrogationError {
	return &interrogationError{
		status:  http.StatusUnprocessableEntity,
		reason:  reasonNotSupportedYet,
		details: map[string]any{"option": option},
		err:     fmt.Errorf("%s is not supported yet", option),
	}
}

// storeUnavailable reports errors that a retry gets past: the tables are not
// migrated yet (42P01 undefined table, 3F000 undefined schema), or a session
// write was refused as a serialization failure on every attempt.
func storeUnavailable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "42P01", "3F000", "40001":
		return true
	}
	return false
}

// writeRefused reports a session write that kept losing to another writer.
func writeRefused(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40001"
}

// writeInterrogationError maps interrogation service errors to HTTP statuses,
// with details.reason where the status alone does not say what happened.
func writeInterrogationError(c *gin.Context, err error) {
	var ie *interrogationError
	switch {
	case errors.As(err, &ie):
	case errors.Is(err, errInterrogationNotFound):
		writeError(c, http.StatusNotFound, err)
		return
	case storeUnavailable(err):
		ie = notReady(err)
	default:
		writeAPIError(c, err)
		return
	}
	details := map[string]any{"reason": ie.reason}
	maps.Copy(details, ie.details)
	if ie.reason == reasonAtCapacity {
		c.Header("Retry-After", interrogationCapacityRetryAfter)
	}
	c.JSON(ie.status, apitypes.ErrorResponse{Error: apitypes.ErrorBody{
		Code:    errorCode(ie.status),
		Message: ie.Error(),
		Details: details,
	}})
}

// interrogationsOrErr returns the configuration when interrogations are
// served.
func (a *API) interrogationsOrErr() (*InterrogationsConfig, error) {
	cfg := a.interrogations.Load()
	switch {
	case cfg == nil:
		return nil, notReady(errors.New("not configured yet"))
	case !cfg.Enabled:
		return nil, errInterrogationsNotEnabled
	}
	return cfg, nil
}

// interrogationWriterOrErr is interrogationsOrErr for the endpoints that
// write a run's sessions.
func (a *API) interrogationWriterOrErr() (*InterrogationsConfig, error) {
	cfg, err := a.interrogationsOrErr()
	if err != nil {
		return nil, err
	}
	if cfg.Sessions == nil {
		return nil, notReady(errors.New("the session store is not open"))
	}
	return cfg, nil
}

// ---------- runs ----------

// InterrogationOptions describes the start form: option sets, defaults and
// limits. It is served whether or not interrogations are enabled.
func (a *API) InterrogationOptions() apitypes.InterrogationOptions {
	values := func(opts []interrogationOption) []apitypes.InterrogationOptionValue {
		out := make([]apitypes.InterrogationOptionValue, len(opts))
		for i, o := range opts {
			out[i] = apitypes.InterrogationOptionValue{Value: o.value, Supported: o.supported, Default: o.def}
		}
		return out
	}
	intp := func(v int) *int { return &v }
	var out apitypes.InterrogationOptions
	cfg := a.interrogations.Load()
	out.Enabled = cfg != nil && cfg.Enabled
	out.Goals = values(interrogationGoals)
	out.ChangeKinds = values(interrogationChangeKinds)
	out.Budget.MaxProbes = apitypes.InterrogationBudgetOption{
		Supported: true, Default: intp(interrogationDefaultProbes), Min: intp(1), Max: intp(interrogationMaxProbes), Choices: []int{},
	}
	out.Budget.MaxAddedSpansPerMin = apitypes.InterrogationBudgetOption{Choices: []int{500, 1000, 5000}}
	out.Budget.TimeLimitSeconds = apitypes.InterrogationBudgetOption{
		Supported: true, Default: intp(interrogationDefaultTimeLimit),
		Min: intp(interrogationMinTimeLimit), Max: intp(interrogationMaxTimeLimit), Choices: []int{900, 3600, 14400},
	}
	out.MaxRounds = interrogationMaxRounds
	out.Extend.DefaultSeconds = interrogationDefaultExtend
	out.Extend.MaxTotalSeconds = interrogationMaxTotalTime
	out.MaxOpen = interrogationMaxOpen
	out.MaxTransactionIds = interrogationMaxTransactions
	return out
}

// ListInterrogations returns a page of runs, newest first, starting after
// the run with id before when it is set.
func (a *API) ListInterrogations(ctx context.Context, before string, limit int) (apitypes.InterrogationList, error) {
	cfg, err := a.interrogationsOrErr()
	if err != nil {
		return apitypes.InterrogationList{}, err
	}
	if before != "" && checkInterrogationID(before) != nil {
		return apitypes.InterrogationList{}, fmt.Errorf("%w: before must be an interrogation id", errBadRequest)
	}
	now := cfg.now()
	runs, err := a.Repo.ListInterrogations(ctx, before, clampTo(limit, defaultInterrogationLimit, maxInterrogationLimit), now)
	if err != nil {
		return apitypes.InterrogationList{}, err
	}
	return apimap.InterrogationList(runs, now)
}

// GetInterrogation returns one run with its rounds, answer and changes.
func (a *API) GetInterrogation(ctx context.Context, id string) (apitypes.Interrogation, error) {
	cfg, err := a.interrogationsOrErr()
	if err != nil {
		return apitypes.Interrogation{}, err
	}
	if err := checkInterrogationID(id); err != nil {
		return apitypes.Interrogation{}, err
	}
	return a.readInterrogation(ctx, cfg, id)
}

// checkInterrogationID answers an id that is not a UUID as not found: no run
// has one, and Postgres refuses some such strings with an error.
func checkInterrogationID(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return errInterrogationNotFound
	}
	return nil
}

func (a *API) readInterrogation(ctx context.Context, cfg *InterrogationsConfig, id string) (apitypes.Interrogation, error) {
	now := cfg.now()
	in, err := a.loadInterrogation(ctx, id, now)
	if err != nil {
		return apitypes.Interrogation{}, err
	}
	return a.interrogationDetail(ctx, in, now)
}

// interrogationDetail maps a run for the detail read, with its changes.
func (a *API) interrogationDetail(ctx context.Context, in repo.Interrogation, now time.Time) (apitypes.Interrogation, error) {
	changes, err := a.Repo.ListInterrogationChanges(ctx, in.ID)
	if err != nil {
		return apitypes.Interrogation{}, err
	}
	return apimap.FromInterrogation(in, changes, true, now)
}

func (a *API) loadInterrogation(ctx context.Context, id string, now time.Time) (repo.Interrogation, error) {
	in, ok, err := a.Repo.GetInterrogation(ctx, id, now)
	if err != nil {
		return repo.Interrogation{}, err
	}
	if !ok {
		return repo.Interrogation{}, errInterrogationNotFound
	}
	return in, nil
}

// StartInterrogation validates a start request, applies its defaults and
// stores the run, queued for the leader to pick up.
func (a *API) StartInterrogation(ctx context.Context, body apitypes.InterrogationStart) (apitypes.Interrogation, error) {
	cfg, err := a.interrogationWriterOrErr()
	if err != nil {
		return apitypes.Interrogation{}, err
	}
	req, err := interrogationRequest(body)
	if err != nil {
		return apitypes.Interrogation{}, err
	}
	if err := a.checkInterrogationEvidence(ctx, req); err != nil {
		return apitypes.Interrogation{}, err
	}
	id := uuid.NewString()
	if err := a.createInterrogation(ctx, cfg, id, req); err != nil {
		return apitypes.Interrogation{}, err
	}
	return a.readInterrogation(ctx, cfg, id)
}

// createInterrogation stores the run. A workload held by a run that has
// waited longer than interrogationQueueTimeout for a leader is taken from
// it: that run is closed as interrupted and the start is tried once more.
// Taking it leaves the open count as it is, so this holds at capacity too,
// and is how such runs end when there is no leader.
func (a *API) createInterrogation(ctx context.Context, cfg *InterrogationsConfig, id string, req repo.InterrogationRequest) error {
	workload := req.Source.Workload()
	for retried := false; ; retried = true {
		err := cfg.Sessions.Start(ctx, id, req)
		if errors.Is(err, interrogation.ErrDailyLimit) {
			return &interrogationError{
				status:  http.StatusServiceUnavailable,
				reason:  reasonAtCapacity,
				details: map[string]any{"max_per_day": interrogationMaxPerDay},
				err:     fmt.Errorf("too many recent interrogations (at most %d a day); try again later", interrogationMaxPerDay),
			}
		}
		full := errors.Is(err, interrogation.ErrAtCapacity)
		if !full && !errors.Is(err, interrogation.ErrSourceBusy) {
			return err
		}
		holder, ok, stale, err := a.workloadHolder(ctx, cfg, workload)
		if err != nil {
			return err
		}
		switch {
		case full && (!stale || retried):
			return &interrogationError{
				status:  http.StatusServiceUnavailable,
				reason:  reasonAtCapacity,
				details: map[string]any{"open": interrogationMaxOpen, "max_open": interrogationMaxOpen},
				err:     fmt.Errorf("%d interrogations are already open; try again when one ends", interrogationMaxOpen),
			}
		case ok && (!stale || retried):
			return &interrogationError{
				status:  http.StatusConflict,
				reason:  reasonSourceBusy,
				details: map[string]any{"interrogation_id": holder.ID, "display_id": repo.Interrogation{ID: holder.ID}.DisplayID()},
				err:     fmt.Errorf("%s already has an open interrogation (%s)", workload, repo.Interrogation{ID: holder.ID}.DisplayID()),
			}
		case retried:
			// The run that held the workload ended between the two reads.
			return notReady(fmt.Errorf("%s changed hands while starting", workload))
		case stale:
			// Not closed means the leader picked the run up meanwhile; the next
			// start then finds it running.
			_, err := cfg.Sessions.CloseQueued(ctx, holder.ID, interrogation.Closed{
				Reason: repo.InterrogationInterrupted,
				Error:  fmt.Sprintf("was not started within %s", interrogationQueueTimeout),
				At:     cfg.now(),
			})
			if err != nil && !writeRefused(err) {
				return err
			}
		}
	}
}

// workloadHolder reads the run that holds workload. It is stale when it has
// waited longer than interrogationQueueTimeout for a leader.
func (a *API) workloadHolder(ctx context.Context, cfg *InterrogationsConfig, workload string) (repo.InterrogationHolder, bool, bool, error) {
	holder, ok, err := a.Repo.InterrogationWorkloadHolder(ctx, workload)
	if err != nil {
		return repo.InterrogationHolder{}, false, false, err
	}
	stale := ok && !holder.Started && cfg.now().Sub(holder.CreatedAt) >= interrogationQueueTimeout
	return holder, ok, stale, nil
}

// interrogationRequest validates a start request in field order, bounds
// before unsupported options, and applies the defaults.
func interrogationRequest(body apitypes.InterrogationStart) (repo.InterrogationRequest, error) {
	src := body.Source
	for _, f := range []struct{ field, value string }{
		{"source.namespace", src.Namespace}, {"source.kind", src.Kind}, {"source.name", src.Name},
	} {
		if strings.TrimSpace(f.value) == "" {
			return repo.InterrogationRequest{}, invalidArgument(f.field, "%s is required", f.field)
		}
		if err := checkNoNUL(f.field, f.value); err != nil {
			return repo.InterrogationRequest{}, err
		}
	}
	goal := cmp.Or(body.Goal, goalObservability)
	if _, ok := findOption(interrogationGoals, goal); !ok {
		return repo.InterrogationRequest{}, invalidArgument("goal", "unknown goal %q", goal)
	}
	if utf8.RuneCountInString(body.Question) > interrogationMaxQuestion {
		return repo.InterrogationRequest{}, invalidArgument("question", "question must be at most %d characters", interrogationMaxQuestion)
	}
	if err := checkNoNUL("question", body.Question); err != nil {
		return repo.InterrogationRequest{}, err
	}
	if len(body.TransactionIds) > interrogationMaxTransactions {
		return repo.InterrogationRequest{}, invalidArgument("transaction_ids", "transaction_ids may name at most %d transactions", interrogationMaxTransactions)
	}
	for i, id := range body.TransactionIds {
		if slices.Contains(body.TransactionIds[:i], id) {
			return repo.InterrogationRequest{}, invalidValue("transaction_ids", strconv.FormatInt(id, 10), "transaction %d is named more than once", id)
		}
	}
	for _, kind := range body.AllowChanges {
		if _, ok := findOption(interrogationChangeKinds, kind); !ok {
			return repo.InterrogationRequest{}, invalidValue("allow_changes", kind, "unknown change kind %q", kind)
		}
	}
	budget := body.Budget
	maxProbes := cmp.Or(budget.MaxProbes, interrogationDefaultProbes)
	if maxProbes < 1 || maxProbes > interrogationMaxProbes {
		return repo.InterrogationRequest{}, invalidArgument("budget.max_probes", "budget.max_probes must be between 1 and %d", interrogationMaxProbes)
	}
	if budget.MaxAddedSpansPerMin != nil && *budget.MaxAddedSpansPerMin < 1 {
		return repo.InterrogationRequest{}, invalidArgument("budget.max_added_spans_per_min", "budget.max_added_spans_per_min must be at least 1")
	}
	timeLimit := cmp.Or(budget.TimeLimitSeconds, interrogationDefaultTimeLimit)
	if timeLimit < interrogationMinTimeLimit || timeLimit > interrogationMaxTimeLimit {
		return repo.InterrogationRequest{}, invalidArgument("budget.time_limit_seconds", "budget.time_limit_seconds must be between %d and %d",
			interrogationMinTimeLimit, interrogationMaxTimeLimit)
	}
	if err := checkActor(body.Actor); err != nil {
		return repo.InterrogationRequest{}, err
	}

	if o, _ := findOption(interrogationGoals, goal); !o.supported {
		return repo.InterrogationRequest{}, notSupported("goal=" + goal)
	}
	for _, kind := range body.AllowChanges {
		if o, _ := findOption(interrogationChangeKinds, kind); !o.supported {
			return repo.InterrogationRequest{}, notSupported("allow_changes=" + kind)
		}
	}
	if budget.MaxAddedSpansPerMin != nil {
		return repo.InterrogationRequest{}, notSupported("budget.max_added_spans_per_min")
	}

	req := repo.InterrogationRequest{
		Source:         repo.InterrogationSource{Namespace: src.Namespace, Kind: src.Kind, Name: src.Name},
		Goal:           goal,
		Question:       body.Question,
		TransactionIDs: body.TransactionIds,
		AllowChanges:   body.AllowChanges,
		Budget:         repo.InterrogationBudget{MaxProbes: maxProbes, TimeLimitSeconds: timeLimit, MaxRounds: interrogationMaxRounds},
		Trigger:        repo.InterrogationTrigger{Kind: "manual", Actor: body.Actor},
	}
	// Stored as [] rather than null: the request is read back as it is.
	if req.TransactionIDs == nil {
		req.TransactionIDs = []int64{}
	}
	if req.AllowChanges == nil {
		req.AllowChanges = []string{}
	}
	return req, nil
}

func checkActor(actor string) error {
	if utf8.RuneCountInString(actor) > interrogationMaxActor {
		return invalidArgument("actor", "actor must be at most %d characters", interrogationMaxActor)
	}
	return checkNoNUL("actor", actor)
}

// checkNoNUL refuses a NUL character, which Postgres text and jsonb cannot
// store.
func checkNoNUL(field, value string) error {
	if strings.ContainsRune(value, 0) {
		return invalidArgument(field, "%s must not contain a NUL character", field)
	}
	return nil
}

// checkInterrogationEvidence refuses a source with no call graph, and a
// requested transaction that has none in the source.
func (a *API) checkInterrogationEvidence(ctx context.Context, req repo.InterrogationRequest) error {
	txs, err := a.Repo.ListInterrogationSourceTransactions(ctx, req.Source, maxInterrogationReadLimit)
	if err != nil {
		return err
	}
	if len(txs) == 0 {
		return &interrogationError{
			status: http.StatusUnprocessableEntity,
			reason: reasonNoEvidence,
			err: fmt.Errorf("no call-graph evidence for %s; only Java sources with profiled traffic can be interrogated",
				req.Source.Workload()),
		}
	}
	for _, id := range req.TransactionIDs {
		if !slices.ContainsFunc(txs, func(tx repo.InterrogationSourceTransaction) bool { return tx.TransactionID == id }) {
			return invalidValue("transaction_ids", strconv.FormatInt(id, 10), "transaction %d has no call graph in %s", id, req.Source.Workload())
		}
	}
	return nil
}

// StopInterrogation stops a run. A queued run is closed here, as stopped; a
// running one has the stop recorded for the leader, which ends it within
// seconds (202); a run that has ended keeps how it ended, and a revert still
// reverts its live changes (200).
func (a *API) StopInterrogation(ctx context.Context, id string, body apitypes.InterrogationStop) (apitypes.Interrogation, int, error) {
	cfg, err := a.interrogationWriterOrErr()
	if err != nil {
		return apitypes.Interrogation{}, 0, err
	}
	if err := checkInterrogationID(id); err != nil {
		return apitypes.Interrogation{}, 0, err
	}
	changes := cmp.Or(string(body.Changes), interrogation.ChangesKeep)
	if changes != interrogation.ChangesKeep && changes != interrogation.ChangesRevert {
		return apitypes.Interrogation{}, 0, invalidArgument("changes", "changes must be %q or %q", interrogation.ChangesKeep, interrogation.ChangesRevert)
	}
	if err := checkActor(body.Actor); err != nil {
		return apitypes.Interrogation{}, 0, err
	}
	now := cfg.now()
	in, err := a.loadInterrogation(ctx, id, now)
	if err != nil {
		return apitypes.Interrogation{}, 0, err
	}

	if in.Open && !in.Started {
		closed, err := cfg.Sessions.CloseQueued(ctx, id, interrogation.Closed{
			Reason: repo.InterrogationStoppedByUser, Actor: body.Actor, Changes: changes, At: now,
		})
		if err != nil && !writeRefused(err) {
			return apitypes.Interrogation{}, 0, err
		}
		if closed {
			out, err := a.readInterrogation(ctx, cfg, id)
			return out, http.StatusOK, err
		}
		if in, err = a.loadInterrogation(ctx, id, now); err != nil {
			return apitypes.Interrogation{}, 0, err
		}
	}

	// A run stopped while queued holds its first stop in closed, which a stop
	// recorded now would replace on the wire.
	if in.Closed == nil || in.Closed.Reason != repo.InterrogationStoppedByUser {
		if err := cfg.Sessions.Stop(ctx, id, interrogation.Stop{Changes: changes, Actor: body.Actor, At: now}); err != nil {
			if errors.Is(err, session.ErrNotFound) {
				return apitypes.Interrogation{}, 0, errInterrogationNotFound
			}
			if !writeRefused(err) {
				return apitypes.Interrogation{}, 0, err
			}
			// A stop that kept losing to other writes has done its job when a
			// stop is recorded or the run has ended anyway.
			in, rerr := a.loadInterrogation(ctx, id, now)
			if rerr != nil {
				return apitypes.Interrogation{}, 0, rerr
			}
			if in.Open && in.Control.Stop == nil {
				return apitypes.Interrogation{}, 0, notReady(err)
			}
		}
	}

	in, err = a.loadInterrogation(ctx, id, cfg.now())
	if err != nil {
		return apitypes.Interrogation{}, 0, err
	}
	if in.Open && !in.Started {
		// Its close lost to other writes and no leader has picked it up, so
		// nothing acts on the stop yet.
		return apitypes.Interrogation{}, 0, notReady(fmt.Errorf("interrogation %s is still queued", id))
	}
	if in.Open {
		// The run reverts its changes itself when it ends: it could still
		// request one after a revert made here.
		out, err := a.interrogationDetail(ctx, in, cfg.now())
		return out, http.StatusAccepted, err
	}
	if changes == interrogation.ChangesRevert || in.Control.Stop != nil && in.Control.Stop.Changes == interrogation.ChangesRevert {
		if err := a.Repo.RevertInterrogationProbes(ctx, id, cfg.now()); err != nil {
			return apitypes.Interrogation{}, 0, err
		}
	}
	out, err := a.readInterrogation(ctx, cfg, id)
	return out, http.StatusOK, err
}

// ExtendInterrogation moves an open run's deadline later, with the expiry of
// its live changes.
func (a *API) ExtendInterrogation(ctx context.Context, id string, body apitypes.InterrogationExtend) (apitypes.Interrogation, error) {
	cfg, err := a.interrogationWriterOrErr()
	if err != nil {
		return apitypes.Interrogation{}, err
	}
	if err := checkInterrogationID(id); err != nil {
		return apitypes.Interrogation{}, err
	}
	if body.Seconds < interrogationMinExtend || body.Seconds > interrogationMaxExtend {
		return apitypes.Interrogation{}, invalidArgument("seconds", "seconds must be between %d and %d", interrogationMinExtend, interrogationMaxExtend)
	}
	if err := checkActor(body.Actor); err != nil {
		return apitypes.Interrogation{}, err
	}
	err = cfg.Sessions.Extend(ctx, id, body.Seconds, interrogationMaxTotalTime, cfg.now())
	switch {
	case errors.Is(err, session.ErrNotFound):
		return apitypes.Interrogation{}, errInterrogationNotFound
	case errors.Is(err, interrogation.ErrNotRunning):
		return apitypes.Interrogation{}, notRunning(id)
	case errors.Is(err, interrogation.ErrOverTotal):
		return apitypes.Interrogation{}, invalidArgument("seconds", "the time limit with every extension cannot pass %ds", interrogationMaxTotalTime)
	case writeRefused(err):
		in, rerr := a.loadInterrogation(ctx, id, cfg.now())
		if rerr != nil {
			return apitypes.Interrogation{}, rerr
		}
		if !in.Open || in.Control.Stop != nil {
			return apitypes.Interrogation{}, notRunning(id)
		}
		return apitypes.Interrogation{}, notReady(err)
	case err != nil:
		return apitypes.Interrogation{}, err
	}
	return a.readInterrogation(ctx, cfg, id)
}

func notRunning(id string) *interrogationError {
	return &interrogationError{
		status: http.StatusConflict,
		reason: reasonNotRunning,
		err:    fmt.Errorf("interrogation %s has ended, is stopping, or is past its deadline", id),
	}
}

// InterrogationEvents returns a page of a run's event log after the given
// sequence.
func (a *API) InterrogationEvents(ctx context.Context, id string, after int64, limit int) (apitypes.InterrogationEventList, error) {
	if _, err := a.interrogationsOrErr(); err != nil {
		return apitypes.InterrogationEventList{}, err
	}
	if err := checkInterrogationID(id); err != nil {
		return apitypes.InterrogationEventList{}, err
	}
	page, ok, err := a.Repo.ListInterrogationEvents(ctx, id, after, clampTo(limit, defaultInterrogationReadLimit, maxInterrogationReadLimit))
	if err != nil {
		return apitypes.InterrogationEventList{}, err
	}
	if !ok {
		return apitypes.InterrogationEventList{}, errInterrogationNotFound
	}
	return apimap.InterrogationEventList(page)
}

// InterrogationSummary returns the list page's counters.
func (a *API) InterrogationSummary(ctx context.Context) (apitypes.InterrogationSummary, error) {
	cfg, err := a.interrogationsOrErr()
	if err != nil {
		return apitypes.InterrogationSummary{}, err
	}
	s, err := a.Repo.InterrogationSummary(ctx)
	if err != nil {
		return apitypes.InterrogationSummary{}, err
	}
	changes, err := a.Repo.InterrogationChangesSummary(ctx, cfg.now())
	if err != nil {
		return apitypes.InterrogationSummary{}, err
	}
	return apimap.FromInterrogationSummary(s, changes, cfg.MaxProbesPerWorkload), nil
}

// InterrogationSources lists the workloads that can be interrogated.
func (a *API) InterrogationSources(ctx context.Context, limit int) (apitypes.InterrogationSourceList, error) {
	cfg, err := a.interrogationsOrErr()
	if err != nil {
		return apitypes.InterrogationSourceList{}, err
	}
	sources, err := a.Repo.ListInterrogationSources(ctx, clampTo(limit, defaultInterrogationReadLimit, maxInterrogationReadLimit), cfg.now())
	if err != nil {
		return apitypes.InterrogationSourceList{}, err
	}
	return apimap.InterrogationSourceList(sources, cfg.MaxProbesPerWorkload), nil
}

// InterrogationSourceTransactions lists a workload's transactions with a call
// graph.
func (a *API) InterrogationSourceTransactions(ctx context.Context, params apitypes.ListInterrogationSourceTransactionsParams) (apitypes.InterrogationSourceTransactionList, error) {
	if _, err := a.interrogationsOrErr(); err != nil {
		return apitypes.InterrogationSourceTransactionList{}, err
	}
	if params.Namespace == "" || params.Kind == "" || params.Name == "" {
		return apitypes.InterrogationSourceTransactionList{}, fmt.Errorf("%w: namespace, kind and name are required", errBadRequest)
	}
	if strings.ContainsRune(params.Namespace+params.Kind+params.Name, 0) {
		return apitypes.InterrogationSourceTransactionList{}, fmt.Errorf("%w: namespace, kind and name must not contain a NUL character", errBadRequest)
	}
	src := repo.InterrogationSource{Namespace: params.Namespace, Kind: params.Kind, Name: params.Name}
	txs, err := a.Repo.ListInterrogationSourceTransactions(ctx, src, clampTo(params.Limit, defaultInterrogationReadLimit, maxInterrogationReadLimit))
	if err != nil {
		return apitypes.InterrogationSourceTransactionList{}, err
	}
	return apimap.InterrogationSourceTransactionList(txs), nil
}

func clampTo(limit, def, maxLimit int) int {
	switch {
	case limit <= 0:
		return def
	case limit > maxLimit:
		return maxLimit
	}
	return limit
}

// limitInterrogationBody bounds the body of an interrogation write, which
// gin would otherwise decode in full before any check.
func limitInterrogationBody(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxInterrogationBody)
}

// bindJSON reads a request body, answering 413 when it is over the limit.
func bindJSON(c *gin.Context, body any) bool {
	err := c.ShouldBindJSON(body)
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(c, http.StatusRequestEntityTooLarge, fmt.Errorf("request body is over %d bytes", tooLarge.Limit))
	} else {
		writeError(c, http.StatusBadRequest, err)
	}
	return false
}

// bindOptionalJSON reads a request body the operation does not require.
func bindOptionalJSON(c *gin.Context, body any) bool {
	return c.Request.ContentLength == 0 || bindJSON(c, body)
}

// ---------- gin handlers ----------

func (a *API) getInterrogationOptions(c *gin.Context) {
	c.JSON(http.StatusOK, a.InterrogationOptions())
}

func (a *API) listInterrogations(c *gin.Context) {
	var params apitypes.ListInterrogationsParams
	if err := c.ShouldBindQuery(&params); err != nil {
		writeBindError(c, err)
		return
	}
	out, err := a.ListInterrogations(c.Request.Context(), params.Before, params.Limit)
	if err != nil {
		writeInterrogationError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) startInterrogation(c *gin.Context) {
	var body apitypes.InterrogationStart
	if !bindJSON(c, &body) {
		return
	}
	out, err := a.StartInterrogation(c.Request.Context(), body)
	if err != nil {
		writeInterrogationError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (a *API) getInterrogationSummary(c *gin.Context) {
	out, err := a.InterrogationSummary(c.Request.Context())
	if err != nil {
		writeInterrogationError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) listInterrogationSources(c *gin.Context) {
	var params apitypes.ListInterrogationSourcesParams
	if err := c.ShouldBindQuery(&params); err != nil {
		writeBindError(c, err)
		return
	}
	out, err := a.InterrogationSources(c.Request.Context(), params.Limit)
	if err != nil {
		writeInterrogationError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) listInterrogationSourceTransactions(c *gin.Context) {
	var params apitypes.ListInterrogationSourceTransactionsParams
	if err := c.ShouldBindQuery(&params); err != nil {
		writeBindError(c, err)
		return
	}
	out, err := a.InterrogationSourceTransactions(c.Request.Context(), params)
	if err != nil {
		writeInterrogationError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) getInterrogation(c *gin.Context) {
	out, err := a.GetInterrogation(c.Request.Context(), c.Param("interrogation_id"))
	if err != nil {
		writeInterrogationError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) listInterrogationEvents(c *gin.Context) {
	var params apitypes.ListInterrogationEventsParams
	if err := c.ShouldBindQuery(&params); err != nil {
		writeBindError(c, err)
		return
	}
	if params.After < 0 {
		writeError(c, http.StatusBadRequest, errors.New("invalid query: after must not be negative"))
		return
	}
	out, err := a.InterrogationEvents(c.Request.Context(), c.Param("interrogation_id"), params.After, params.Limit)
	if err != nil {
		writeInterrogationError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) stopInterrogation(c *gin.Context) {
	var body apitypes.InterrogationStop
	if !bindOptionalJSON(c, &body) {
		return
	}
	out, status, err := a.StopInterrogation(c.Request.Context(), c.Param("interrogation_id"), body)
	if err != nil {
		writeInterrogationError(c, err)
		return
	}
	c.JSON(status, out)
}

func (a *API) extendInterrogation(c *gin.Context) {
	var body apitypes.InterrogationExtend
	if !bindJSON(c, &body) {
		return
	}
	out, err := a.ExtendInterrogation(c.Request.Context(), c.Param("interrogation_id"), body)
	if err != nil {
		writeInterrogationError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
