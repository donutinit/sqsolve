// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/store"
)

// logBuffer collects the log lines of a recorder.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// newRecorder opens a fresh control plane database and a recorder on it.
func newRecorder(t *testing.T) (*Recorder, *Bus, *store.DB, *logBuffer) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logs := &logBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	bus := NewBus()
	return NewRecorder(db, bus, log), bus, db, logs
}

func ids(evs []Event) string {
	var out []string
	for _, e := range evs {
		out = append(out, e.ID)
	}
	return strings.Join(out, ",")
}

// An event emitted with only a type and a message gets an ID, the current
// time and the info severity; it is stored, published on the "events" topic
// and passed to every listener with those defaults.
func TestEmitFillsDefaults(t *testing.T) {
	ctx := context.Background()
	r, bus, _, logs := newRecorder(t)
	ch, cancel := bus.Subscribe("events")
	defer cancel()
	var heard []Event
	r.Listen(func(e Event) { heard = append(heard, e) })
	r.Listen(func(e Event) { heard = append(heard, e) })
	before := time.Now()
	e := r.Emit(ctx, Event{Type: LibraryCreated, Message: "Library Demo created"})
	if len(e.ID) != 26 || e.Severity != Info || e.TS.Before(before) || e.TS.After(time.Now()) {
		t.Fatalf("defaults %+v", e)
	}
	select {
	case m := <-ch:
		if got, ok := m.Data.(Event); m.Topic != "events" || !ok || got.ID != e.ID {
			t.Fatalf("published %+v", m)
		}
	default:
		t.Fatal("nothing published")
	}
	if len(heard) != 2 || heard[0].ID != e.ID || heard[1].ID != e.ID {
		t.Fatalf("listeners heard %+v", heard)
	}
	got, err := r.List(ctx, Filter{})
	if err != nil || len(got) != 1 {
		t.Fatalf("stored %+v: %v", got, err)
	}
	s := got[0]
	if s.ID != e.ID || s.Type != LibraryCreated || s.Severity != Info || s.Message != "Library Demo created" || len(s.Data) != 0 || !s.TS.Equal(e.TS.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("stored %+v, emitted %+v", s, e)
	}
	if line := logs.String(); !strings.Contains(line, "level=INFO") || !strings.Contains(line, `msg="Library Demo created"`) || !strings.Contains(line, "event=library.created") {
		t.Fatalf("log %q", line)
	}
}

// What the caller sets is kept: ID, time, severity, library, job and data,
// which come back from the database as JSON values.
func TestEmitKeepsWhatIsGiven(t *testing.T) {
	ctx := context.Background()
	r, _, _, _ := newRecorder(t)
	at := time.Date(2026, 10, 20, 3, 4, 5, 123456000, time.UTC)
	e := r.Emit(ctx, Event{ID: "01JEVENT", TS: at, Type: BackupFailed, Severity: Error, LibraryID: "LIB1", JobID: "JOB1",
		Message: "Backup failed", Data: map[string]any{"code": "SQS-B001", "attempt": 2}})
	if e.ID != "01JEVENT" || !e.TS.Equal(at) || e.Severity != Error {
		t.Fatalf("emitted %+v", e)
	}
	got, err := r.Latest(ctx, BackupFailed, "LIB1")
	if err != nil || got == nil {
		t.Fatalf("latest %+v: %v", got, err)
	}
	if got.ID != "01JEVENT" || !got.TS.Equal(at) || got.JobID != "JOB1" || got.LibraryID != "LIB1" || got.Severity != Error ||
		got.Data["code"] != "SQS-B001" || got.Data["attempt"] != float64(2) {
		t.Fatalf("stored %+v", got)
	}
}

// The log level follows the severity: warnings are WARN, errors and critical
// events are ERROR.
func TestEmitLogLevelFollowsSeverity(t *testing.T) {
	ctx := context.Background()
	for sev, want := range map[Severity]string{Info: "level=INFO", Warning: "level=WARN", Error: "level=ERROR", Critical: "level=ERROR"} {
		r, _, _, logs := newRecorder(t)
		r.Emit(ctx, Event{Type: StorageLow, Severity: sev, Message: "space"})
		if line := logs.String(); !strings.Contains(line, want) {
			t.Errorf("%s: %q, want %s", sev, line, want)
		}
	}
}

// A database that cannot store the event does not stop it: the failure is
// logged with its cause, and the bus and the listeners still get the event
// (a notification about a full disk must still go out).
func TestEmitSurvivesAFailedInsert(t *testing.T) {
	ctx := context.Background()
	r, bus, db, logs := newRecorder(t)
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER events_down BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	ch, cancel := bus.Subscribe("*")
	defer cancel()
	heard := 0
	r.Listen(func(Event) { heard++ })
	e := r.Emit(ctx, Event{Type: StorageLow, Severity: Critical, Message: "No space left"})
	if e.ID == "" || heard != 1 || len(ch) != 1 {
		t.Fatalf("event %+v, listeners %d, published %d", e, heard, len(ch))
	}
	line := logs.String()
	if !strings.Contains(line, `msg="could not save the event"`) || !strings.Contains(line, "type=storage.low") || !strings.Contains(line, "disk I/O error") {
		t.Fatalf("log %q", line)
	}
	if got, err := r.List(ctx, Filter{}); err != nil || len(got) != 0 {
		t.Fatalf("stored %+v: %v", got, err)
	}
}

// A cancelled request still records its event: the insert does not use the
// cancellation of the caller.
func TestEmitIgnoresTheCallerCancellation(t *testing.T) {
	r, _, _, _ := newRecorder(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Emit(ctx, Event{Type: AuthLocked, Severity: Warning, Message: "locked"})
	if got, err := r.List(context.Background(), Filter{}); err != nil || len(got) != 1 {
		t.Fatalf("stored %+v: %v", got, err)
	}
}

// seed stores events with increasing IDs E001, E002... so newest first is the
// reverse of the order given.
func seed(t *testing.T, r *Recorder, evs ...Event) {
	t.Helper()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i, e := range evs {
		e.ID = fmt.Sprintf("E%03d", i+1)
		e.TS = base.Add(time.Duration(i) * time.Minute)
		r.Emit(context.Background(), e)
	}
}

// List filters by library, type prefix and minimum severity, combined, and
// returns the newest first.
func TestListFilters(t *testing.T) {
	ctx := context.Background()
	r, _, _, _ := newRecorder(t)
	seed(t, r,
		Event{Type: BackupSucceeded, Severity: Info, LibraryID: "A", Message: "1"},
		Event{Type: BackupFailed, Severity: Error, LibraryID: "A", Message: "2"},
		Event{Type: StorageLow, Severity: Warning, Message: "3"},
		Event{Type: BackupMissed, Severity: Error, LibraryID: "B", Message: "4"},
		Event{Type: PostgresDown, Severity: Critical, Message: "5"},
		Event{Type: RestoreFailed, Severity: Error, LibraryID: "A", Message: "6"},
	)
	for _, tc := range []struct {
		name string
		f    Filter
		want string
	}{
		{"all", Filter{}, "E006,E005,E004,E003,E002,E001"},
		{"library", Filter{LibraryID: "A"}, "E006,E002,E001"},
		{"type prefix", Filter{TypePrefix: "backup."}, "E004,E002,E001"},
		{"exact type", Filter{TypePrefix: BackupFailed}, "E002"},
		{"from info", Filter{MinSeverity: Info}, "E006,E005,E004,E003,E002,E001"},
		{"from warning", Filter{MinSeverity: Warning}, "E006,E005,E004,E003,E002"},
		{"from error", Filter{MinSeverity: Error}, "E006,E005,E004,E002"},
		{"critical only", Filter{MinSeverity: Critical}, "E005"},
		{"unknown severity counts as info", Filter{MinSeverity: "debug"}, "E006,E005,E004,E003,E002,E001"},
		{"before a cursor", Filter{Before: "E004"}, "E003,E002,E001"},
		{"combined", Filter{LibraryID: "A", TypePrefix: "backup.", MinSeverity: Error}, "E002"},
		{"nothing matches", Filter{LibraryID: "C"}, ""},
	} {
		got, err := r.List(ctx, tc.f)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if ids(got) != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, ids(got), tc.want)
		}
	}
}

// Pages of Limit events chained by the Before cursor walk every event once,
// newest first; a limit of zero, negative or above 500 means 100.
func TestListPagination(t *testing.T) {
	ctx := context.Background()
	r, _, _, _ := newRecorder(t)
	var evs []Event
	for i := 0; i < 105; i++ {
		evs = append(evs, Event{Type: JobFailed, Severity: Error, Message: "x"})
	}
	seed(t, r, evs...)
	var walked []string
	cursor := ""
	for pages := 0; ; pages++ {
		page, err := r.List(ctx, Filter{Limit: 40, Before: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			if pages != 3 {
				t.Fatalf("%d pages, want 3", pages)
			}
			break
		}
		walked = append(walked, ids(page))
		cursor = page[len(page)-1].ID
	}
	all := strings.Split(strings.Join(walked, ","), ",")
	if len(all) != 105 || all[0] != "E105" || all[104] != "E001" {
		t.Fatalf("walked %d events: first %s, last %s", len(all), all[0], all[len(all)-1])
	}
	for _, limit := range []int{0, -1, 501} {
		got, err := r.List(ctx, Filter{Limit: limit})
		if err != nil || len(got) != 100 {
			t.Errorf("limit %d: %d events, want 100: %v", limit, len(got), err)
		}
	}
	if got, _ := r.List(ctx, Filter{Limit: 500}); len(got) != 105 {
		t.Errorf("limit 500: %d events, want all 105", len(got))
	}
}

// Purge deletes the events older than the given time and reports how many.
func TestPurgeDeletesOnlyOlderEvents(t *testing.T) {
	ctx := context.Background()
	r, _, _, _ := newRecorder(t)
	now := time.Now()
	r.Emit(ctx, Event{ID: "OLD1", TS: now.Add(-48 * time.Hour), Type: BackupSucceeded, Message: "old"})
	r.Emit(ctx, Event{ID: "OLD2", TS: now.Add(-25 * time.Hour), Type: BackupSucceeded, Message: "old"})
	r.Emit(ctx, Event{ID: "NEW1", TS: now.Add(-time.Hour), Type: BackupSucceeded, Message: "recent"})
	n, err := r.Purge(ctx, now.Add(-24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("purged %d: %v", n, err)
	}
	if got, _ := r.List(ctx, Filter{}); ids(got) != "NEW1" {
		t.Fatalf("left %s", ids(got))
	}
	if n, err := r.Purge(ctx, now.Add(-24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("second purge %d: %v", n, err)
	}
}

// Latest returns the newest event of a type for one library, nil when there
// is none.
func TestLatest(t *testing.T) {
	ctx := context.Background()
	r, _, _, _ := newRecorder(t)
	if got, err := r.Latest(ctx, BackupVerified, "A"); got != nil || err != nil {
		t.Fatalf("empty: %+v %v", got, err)
	}
	seed(t, r,
		Event{Type: BackupVerified, LibraryID: "A", Message: "first"},
		Event{Type: BackupVerified, LibraryID: "A", Message: "second", Data: map[string]any{"backup_id": "BK2"}},
		Event{Type: BackupVerified, LibraryID: "B", Message: "other library"},
		Event{Type: BackupVerifyFailed, LibraryID: "A", Message: "other type"},
	)
	got, err := r.Latest(ctx, BackupVerified, "A")
	if err != nil || got == nil || got.ID != "E002" || got.Message != "second" || got.Data["backup_id"] != "BK2" || got.Severity != Info {
		t.Fatalf("latest %+v: %v", got, err)
	}
}

// Reads on a database that is gone report the error instead of an empty
// answer.
func TestReadsReportDatabaseErrors(t *testing.T) {
	ctx := context.Background()
	r, _, db, _ := newRecorder(t)
	db.Close()
	if _, err := r.List(ctx, Filter{}); err == nil || err.Error() != "sql: database is closed" {
		t.Errorf("list: %v", err)
	}
	if _, err := r.Latest(ctx, BackupFailed, "A"); err == nil || err.Error() != "sql: database is closed" {
		t.Errorf("latest: %v", err)
	}
	if _, err := r.Purge(ctx, time.Now()); err == nil || err.Error() != "sql: database is closed" {
		t.Errorf("purge: %v", err)
	}
}

// The severity ranks order the thresholds of filters and notification
// channels: an event passes a threshold when its rank is at least the
// threshold's. An unknown severity ranks and reads as info.
func TestSeverityRankDecidesThresholds(t *testing.T) {
	order := []Severity{Info, Warning, Error, Critical}
	for i, ev := range order {
		for j, min := range order {
			if pass := ev.Rank() >= min.Rank(); pass != (i >= j) {
				t.Errorf("%s against a %s threshold: pass %v", ev, min, pass)
			}
		}
	}
	for _, unknown := range []Severity{"", "debug"} {
		if unknown.Rank() != Info.Rank() || unknown.Label() != "Info" {
			t.Errorf("%q: rank %d, label %q", unknown, unknown.Rank(), unknown.Label())
		}
	}
	for sev, want := range map[Severity]string{Info: "Info", Warning: "Warning", Error: "Error", Critical: "Critical"} {
		if sev.Label() != want {
			t.Errorf("%s: label %q", sev, sev.Label())
		}
	}
}
