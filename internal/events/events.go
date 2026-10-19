// SPDX-License-Identifier: AGPL-3.0-only

// Package events records business events for people (persisted in SQLite)
// and fans out live updates to the browser through an in-memory bus.
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// Severity of an event.
type Severity string

// Severities, ordered.
const (
	Info     Severity = "info"
	Warning  Severity = "warning"
	Error    Severity = "error"
	Critical Severity = "critical"
)

// Rank orders severities.
func (s Severity) Rank() int {
	switch s {
	case Warning:
		return 1
	case Error:
		return 2
	case Critical:
		return 3
	}
	return 0
}

// Label is the Spanish name of a severity.
func (s Severity) Label() string {
	switch s {
	case Warning:
		return "Aviso"
	case Error:
		return "Error"
	case Critical:
		return "Crítico"
	}
	return "Info"
}

// Event types (docs 10 §4).
const (
	LibraryCreated           = "library.created"
	LibraryActivated         = "library.activated"
	LibraryInitExpired       = "library.init_expired"
	LibraryExtraDatabase     = "library.extra_database"
	LibrarySchemaChanged     = "library.schema_changed"
	LibraryMaintenanceOn     = "library.maintenance_on"
	LibraryMaintenanceOff    = "library.maintenance_off"
	LibraryTrashed           = "library.trashed"
	LibraryRestoredFromTrash = "library.restored_from_trash"
	LibraryPurged            = "library.purged"
	CredentialRotated        = "library.credential_rotated"
	CredentialRevealed       = "library.credential_revealed"
	BackupStarted            = "backup.started"
	BackupSucceeded          = "backup.succeeded"
	BackupFailed             = "backup.failed"
	BackupVerifyFailed       = "backup.verify_failed"
	BackupVerified           = "backup.verified"
	BackupMissed             = "backup.missed"
	BackupStale              = "backup.stale"
	BackupExpired            = "backup.expired"
	RestoreStarted           = "restore.started"
	RestoreSucceeded         = "restore.succeeded"
	RestoreFailed            = "restore.failed"
	RestoreReverted          = "restore.reverted"
	StorageLow               = "storage.low"
	StorageVolumeMissing     = "storage.volume_missing"
	PostgresDown             = "postgres.down"
	PostgresUp               = "postgres.up"
	AuthBruteforce           = "auth.login_failed_burst"
	AuditChainBroken         = "audit.chain_broken"
	TLSExpiring              = "tls.expiring"
	JobInterrupted           = "job.interrupted"
	JobFailed                = "job.failed"
	SystemStarted            = "system.started"
	SystemUpgraded           = "system.upgraded"
)

// Event is one recorded fact.
type Event struct {
	ID        string         `json:"id"`
	TS        time.Time      `json:"occurred_at"`
	Type      string         `json:"type"`
	Severity  Severity       `json:"severity"`
	LibraryID string         `json:"library_id,omitempty"`
	JobID     string         `json:"job_id,omitempty"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data,omitempty"`
}

// Msg is a live update on the bus.
type Msg struct {
	Topic string
	Data  any
}

// Bus is a small publish/subscribe hub for SSE.
type Bus struct {
	mu   sync.Mutex
	next int
	subs map[int]sub
}

type sub struct {
	topics map[string]bool
	ch     chan Msg
}

// NewBus creates a bus.
func NewBus() *Bus { return &Bus{subs: map[int]sub{}} }

// Subscribe returns a channel of messages for the topics and a cancel func.
func (b *Bus) Subscribe(topics ...string) (<-chan Msg, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	t := map[string]bool{}
	for _, x := range topics {
		t[x] = true
	}
	ch := make(chan Msg, 64)
	b.subs[id] = sub{t, ch}
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if s, ok := b.subs[id]; ok {
			close(s.ch)
			delete(b.subs, id)
		}
	}
}

// Publish sends a message without blocking; slow subscribers miss updates.
func (b *Bus) Publish(topic string, data any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs {
		if s.topics[topic] || s.topics["*"] {
			select {
			case s.ch <- Msg{topic, data}:
			default:
			}
		}
	}
}

// Recorder persists events and passes them to listeners.
type Recorder struct {
	db        *store.DB
	bus       *Bus
	log       *slog.Logger
	mu        sync.RWMutex
	listeners []func(Event)
}

// NewRecorder creates a recorder.
func NewRecorder(db *store.DB, bus *Bus, log *slog.Logger) *Recorder {
	return &Recorder{db: db, bus: bus, log: log}
}

// Listen registers a function called for every event (notifications).
func (r *Recorder) Listen(fn func(Event)) {
	r.mu.Lock()
	r.listeners = append(r.listeners, fn)
	r.mu.Unlock()
}

// Emit records an event. Failures to persist are logged, never fatal.
func (r *Recorder) Emit(ctx context.Context, e Event) Event {
	if e.ID == "" {
		e.ID = ulid.New()
	}
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	if e.Severity == "" {
		e.Severity = Info
	}
	data, _ := json.Marshal(e.Data)
	if e.Data == nil {
		data = []byte("{}")
	}
	_, err := r.db.ExecContext(context.WithoutCancel(ctx), `INSERT INTO events(id, ts, type, severity, library_id, job_id, message, data) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, store.T(e.TS), e.Type, string(e.Severity), e.LibraryID, e.JobID, e.Message, string(data))
	if err != nil {
		r.log.Error("no se pudo guardar el evento", "type", e.Type, "err", err)
	}
	level := slog.LevelInfo
	switch e.Severity {
	case Warning:
		level = slog.LevelWarn
	case Error, Critical:
		level = slog.LevelError
	}
	r.log.Log(ctx, level, e.Message, "event", e.Type, "library", e.LibraryID, "job", e.JobID)
	r.bus.Publish("events", e)
	r.mu.RLock()
	ls := append([]func(Event){}, r.listeners...)
	r.mu.RUnlock()
	for _, fn := range ls {
		fn(e)
	}
	return e
}

// Filter for List.
type Filter struct {
	LibraryID   string
	MinSeverity Severity
	TypePrefix  string
	Before      string // id cursor
	Limit       int
}

// List returns events, newest first.
func (r *Recorder) List(ctx context.Context, f Filter) ([]Event, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q := `SELECT id, ts, type, severity, library_id, job_id, message, data FROM events WHERE 1=1`
	var args []any
	if f.LibraryID != "" {
		q += ` AND library_id = ?`
		args = append(args, f.LibraryID)
	}
	if f.TypePrefix != "" {
		q += ` AND type LIKE ?`
		args = append(args, f.TypePrefix+"%")
	}
	if f.MinSeverity != "" {
		var sev []any
		for _, s := range []Severity{Info, Warning, Error, Critical} {
			if s.Rank() >= f.MinSeverity.Rank() {
				sev = append(sev, string(s))
			}
		}
		q += ` AND severity IN (?` + repeat(",?", len(sev)-1) + `)`
		args = append(args, sev...)
	}
	if f.Before != "" {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var ts, sev, data string
		if err := rows.Scan(&e.ID, &ts, &e.Type, &sev, &e.LibraryID, &e.JobID, &e.Message, &data); err != nil {
			return nil, err
		}
		e.TS = store.ParseT(ts)
		e.Severity = Severity(sev)
		json.Unmarshal([]byte(data), &e.Data)
		out = append(out, e)
	}
	return out, rows.Err()
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// Purge deletes events older than before.
func (r *Recorder) Purge(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM events WHERE ts < ?`, store.T(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Latest returns the newest event of a type for a library, if any.
func (r *Recorder) Latest(ctx context.Context, typ, libraryID string) (*Event, error) {
	var e Event
	var ts, sev, data string
	err := r.db.QueryRowContext(ctx, `SELECT id, ts, type, severity, library_id, job_id, message, data FROM events
		WHERE type = ? AND library_id = ? ORDER BY id DESC LIMIT 1`, typ, libraryID).
		Scan(&e.ID, &ts, &e.Type, &sev, &e.LibraryID, &e.JobID, &e.Message, &data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.TS = store.ParseT(ts)
	e.Severity = Severity(sev)
	json.Unmarshal([]byte(data), &e.Data)
	return &e, nil
}
