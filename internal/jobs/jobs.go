// SPDX-License-Identifier: AGPL-3.0-only

// Package jobs is the persistent task queue. Every long operation runs as a
// job with steps, progress and a log, survives restarts as "interrupted" and
// respects per-library locks.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// State of a job.
type State string

// States.
const (
	Queued      State = "queued"
	Running     State = "running"
	Succeeded   State = "succeeded"
	Failed      State = "failed"
	Cancelled   State = "cancelled"
	Interrupted State = "interrupted"
)

// Label returns the display label of a state.
func (s State) Label() string {
	switch s {
	case Queued:
		return "Queued"
	case Running:
		return "Running"
	case Succeeded:
		return "Succeeded"
	case Failed:
		return "Failed"
	case Cancelled:
		return "Cancelled"
	case Interrupted:
		return "Interrupted"
	}
	return string(s)
}

// Finished reports whether the state is final.
func (s State) Finished() bool { return s != Queued && s != Running }

func (s State) retryable() bool { return s == Failed || s == Cancelled || s == Interrupted }

// LockMode on the job's library.
type LockMode int

// Lock modes.
const (
	LockNone LockMode = iota
	LockRead
	LockWrite
)

// Spec defines a job type.
type Spec struct {
	Type        string
	Title       string
	Priority    int
	Heavy       bool
	Lock        LockMode
	Cancellable bool
	// Retryable marks idempotent, non-destructive types: Retry may queue them
	// again with their original parameters. Destructive types are started
	// again from the page that asks for their confirmation.
	Retryable bool
	// Action is the permission (an action name of internal/auth) needed to
	// cancel or retry a job of this type. Empty means administrators only.
	Action string
	Run    func(ctx context.Context, r *Run) error
}

// Job is a stored job.
type Job struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Title        string          `json:"title"`
	LibraryID    string          `json:"library_id,omitempty"`
	Params       json.RawMessage `json:"params"`
	State        State           `json:"state"`
	Priority     int             `json:"priority"`
	ScheduledFor time.Time       `json:"scheduled_for"`
	CreatedAt    time.Time       `json:"created_at"`
	StartedAt    *time.Time      `json:"started_at,omitempty"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
	Progress     int             `json:"progress"`
	CurrentStep  string          `json:"current_step"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorDetail  string          `json:"error_detail,omitempty"`
	CreatedBy    audit.Actor     `json:"created_by"`
	ScheduleID   string          `json:"schedule_id,omitempty"`
	Attempt      int             `json:"attempt"`
	Result       json.RawMessage `json:"result"`
	Cancellable  bool            `json:"cancellable"`
	Retryable    bool            `json:"retryable"`
}

// Duration of the run so far or in total.
func (j *Job) Duration() time.Duration {
	if j.StartedAt == nil {
		return 0
	}
	end := time.Now()
	if j.FinishedAt != nil {
		end = *j.FinishedAt
	}
	return end.Sub(*j.StartedAt)
}

// Step is a recorded step.
type Step struct {
	Seq        int             `json:"seq"`
	Name       string          `json:"name"`
	State      string          `json:"state"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Data       json.RawMessage `json:"data"`
}

// LogLine is a job log line.
type LogLine struct {
	Seq     int       `json:"seq"`
	TS      time.Time `json:"ts"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// Update is published on the bus while a job runs.
type Update struct {
	JobID    string   `json:"job_id"`
	State    State    `json:"state"`
	Progress int      `json:"progress"`
	Step     string   `json:"step"`
	Log      *LogLine `json:"log,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Manager runs jobs.
type Manager struct {
	db    *store.DB
	ev    *events.Recorder
	bus   *events.Bus
	log   *slog.Logger
	specs map[string]Spec

	workers int
	heavy   chan struct{}
	locks   *lockTable

	mu      sync.Mutex
	running map[string]*runningJob
	busy    int
	wake    chan struct{}
	stop    atomic.Bool
	wg      sync.WaitGroup

	// finishWaits are the pauses between attempts to record the final
	// state of a job.
	finishWaits []time.Duration
	// hookDispatch, when set, runs at the start of every dispatch (tests).
	hookDispatch func()
}

// dispatchPage is how many queued rows the dispatcher reads at a time.
const dispatchPage = 50

type runningJob struct {
	cancel      context.CancelCauseFunc
	cancellable bool
}

// ErrStopped is the cause given to the jobs that a graceful shutdown cuts
// off: they end as interrupted, like the ones a crash leaves behind, so they
// are reconciled and can be retried.
var ErrStopped = errors.New("SQS-J010 the process stopped while the job was running")

// NewManager creates a manager.
func NewManager(db *store.DB, ev *events.Recorder, bus *events.Bus, log *slog.Logger, workers, heavy int) *Manager {
	return &Manager{
		db: db, ev: ev, bus: bus, log: log,
		specs:   map[string]Spec{},
		workers: workers,
		heavy:   make(chan struct{}, heavy),
		locks:   newLockTable(),
		running: map[string]*runningJob{},
		wake:    make(chan struct{}, 1),

		finishWaits: []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 2 * time.Second, 5 * time.Second},
	}
}

// Register adds a job type.
func (m *Manager) Register(s Spec) {
	if s.Priority == 0 {
		s.Priority = 50
	}
	m.specs[s.Type] = s
}

// Spec returns the definition of a job type.
func (m *Manager) Spec(typ string) (Spec, bool) {
	s, ok := m.specs[typ]
	return s, ok
}

// Types lists the registered job types, sorted.
func (m *Manager) Types() []string {
	out := make([]string, 0, len(m.specs))
	for t := range m.specs {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Action returns the permission needed to cancel or retry a job of type
// typ, or "" when the type has none (administrators only).
func (m *Manager) Action(typ string) string { return m.specs[typ].Action }

// Title returns the label of a job type.
func (m *Manager) Title(typ string) string {
	if s, ok := m.specs[typ]; ok && s.Title != "" {
		return s.Title
	}
	return typ
}

// Option changes how a job is enqueued.
type Option func(*enqueueOpts)

type enqueueOpts struct {
	at         time.Time
	scheduleID string
	priority   int
	unique     bool
	attempt    int
}

// At delays a job.
func At(t time.Time) Option { return func(o *enqueueOpts) { o.at = t } }

// FromSchedule tags a job with its schedule.
func FromSchedule(id string) Option { return func(o *enqueueOpts) { o.scheduleID = id } }

// Priority overrides the default priority.
func Priority(p int) Option { return func(o *enqueueOpts) { o.priority = p } }

// Unique returns the existing queued or running job of the same type and
// library instead of creating another. A row left "running" that this
// process is not running (its final update failed) does not count.
func Unique() Option { return func(o *enqueueOpts) { o.unique = true } }

func attempt(n int) Option { return func(o *enqueueOpts) { o.attempt = n } }

// ErrUnknownType is returned for unregistered job types.
var ErrUnknownType = errors.New("SQS-J001 unknown job type")

// Enqueue stores a new job.
func (m *Manager) Enqueue(ctx context.Context, typ, libraryID string, params any, by audit.Actor, opts ...Option) (*Job, error) {
	spec, ok := m.specs[typ]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownType, typ)
	}
	o := enqueueOpts{at: time.Now(), priority: spec.Priority, attempt: 1}
	for _, f := range opts {
		f(&o)
	}
	if o.unique {
		id, err := m.pending(ctx, typ, libraryID)
		if err != nil {
			return nil, err
		}
		if id != "" {
			return m.Get(ctx, id)
		}
	}
	p, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if params == nil {
		p = []byte("{}")
	}
	id := ulid.New()
	now := store.NowT()
	_, err = m.db.ExecContext(ctx, `INSERT INTO jobs(id, type, library_id, params, state, priority, scheduled_for, created_at, created_by_type, created_by_id, created_by_name, schedule_id, attempt)
		VALUES (?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, typ, libraryID, string(p), o.priority, store.T(o.at), now, by.Type, by.ID, by.Name, o.scheduleID, o.attempt)
	if err != nil {
		return nil, err
	}
	m.Wake()
	m.bus.Publish("jobs", Update{JobID: id, State: Queued})
	return m.Get(ctx, id)
}

// pending returns the oldest queued job of the type and library, or a
// running one this process is actually running; "" when there is none.
func (m *Manager) pending(ctx context.Context, typ, libraryID string) (string, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id, state FROM jobs WHERE type = ? AND library_id = ? AND state IN ('queued', 'running') ORDER BY created_at`, typ, libraryID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return "", err
		}
		if State(state) == Queued || m.isRunning(id) {
			return id, nil
		}
	}
	return "", rows.Err()
}

func (m *Manager) isRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[id]
	return ok
}

// Wake nudges the dispatcher.
func (m *Manager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

const jobCols = `id, type, library_id, params, state, priority, scheduled_for, created_at, started_at, finished_at, progress, current_step,
	error_code, error_detail, created_by_type, created_by_id, created_by_name, schedule_id, attempt, result`

func (m *Manager) scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var params, result, sched, created string
	var started, finished sql.NullString
	var state string
	err := row.Scan(&j.ID, &j.Type, &j.LibraryID, &params, &state, &j.Priority, &sched, &created, &started, &finished, &j.Progress,
		&j.CurrentStep, &j.ErrorCode, &j.ErrorDetail, &j.CreatedBy.Type, &j.CreatedBy.ID, &j.CreatedBy.Name, &j.ScheduleID, &j.Attempt, &result)
	if err != nil {
		return nil, err
	}
	j.State = State(state)
	j.Params = json.RawMessage(params)
	j.Result = json.RawMessage(result)
	j.ScheduledFor = store.ParseT(sched)
	j.CreatedAt = store.ParseT(created)
	j.StartedAt = store.NullT(started)
	j.FinishedAt = store.NullT(finished)
	j.Title = m.Title(j.Type)
	if s, ok := m.specs[j.Type]; ok {
		j.Cancellable = s.Cancellable && !j.State.Finished()
		j.Retryable = s.Retryable && j.State.retryable()
	}
	return &j, nil
}

// ErrNotFound is returned for unknown jobs.
var ErrNotFound = errors.New("SQS-J002 job not found")

// Get returns one job.
func (m *Manager) Get(ctx context.Context, id string) (*Job, error) {
	j, err := m.scanJob(m.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return j, err
}

// Filter for List.
type Filter struct {
	States    []State
	Type      string
	LibraryID string
	Before    string
	Limit     int
}

// List returns jobs, newest first.
func (m *Manager) List(ctx context.Context, f Filter) ([]*Job, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	q := `SELECT ` + jobCols + ` FROM jobs WHERE 1=1`
	var args []any
	if len(f.States) > 0 {
		q += ` AND state IN (`
		for i, s := range f.States {
			if i > 0 {
				q += `,`
			}
			q += `?`
			args = append(args, string(s))
		}
		q += `)`
	}
	if f.Type != "" {
		q += ` AND type = ?`
		args = append(args, f.Type)
	}
	if f.LibraryID != "" {
		q += ` AND library_id = ?`
		args = append(args, f.LibraryID)
	}
	if f.Before != "" {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := m.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := m.scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Steps returns the steps of a job.
func (m *Manager) Steps(ctx context.Context, id string) ([]Step, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT seq, name, state, started_at, finished_at, data FROM job_steps WHERE job_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Step
	for rows.Next() {
		var s Step
		var started, data string
		var finished sql.NullString
		if err := rows.Scan(&s.Seq, &s.Name, &s.State, &started, &finished, &data); err != nil {
			return nil, err
		}
		s.StartedAt = store.ParseT(started)
		s.FinishedAt = store.NullT(finished)
		s.Data = json.RawMessage(data)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Logs returns log lines after seq.
func (m *Manager) Logs(ctx context.Context, id string, after int) ([]LogLine, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT seq, ts, level, message FROM job_logs WHERE job_id = ? AND seq > ? ORDER BY seq LIMIT 5000`, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var l LogLine
		var ts string
		if err := rows.Scan(&l.Seq, &ts, &l.Level, &l.Message); err != nil {
			return nil, err
		}
		l.TS = store.ParseT(ts)
		out = append(out, l)
	}
	return out, rows.Err()
}

// Counts returns the number of jobs per state among queued and running.
func (m *Manager) Counts(ctx context.Context) (queued, running int, err error) {
	err = m.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(state = 'queued'), 0), COALESCE(SUM(state = 'running'), 0) FROM jobs WHERE state IN ('queued', 'running')`).Scan(&queued, &running)
	return
}

// FailedSince counts failed jobs since t.
func (m *Manager) FailedSince(ctx context.Context, t time.Time) (int, error) {
	var n int
	err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE state = 'failed' AND finished_at >= ?`, store.T(t)).Scan(&n)
	return n, err
}

// ErrNotCancellable is returned when a running job cannot be cancelled.
var ErrNotCancellable = errors.New("SQS-J003 this job cannot be cancelled right now")

// Cancel stops a queued or cancellable running job.
func (m *Manager) Cancel(ctx context.Context, id string) error {
	res, err := m.db.ExecContext(ctx, `UPDATE jobs SET state = 'cancelled', finished_at = ? WHERE id = ? AND state = 'queued'`, store.NowT(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		m.bus.Publish("job:"+id, Update{JobID: id, State: Cancelled})
		m.bus.Publish("jobs", Update{JobID: id, State: Cancelled})
		return nil
	}
	m.mu.Lock()
	rj, ok := m.running[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if !rj.cancellable {
		return ErrNotCancellable
	}
	rj.cancel(context.Canceled)
	return nil
}

// ErrNotRetryable is returned when Retry is asked for a type that is not
// idempotent (docs 11 §11): its original confirmation and checks would be
// skipped.
var ErrNotRetryable = errors.New("SQS-J030 this type of job cannot be retried; start the operation again from its page, which asks for confirmation")

// Retry re-enqueues a failed, cancelled or interrupted job of a retryable
// type with its original parameters. The caller checks the permission of
// the type (Action) and records the audit entry.
func (m *Manager) Retry(ctx context.Context, id string, by audit.Actor) (*Job, error) {
	j, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	spec, ok := m.specs[j.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownType, j.Type)
	}
	if !spec.Retryable {
		return nil, ErrNotRetryable
	}
	if !j.State.retryable() {
		return nil, errors.New("SQS-J004 only failed, cancelled or interrupted jobs can be retried")
	}
	var params any
	if err := json.Unmarshal(j.Params, &params); err != nil {
		return nil, fmt.Errorf("SQS-J031 the stored parameters of job %s are not valid: %w", j.ID, err)
	}
	return m.Enqueue(ctx, j.Type, j.LibraryID, params, by, attempt(j.Attempt+1))
}

// MarkInterrupted turns running jobs left by a previous process into
// interrupted ones and returns them (for reconciliation).
func (m *Manager) MarkInterrupted(ctx context.Context) ([]*Job, error) {
	running, err := m.List(ctx, Filter{States: []State{Running}, Limit: 500})
	if err != nil {
		return nil, err
	}
	for _, j := range running {
		m.db.ExecContext(ctx, `UPDATE jobs SET state = 'interrupted', finished_at = ?, error_code = 'SQS-J010', error_detail = 'The process stopped while the job was running' WHERE id = ?`, store.NowT(), j.ID)
		m.db.ExecContext(ctx, `UPDATE job_steps SET state = 'interrupted', finished_at = ? WHERE job_id = ? AND state = 'running'`, store.NowT(), j.ID)
		m.ev.Emit(ctx, events.Event{Type: events.JobInterrupted, Severity: events.Warning, LibraryID: j.LibraryID, JobID: j.ID,
			Message: fmt.Sprintf("The job %q was interrupted by a restart", m.Title(j.Type)), Data: map[string]any{"job_type": j.Type}})
		j.State = Interrupted
	}
	return running, nil
}

// Purge deletes finished jobs older than before.
func (m *Manager) Purge(ctx context.Context, before time.Time) (int64, error) {
	res, err := m.db.ExecContext(ctx, `DELETE FROM jobs WHERE state IN ('succeeded', 'failed', 'cancelled', 'interrupted') AND finished_at < ?`, store.T(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Start runs the dispatcher until ctx ends.
func (m *Manager) Start(ctx context.Context) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.wake:
			case <-t.C:
			}
			if !m.stop.Load() {
				m.safeDispatch(ctx)
			}
		}
	}()
}

// safeDispatch runs one pass of the dispatcher; a panic is logged and
// recorded as a critical event, and the loop goes on.
func (m *Manager) safeDispatch(ctx context.Context) {
	defer m.recoverLoop("job dispatcher")
	if m.hookDispatch != nil {
		m.hookDispatch()
	}
	m.dispatch(ctx)
}

func (m *Manager) recoverLoop(name string) {
	p := recover()
	if p == nil {
		return
	}
	m.log.Error("panic in a background loop", "loop", name, "panic", p, "stack", string(debug.Stack()))
	m.ev.Emit(context.Background(), events.Event{Type: events.SystemInternalError, Severity: events.Critical,
		Message: fmt.Sprintf("Internal error in the %s (it keeps running): %v", name, p), Data: map[string]any{"loop": name}})
}

// Shutdown stops dispatching, cancels cancellable jobs and waits up to
// timeout for the rest. The jobs it cancels end as interrupted.
func (m *Manager) Shutdown(timeout time.Duration) {
	m.stop.Store(true)
	m.mu.Lock()
	for _, rj := range m.running {
		if rj.cancellable {
			rj.cancel(ErrStopped)
		}
	}
	m.mu.Unlock()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		n := m.busy
		m.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Busy reports how many jobs are running in this process.
func (m *Manager) Busy() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy
}

func (m *Manager) dispatch(ctx context.Context) {
	m.mu.Lock()
	free := m.workers - m.busy
	m.mu.Unlock()
	if free <= 0 {
		return
	}
	now := store.NowT()
	// The queue is read a page at a time and blocked jobs (locked library,
	// heavy slot taken) are skipped, so runnable jobs further down are not
	// starved by a full first page.
	var after *Job
	for free > 0 {
		page, err := m.queuedPage(ctx, now, after)
		if err != nil {
			m.log.Error("job queue", "err", err)
			return
		}
		for _, j := range page {
			if free <= 0 || m.stop.Load() {
				return
			}
			if m.tryStart(ctx, j) {
				free--
			}
		}
		if len(page) < dispatchPage {
			return
		}
		after = page[len(page)-1]
	}
}

// queuedPage returns due queued jobs in dispatch order after the cursor.
func (m *Manager) queuedPage(ctx context.Context, now string, after *Job) ([]*Job, error) {
	q := `SELECT ` + jobCols + ` FROM jobs WHERE state = 'queued' AND scheduled_for <= ?`
	args := []any{now}
	if after != nil {
		sf := store.T(after.ScheduledFor)
		q += ` AND (priority < ? OR (priority = ? AND (scheduled_for > ? OR (scheduled_for = ? AND id > ?))))`
		args = append(args, after.Priority, after.Priority, sf, sf, after.ID)
	}
	q += ` ORDER BY priority DESC, scheduled_for, id LIMIT ?`
	args = append(args, dispatchPage)
	rows, err := m.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := m.scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// tryStart claims a queued job and runs it; false when it must wait.
func (m *Manager) tryStart(ctx context.Context, j *Job) bool {
	spec, ok := m.specs[j.Type]
	if !ok {
		m.finish(j, Failed, fmt.Errorf("%w: %s", ErrUnknownType, j.Type))
		return false
	}
	if !m.locks.tryAcquire(j.LibraryID, spec.Lock) {
		return false
	}
	if spec.Heavy {
		select {
		case m.heavy <- struct{}{}:
		default:
			m.locks.release(j.LibraryID, spec.Lock)
			return false
		}
	}
	// Registered before the row says "running", so Unique never mistakes a
	// job being started for a ghost.
	jctx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	m.mu.Lock()
	m.busy++
	m.running[j.ID] = &runningJob{cancel: cancel, cancellable: spec.Cancellable}
	m.mu.Unlock()
	res, err := m.db.ExecContext(ctx, `UPDATE jobs SET state = 'running', started_at = ? WHERE id = ? AND state = 'queued'`, store.NowT(), j.ID)
	var n int64
	if err == nil {
		n, err = res.RowsAffected()
	}
	if err != nil || n != 1 {
		if err != nil && ctx.Err() == nil {
			m.log.Error("could not start a job", "job", j.ID, "err", err)
		}
		m.mu.Lock()
		m.busy--
		delete(m.running, j.ID)
		m.mu.Unlock()
		cancel(nil)
		m.locks.release(j.LibraryID, spec.Lock)
		if spec.Heavy {
			<-m.heavy
		}
		return false
	}
	now := time.Now()
	j.StartedAt = &now
	j.State = Running
	m.wg.Add(1)
	go m.execute(jctx, cancel, j, spec)
	return true
}

var codeRe = regexp.MustCompile(`SQS-[A-Z][0-9]{3}`)

func (m *Manager) execute(ctx context.Context, cancel context.CancelCauseFunc, j *Job, spec Spec) {
	defer m.wg.Done()
	r := &Run{Job: j, m: m, ctx: ctx}
	m.bus.Publish("job:"+j.ID, Update{JobID: j.ID, State: Running})
	m.bus.Publish("jobs", Update{JobID: j.ID, State: Running})
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("SQS-X999 internal error: %v", p)
				m.log.Error("panic in job", "job", j.ID, "panic", p, "stack", string(debug.Stack()))
			}
		}()
		return spec.Run(ctx, r)
	}()
	state := Succeeded
	if err != nil {
		state = Failed
		switch {
		case ctx.Err() != nil && errors.Is(context.Cause(ctx), ErrStopped):
			state = Interrupted
			err = fmt.Errorf("%w (%v)", ErrStopped, err)
		case ctx.Err() != nil && (errors.Is(err, context.Canceled) || spec.Cancellable):
			state = Cancelled
		}
	}
	m.finish(j, state, err)
	cancel(nil)
	m.mu.Lock()
	m.busy--
	delete(m.running, j.ID)
	m.mu.Unlock()
	m.locks.release(j.LibraryID, spec.Lock)
	if spec.Heavy {
		<-m.heavy
	}
	m.Wake()
}

// finish records the final state of a job. It never uses the job context
// (cancelled by then): the final update is retried with a bounded backoff,
// because a row left "running" would hold Unique and the schedules.
func (m *Manager) finish(j *Job, state State, err error) {
	ctx := context.Background()
	code, detail := "", ""
	if err != nil {
		detail = err.Error()
		code = codeRe.FindString(detail)
		if code == "" {
			code = "SQS-X900"
		}
	}
	progress := j.Progress
	if state == Succeeded {
		progress = 100
	}
	stepState := map[State]string{Failed: "failed", Interrupted: "interrupted"}[state]
	if stepState == "" {
		stepState = "cancelled"
	}
	for i := 0; ; i++ {
		uerr := m.recordFinal(ctx, j.ID, state, code, detail, progress, stepState)
		if uerr == nil {
			break
		}
		if i >= len(m.finishWaits) {
			m.log.Error("could not record the final state of a job; it stays running until the next start", "job", j.ID, "state", state, "err", uerr)
			break
		}
		m.log.Warn("could not record the final state of a job; retrying", "job", j.ID, "state", state, "err", uerr)
		time.Sleep(m.finishWaits[i])
	}
	u := Update{JobID: j.ID, State: state, Progress: progress, Error: detail}
	m.bus.Publish("job:"+j.ID, u)
	m.bus.Publish("jobs", u)
	switch state {
	case Failed:
		m.ev.Emit(ctx, events.Event{Type: events.JobFailed, Severity: events.Error, LibraryID: j.LibraryID, JobID: j.ID,
			Message: fmt.Sprintf("The job %q failed: %s", m.Title(j.Type), shorten(detail, 300)), Data: map[string]any{"code": code, "job_type": j.Type}})
	case Interrupted:
		m.ev.Emit(ctx, events.Event{Type: events.JobInterrupted, Severity: events.Warning, LibraryID: j.LibraryID, JobID: j.ID,
			Message: fmt.Sprintf("The job %q was interrupted because sqsolve stopped", m.Title(j.Type)), Data: map[string]any{"job_type": j.Type}})
	}
}

// recordFinal writes the final state of the job and of its open steps. Both
// updates are idempotent, so a failed attempt is simply repeated.
func (m *Manager) recordFinal(ctx context.Context, id string, state State, code, detail string, progress int, stepState string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := store.NowT()
	if _, err := m.db.ExecContext(ctx, `UPDATE jobs SET state = ?, finished_at = ?, error_code = ?, error_detail = ?, progress = ? WHERE id = ?`,
		string(state), now, code, detail, progress, id); err != nil {
		return err
	}
	_, err := m.db.ExecContext(ctx, `UPDATE job_steps SET state = ?, finished_at = ? WHERE job_id = ? AND state = 'running'`, stepState, now, id)
	return err
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Run is the handle a job uses while it executes.
type Run struct {
	Job    *Job
	m      *Manager
	ctx    context.Context
	step   int
	logSeq int
	mu     sync.Mutex
}

// Params decodes the job parameters.
func (r *Run) Params(out any) error { return json.Unmarshal(r.Job.Params, out) }

// Step records and runs one named step.
func (r *Run) Step(name string, fn func() error) error {
	r.mu.Lock()
	r.step++
	seq := r.step
	r.mu.Unlock()
	db := r.m.db
	ctx := context.WithoutCancel(r.ctx)
	db.ExecContext(ctx, `INSERT INTO job_steps(job_id, seq, name, state, started_at) VALUES (?, ?, ?, 'running', ?)`, r.Job.ID, seq, name, store.NowT())
	db.ExecContext(ctx, `UPDATE jobs SET current_step = ? WHERE id = ?`, name, r.Job.ID)
	r.Job.CurrentStep = name
	r.m.bus.Publish("job:"+r.Job.ID, Update{JobID: r.Job.ID, State: Running, Progress: r.Job.Progress, Step: name})
	err := fn()
	state := "succeeded"
	if err != nil {
		state = "failed"
		if r.ctx.Err() != nil {
			state = "cancelled"
			if errors.Is(context.Cause(r.ctx), ErrStopped) {
				state = "interrupted"
			}
		}
	}
	db.ExecContext(ctx, `UPDATE job_steps SET state = ?, finished_at = ? WHERE job_id = ? AND seq = ?`, state, store.NowT(), r.Job.ID, seq)
	return err
}

// Journal stores data on the current step before an irreversible action, so
// reconciliation knows what was about to happen.
func (r *Run) Journal(data any) error {
	r.mu.Lock()
	seq := r.step
	r.mu.Unlock()
	_, err := r.m.db.ExecContext(context.WithoutCancel(r.ctx), `UPDATE job_steps SET data = ? WHERE job_id = ? AND seq = ?`, store.JSON(data), r.Job.ID, seq)
	return err
}

// Progress sets the percentage.
func (r *Run) Progress(p int) {
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	r.Job.Progress = p
	r.m.db.ExecContext(context.WithoutCancel(r.ctx), `UPDATE jobs SET progress = ? WHERE id = ?`, p, r.Job.ID)
	r.m.bus.Publish("job:"+r.Job.ID, Update{JobID: r.Job.ID, State: Running, Progress: p, Step: r.Job.CurrentStep})
}

// Logf appends a log line.
func (r *Run) Logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.logSeq++
	seq := r.logSeq
	r.mu.Unlock()
	l := LogLine{Seq: seq, TS: time.Now(), Level: level, Message: msg}
	r.m.db.ExecContext(context.WithoutCancel(r.ctx), `INSERT INTO job_logs(job_id, seq, ts, level, message) VALUES (?, ?, ?, ?, ?)`, r.Job.ID, seq, store.T(l.TS), level, msg)
	r.m.bus.Publish("job:"+r.Job.ID, Update{JobID: r.Job.ID, State: Running, Progress: r.Job.Progress, Step: r.Job.CurrentStep, Log: &l})
	r.m.log.Debug(msg, "job", r.Job.ID, "level", level)
}

// Infof logs at info level.
func (r *Run) Infof(format string, args ...any) { r.Logf("info", format, args...) }

// Warnf logs at warning level.
func (r *Run) Warnf(format string, args ...any) { r.Logf("warn", format, args...) }

// SetResult stores the job result (for example the backup id).
func (r *Run) SetResult(v any) {
	b := store.JSON(v)
	r.Job.Result = json.RawMessage(b)
	r.m.db.ExecContext(context.WithoutCancel(r.ctx), `UPDATE jobs SET result = ? WHERE id = ?`, b, r.Job.ID)
}

// Context returns the job context.
func (r *Run) Context() context.Context { return r.ctx }

type lockState struct {
	readers int
	writer  bool
}

type lockTable struct {
	mu sync.Mutex
	m  map[string]*lockState
}

func newLockTable() *lockTable { return &lockTable{m: map[string]*lockState{}} }

func (t *lockTable) tryAcquire(key string, mode LockMode) bool {
	if mode == LockNone || key == "" {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.m[key]
	if s == nil {
		s = &lockState{}
		t.m[key] = s
	}
	switch mode {
	case LockRead:
		if s.writer {
			return false
		}
		s.readers++
	case LockWrite:
		if s.writer || s.readers > 0 {
			return false
		}
		s.writer = true
	}
	return true
}

func (t *lockTable) release(key string, mode LockMode) {
	if mode == LockNone || key == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.m[key]
	if s == nil {
		return
	}
	switch mode {
	case LockRead:
		s.readers--
	case LockWrite:
		s.writer = false
	}
	if s.readers <= 0 && !s.writer {
		delete(t.m, key)
	}
}
