// SPDX-License-Identifier: AGPL-3.0-only

package core_test

// The purge checks the other databases of the library role before it
// journals or drops anything, goes on when the purge of expired items already
// dropped and forgot its previous state, and records that previous state in
// its journal, so a purge cut before dropping it leaves the library in the
// trash instead of reporting SQS-L046 at every start (docs 11 §7.8).

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/store"
)

// trashedWithPrev creates an active library with a backup, replaces it from
// that backup, which keeps a previous state covered by a verified backup of
// its own, and moves it to the trash.
func (e *env) trashedWithPrev(slug string) *core.Library {
	e.t.Helper()
	l, _ := e.createActive(slug)
	b := e.firstBackup(l)
	e.idle()
	j, err := e.svc.RestoreReplace(e.ctx, admin, b.ID, slug, true)
	if err != nil {
		e.t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	e.idle()
	tj, err := e.svc.Trash(e.ctx, admin, l.ID, slug)
	if err != nil {
		e.t.Fatal(err)
	}
	e.wait(tj, jobs.Succeeded)
	l = e.lib(l.ID)
	if l.PrevDB == "" || l.TrashDB == "" || e.db(l.PrevDB) == nil || e.db(l.TrashDB) == nil {
		e.t.Fatalf("%s: previous state %q, trash %q", slug, l.PrevDB, l.TrashDB)
	}
	if pb, err := e.svc.Backup(e.ctx, l.PrevBackupID); err != nil || !pb.Verified() || pb.SourceDB != l.PrevDB {
		e.t.Fatalf("%s: the previous state %s has no verified backup of its own: %+v %v", slug, l.PrevDB, pb, err)
	}
	return l
}

// purge queues the purge of l, confirmed as the panel does.
func (e *env) purge(l *core.Library) *jobs.Job {
	e.t.Helper()
	j, err := e.svc.Purge(e.ctx, admin, l.ID, l.Slug)
	if err != nil {
		e.t.Fatal(err)
	}
	return j
}

// stepNames lists the steps a job ran, in order.
func (e *env) stepNames(j *jobs.Job) []string {
	e.t.Helper()
	steps, err := e.svc.Jobs.Steps(e.ctx, j.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, s := range steps {
		out = append(out, s.Name)
	}
	return out
}

// purgedForGood checks that l is recorded purged, its roles and its
// databases are gone and its backups are kept.
func (e *env) purgedForGood(l *core.Library, notes string) {
	e.t.Helper()
	got := e.lib(l.ID)
	exists, err := e.svc.PG.RoleExists(e.ctx, l.OwnerRole)
	if err != nil {
		e.t.Fatal(err)
	}
	if got.State != core.StatePurged || got.TrashDB != "" || exists {
		e.t.Fatalf("%s: state %s, trash %q, role kept %v\n%s", l.Slug, got.State, got.TrashDB, exists, notes)
	}
	for _, name := range []string{l.PrevDB, l.TrashDB} {
		if name != "" && e.db(name) != nil {
			e.t.Fatalf("%s: %s is still on the server\n%s", l.Slug, name, notes)
		}
	}
	if bs, _ := e.svc.Backups(e.ctx, core.BackupFilter{LibraryID: l.ID, Statuses: []string{core.BackupOK}}); len(bs) == 0 {
		e.t.Fatalf("%s: the backups were lost", l.Slug)
	}
}

// keptInTheTrash checks that a purge that stopped left l in the trash with
// its roles and with the databases it had, and that its job j does not wait
// for reconciliation.
func (e *env) keptInTheTrash(l *core.Library, j *jobs.Job) {
	e.t.Helper()
	got := e.lib(l.ID)
	exists, err := e.svc.PG.RoleExists(e.ctx, l.OwnerRole)
	if err != nil {
		e.t.Fatal(err)
	}
	if got.State != core.StateTrashed || got.TrashDB != l.TrashDB || got.PrevDB != l.PrevDB || !exists {
		e.t.Fatalf("%s changed: state %s, trash %q, previous state %q, role kept %v", l.Slug, got.State, got.TrashDB, got.PrevDB, exists)
	}
	if l.TrashDB != "" && e.db(l.TrashDB) == nil {
		e.t.Fatalf("%s: the trash database %s was dropped", l.Slug, l.TrashDB)
	}
	if e.pendingFlag(j.ID) != 0 {
		e.t.Fatalf("%s: a purge that dropped nothing waits for reconciliation", l.Slug)
	}
}

// A database of the library role that the purge does not drop itself stops
// it before its journal and before any drop, so the library can still be
// recovered from the trash; so does a lookup that fails. A previous state
// that an interrupted DROP DATABASE left invalid is such a database, since
// the purge never drops it. A database that appears after the check is still
// caught right before the roles are dropped.
func TestPurgeChecksTheRoleBeforeAnyDrop(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	exact := func(j *jobs.Job, want string) {
		t.Helper()
		if got := e.failsWith(j, "SQS-L046", want); got.ErrorDetail != want {
			t.Fatalf("detail %q, want %q", got.ErrorDetail, want)
		}
	}
	noJournal := func(j *jobs.Job) {
		t.Helper()
		if steps := e.stepNames(j); slices.Contains(steps, "journal") {
			t.Fatalf("the purge journaled before it stopped: %v", steps)
		}
	}

	// Another database of the role: the previous state and the trash database
	// stay, and the library comes back from the trash.
	a := e.trashedWithPrev("pc_owned")
	if err := e.svc.PG.CreateDatabase(ctx, "pc_owned_extra", a.OwnerRole); err != nil {
		t.Fatal(err)
	}
	j := e.purge(a)
	exact(j, "SQS-L046 role "+a.OwnerRole+" still owns: pc_owned_extra; review them before purging")
	e.keptInTheTrash(a, j)
	noJournal(j)
	if e.db(a.PrevDB) == nil {
		t.Fatalf("the previous state %s was dropped", a.PrevDB)
	}
	uj, err := e.svc.RestoreFromTrash(ctx, admin, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(uj, jobs.Succeeded)
	if got := e.lib(a.ID); got.State != core.StateActive || got.PrevDB != a.PrevDB {
		t.Fatalf("not recovered: state %s, previous state %q", got.State, got.PrevDB)
	}

	// The lookup fails: SQS-L047 with nothing dropped and nothing left for
	// reconciliation.
	b := e.trashed("pc_lookup")
	restore := core.SetFault("purge.owned_check", errors.New("lookup refused"))
	j = e.purge(b)
	e.failsWith(j, "SQS-L047", "the purge stopped before it finished: could not list the databases of "+b.OwnerRole+": lookup refused")
	restore()
	e.keptInTheTrash(b, j)
	noJournal(j)
	j = e.purge(b)
	e.wait(j, jobs.Succeeded)
	e.purgedForGood(b, "")

	// A previous state left invalid (datconnlimit -2) by an interrupted DROP
	// DATABASE: reported before the trash database is dropped.
	c := e.trashedWithPrev("pc_invalid")
	su, err := e.c.Connect(ctx, "postgres", e.c.SuperPass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer su.Close(ctx)
	if _, err := su.Exec(ctx, `UPDATE pg_database SET datconnlimit = -2 WHERE datname = $1`, c.PrevDB); err != nil {
		t.Fatal(err)
	}
	j = e.purge(c)
	exact(j, "SQS-L046 role "+c.OwnerRole+" still owns: "+c.PrevDB+"; review them before purging")
	e.keptInTheTrash(c, j)
	noJournal(j)
	// What the administrator does: drop it by hand, then purge again.
	if err := e.svc.PG.DropDatabase(ctx, c.PrevDB); err != nil {
		t.Fatal(err)
	}
	j = e.purge(c)
	e.wait(j, jobs.Succeeded)
	e.purgedForGood(c, "")

	// A database created between the check and the role drop: SQS-L046 right
	// before the roles are dropped, and again at the check of the next purge.
	d := e.trashed("pc_late")
	restore = core.SetFaultHook(func(point string) error {
		if point == "purge.drop" {
			if err := e.svc.PG.CreateDatabase(ctx, "pc_late_extra", d.OwnerRole); err != nil {
				t.Error(err)
			}
		}
		return nil
	})
	j = e.purge(d)
	exact(j, "SQS-L046 role "+d.OwnerRole+" still owns: pc_late_extra; review them before purging")
	restore()
	if exists, _ := e.svc.PG.RoleExists(ctx, d.OwnerRole); e.db(d.TrashDB) != nil || !exists || e.lib(d.ID).State != core.StateTrashed || e.pendingFlag(j.ID) != 0 {
		t.Fatalf("after SQS-L046 at the role drop: trash kept %v, role kept %v, state %s, pending %d",
			e.db(d.TrashDB) != nil, exists, e.lib(d.ID).State, e.pendingFlag(j.ID))
	}
	j = e.purge(d)
	exact(j, "SQS-L046 role "+d.OwnerRole+" still owns: pc_late_extra; review them before purging")
	noJournal(j)
	if err := e.svc.PG.DropDatabase(ctx, "pc_late_extra"); err != nil {
		t.Fatal(err)
	}
	j = e.purge(d)
	e.wait(j, jobs.Succeeded)
	e.purgedForGood(d, "")
}

// The purge of expired items drops an expired previous state and forgets it
// without the library's lock: a purge that finds its previous state already
// gone and forgotten goes on. One whose catalog write fails after the
// drop still stops with SQS-L047 and is finished by the next start.
func TestPurgeAcceptsAPreviousStateAlreadyForgotten(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	exec := func(q string) {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	// The row is forgotten right after the purge read it (the trigger runs
	// when its journal is written), as the pass does.
	a := e.trashedWithPrev("pf_forgot")
	if err := e.svc.PG.DropDatabase(ctx, a.PrevDB); err != nil {
		t.Fatal(err)
	}
	exec(`CREATE TRIGGER forgot AFTER UPDATE OF data ON job_steps WHEN NEW.data LIKE '%purge_of%' BEGIN UPDATE libraries SET prev_db_name = '', prev_expires_at = NULL, prev_backup_id = '' WHERE prev_db_name <> ''; END`)
	j := e.purge(a)
	got := e.wait(j, jobs.Succeeded)
	exec(`DROP TRIGGER forgot`)
	e.purgedForGood(a, "")
	if e.pendingFlag(got.ID) != 0 {
		t.Fatal("a purge that finished waits for reconciliation")
	}

	// The catalog write fails: the row still names the dropped previous
	// state.
	b := e.trashedWithPrev("pf_fault")
	exec(`CREATE TRIGGER fault BEFORE UPDATE OF prev_db_name ON libraries BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
	j = e.purge(b)
	got = e.failsWith(j, "SQS-L047", "the purge stopped before it finished: the previous state "+b.PrevDB+" was dropped, but the catalog still names it")
	exec(`DROP TRIGGER fault`)
	if !strings.Contains(got.ErrorDetail, "reconciliation finishes it at the next start of sqsolve") || e.pendingFlag(got.ID) != 1 {
		t.Fatalf("not pending reconciliation: %q, pending %d", got.ErrorDetail, e.pendingFlag(got.ID))
	}
	if l := e.lib(b.ID); e.db(b.PrevDB) != nil || e.db(b.TrashDB) == nil || l.State != core.StateTrashed {
		t.Fatalf("previous state kept %v, trash kept %v, state %s", e.db(b.PrevDB) != nil, e.db(b.TrashDB) != nil, l.State)
	}
	notes := e.start()
	for _, want := range []string{
		"pf_fault: the purge stopped before dropping " + b.TrashDB + "; the library stays in the trash",
		"pf_fault: forgot its previous state " + b.PrevDB + ", which is no longer on the server",
	} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
	if e.pendingFlag(got.ID) != 0 || e.lib(b.ID).PrevDB != "" {
		t.Fatalf("pending %d, previous state %q\n%s", e.pendingFlag(got.ID), e.lib(b.ID).PrevDB, notes)
	}
	j = e.purge(b)
	e.wait(j, jobs.Succeeded)
	e.purgedForGood(b, notes)
}

// When the trash period and the previous-state period of a library end
// before the same pass, the pass drops the previous state before it queues
// the purge, which then has no previous state to drop and succeeds.
func TestPurgeExpiredWithTrashAndPreviousStateDueTogether(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l := e.trashedWithPrev("px_both")
	due := store.T(time.Now().Add(-time.Hour))
	if _, err := e.svc.DB.ExecContext(ctx, `UPDATE libraries SET purge_after = ?, prev_expires_at = ? WHERE id = ?`, due, due, l.ID); err != nil {
		t.Fatal(err)
	}
	pj, err := e.svc.Jobs.Enqueue(ctx, core.JobPurgeExpired, "", nil, admin)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(pj, jobs.Succeeded)
	list, err := e.svc.Jobs.List(ctx, jobs.Filter{Type: core.JobLibraryPurge, LibraryID: l.ID})
	if err != nil || len(list) != 1 {
		t.Fatalf("purges queued by the pass: %d %v", len(list), err)
	}
	purge := e.wait(list[0], jobs.Succeeded)
	logs := e.jobLogs(pj)
	e.purgedForGood(l, logs)
	if !strings.Contains(logs, "Deleted previous state "+l.PrevDB) {
		t.Fatalf("the pass did not drop the previous state:\n%s", logs)
	}
	if steps := e.stepNames(purge); slices.Contains(steps, "drop_previous_state") {
		t.Fatalf("the purge queued by the pass dropped the previous state too: %v", steps)
	}
	if failed, _ := e.svc.Jobs.List(ctx, jobs.Filter{States: []jobs.State{jobs.Failed}, LibraryID: l.ID}); len(failed) != 0 {
		t.Fatalf("failed jobs: %s %s", failed[0].Type, failed[0].ErrorDetail)
	}
	if e.pendingFlag(purge.ID) != 0 {
		t.Fatal("the purge waits for reconciliation")
	}
}

// The purge of a library whose time in the trash ended is queued with its
// audit entry (actor system, ADR 0026) and its result follows. Without the
// entry nothing is queued: the pass warns with SQS-X040 and the library stays
// in the trash with its database until a later pass.
func TestAutomaticPurgeIsAudited(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l := e.trashed("pa_audit")
	due := store.T(time.Now().Add(-time.Hour))
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.svc.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE libraries SET purge_after = ? WHERE id = ?`, due, l.ID)
	pass := func() *jobs.Job {
		t.Helper()
		pj, err := e.svc.Jobs.Enqueue(ctx, core.JobPurgeExpired, "", nil, admin)
		if err != nil {
			t.Fatal(err)
		}
		return e.wait(pj, jobs.Succeeded)
	}
	purges := func() []*jobs.Job {
		t.Helper()
		list, err := e.svc.Jobs.List(ctx, jobs.Filter{Type: core.JobLibraryPurge, LibraryID: l.ID})
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	entries := func() []audit.Entry {
		t.Helper()
		list, err := e.svc.Audit.List(ctx, 0, 200, l.ID)
		if err != nil {
			t.Fatal(err)
		}
		var out []audit.Entry
		for i := len(list) - 1; i >= 0; i-- {
			if list[i].Action == "library.purge" {
				out = append(out, list[i])
			}
		}
		return out
	}

	exec(`CREATE TRIGGER purge_entry_down BEFORE INSERT ON audit_log WHEN NEW.action = 'library.purge' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
	logs := e.jobLogs(pass())
	if want := "warn: Could not queue the purge of pa_audit: SQS-X040 the audit log could not be written, so the action was not done: "; !strings.Contains(logs, want) ||
		!strings.Contains(logs, "disk I/O error") {
		t.Fatalf("the pass log lacks %q:\n%s", want, logs)
	}
	if got := e.lib(l.ID); len(purges()) != 0 || got.State != core.StateTrashed || e.db(l.TrashDB) == nil || len(entries()) != 0 {
		t.Fatalf("without its entry: %d purge jobs, state %s, %d entries", len(purges()), got.State, len(entries()))
	}
	exec(`DROP TRIGGER purge_entry_down`)

	pass()
	ps := purges()
	if len(ps) != 1 || ps[0].CreatedBy != audit.System {
		t.Fatalf("purge jobs %+v", ps)
	}
	p := e.wait(ps[0], jobs.Succeeded)
	e.purgedForGood(l, e.jobLogs(p))
	es := entries()
	if len(es) != 2 {
		t.Fatalf("entries %+v", es)
	}
	if a := es[0]; a.Actor != audit.System || a.Outcome != audit.Accepted || a.TargetType != "library" || a.Details["job_id"] != p.ID ||
		a.Details["slug"] != l.Slug || a.Details["purge_after"] != due {
		t.Fatalf("accepted entry %+v", a)
	}
	if r := es[1]; r.Actor != audit.System || r.Outcome != audit.OK || r.Details["job_id"] != p.ID || r.Details["state"] != "succeeded" {
		t.Fatalf("result entry %+v", r)
	}
}

// A purge of a library in the trash without a trash database, cut while its
// previous state is still on the server, leaves the library in the trash at
// the next start instead of reporting SQS-L046 at every start,
// whether its journal names the previous state or, written up to 1.2.3, only
// the catalog does. A database of the role that is neither still stops
// reconciliation with SQS-L046.
func TestInterruptedPurgeWithItsPreviousStateOnTheServer(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	for _, c := range []struct {
		slug, job string
		named     bool
	}{
		{"pi_named", "JPNAMED", true},
		{"pi_older", "JPOLDER", false},
	} {
		// A library replaced, lost (its live database is gone) and deleted:
		// in the trash with its previous state and no trash database.
		l, _ := e.createActive(c.slug)
		b := e.firstBackup(l)
		e.idle()
		rj, err := e.svc.RestoreReplace(ctx, admin, b.ID, c.slug, true)
		if err != nil {
			t.Fatal(err)
		}
		e.wait(rj, jobs.Succeeded)
		e.lose(e.lib(l.ID), false)
		tj, err := e.svc.Trash(ctx, admin, l.ID, c.slug)
		if err != nil {
			t.Fatal(err)
		}
		e.wait(tj, jobs.Succeeded)
		l = e.lib(l.ID)
		if l.State != core.StateTrashed || l.TrashDB != "" || l.PrevDB == "" || e.db(l.PrevDB) == nil {
			t.Fatalf("%s: state %s, trash %q, previous state %q", c.slug, l.State, l.TrashDB, l.PrevDB)
		}
		journal := fmt.Sprintf(`{"purge_of":%q,"trash_db":""}`, l.ID)
		if c.named {
			journal = fmt.Sprintf(`{"purge_of":%q,"trash_db":"","prev_db":%q}`, l.ID, l.PrevDB)
		}
		e.interruptedJob(c.job, core.JobLibraryPurge, l.ID, "drop_previous_state", journal)
		notes := e.start()
		if !strings.Contains(notes, c.slug+": the purge stopped before dropping "+l.PrevDB+"; the library stays in the trash") || strings.Contains(notes, "SQS-L046") {
			t.Fatalf("%s: notes:\n%s", c.slug, notes)
		}
		if got := e.lib(l.ID); e.pendingFlag(c.job) != 0 || got.State != core.StateTrashed || got.PrevDB != l.PrevDB || e.db(l.PrevDB) == nil {
			t.Fatalf("%s: pending %d, state %s, previous state %q\n%s", c.slug, e.pendingFlag(c.job), got.State, got.PrevDB, notes)
		}
		if ev, err := e.svc.Events.Latest(ctx, "library.reconcile_pending", l.ID); err != nil || ev != nil {
			t.Fatalf("%s: warning %+v %v", c.slug, ev, err)
		}
		if notes := e.start(); strings.Contains(notes, c.slug) {
			t.Fatalf("%s: the next start reports it again:\n%s", c.slug, notes)
		}
		j := e.purge(l)
		e.wait(j, jobs.Succeeded)
		e.purgedForGood(l, "")
	}

	// Its trash database is gone and the role owns another database:
	// reconciliation keeps reporting SQS-L046 until a person drops it.
	l := e.trashed("pr_late")
	if err := e.svc.PG.DropDatabase(ctx, l.TrashDB); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.CreateDatabase(ctx, "pr_late_extra", l.OwnerRole); err != nil {
		t.Fatal(err)
	}
	e.interruptedJob("JPLATE", core.JobLibraryPurge, l.ID, "drop_roles", fmt.Sprintf(`{"purge_of":%q,"trash_db":%q}`, l.ID, l.TrashDB))
	notes := e.start()
	if !strings.Contains(notes, "pr_late: could not finish the interrupted purge: SQS-L046 role pr_late still owns: pr_late_extra; review them before purging") {
		t.Fatalf("notes:\n%s", notes)
	}
	if exists, _ := e.svc.PG.RoleExists(ctx, l.OwnerRole); e.pendingFlag("JPLATE") != 1 || !exists || e.lib(l.ID).State != core.StateTrashed {
		t.Fatalf("pending %d, role kept %v, state %s", e.pendingFlag("JPLATE"), exists, e.lib(l.ID).State)
	}
	if ev, err := e.svc.Events.Latest(ctx, "library.reconcile_pending", l.ID); err != nil || ev == nil {
		t.Fatalf("no warning: %v", err)
	}
	if err := e.svc.PG.DropDatabase(ctx, "pr_late_extra"); err != nil {
		t.Fatal(err)
	}
	notes = e.start()
	e.purgedForGood(l, notes)
	if e.pendingFlag("JPLATE") != 0 {
		t.Fatalf("still pending:\n%s", notes)
	}
}
