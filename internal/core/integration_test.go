// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/fsx"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/scheduler"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/testpg"
)

type env struct {
	t   *testing.T
	c   *testpg.Cluster
	svc *core.Service
	ctx context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	c := testpg.Start(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	app := t.TempDir()
	backups := t.TempDir()
	cfg := &config.Config{AppDir: app, BackupDir: backups, PGDir: c.Base, PGDataDir: c.DataDir, PGSocket: c.SocketDir, PGPort: c.Port,
		Location: time.UTC, Workers: 2, HeavyJobs: 1}
	db, err := store.Open(ctx, filepath.Join(app, "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if os.Getenv("SQSOLVE_TEST_LOG") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	bus := events.NewBus()
	ev := events.NewRecorder(db, bus, log)
	jm := jobs.NewManager(db, ev, bus, log, 2, 1)
	key := secrets.RandomBytes(32)
	box, _ := secrets.NewBox(key)
	client := pg.New(c.SocketDir, c.Port, c.AdminPass, c.SuperPass)
	if err := client.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	svc := &core.Service{Cfg: cfg, DB: db, PG: client, Exec: execx.NewRunner(c.BinDir), Box: box, Jobs: jm,
		Sched: scheduler.New(db, jm, ev, log), Events: ev, Audit: audit.New(db), Log: log, InstanceID: "TESTINSTANCE"}
	svc.Register()
	fsx.WriteMarker(backups, fsx.Marker{InstanceID: "TESTINSTANCE", Volume: "backups", CreatedAt: time.Now()})
	jm.Start(ctx)
	return &env{t, c, svc, ctx}
}

var admin = audit.Actor{Type: "user", ID: "U1", Name: "diego"}

func (e *env) wait(j *jobs.Job, want jobs.State) *jobs.Job {
	e.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		got, err := e.svc.Jobs.Get(e.ctx, j.ID)
		if err == nil && got.State.Finished() {
			if got.State != want {
				logs, _ := e.svc.Jobs.Logs(e.ctx, j.ID, 0)
				e.t.Fatalf("job %s (%s) ended %s, want %s: %s\nlogs: %+v\npg: %s", j.ID, j.Type, got.State, want, got.ErrorDetail, logs, tail(e.c.Log(), 3000))
			}
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("job %s timed out", j.ID)
	return nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// resolveCreate imitates what Resolve does on "Create": as the library role,
// create the database and some tables with data.
func (e *env) resolveCreate(role string, pass secrets.String, db string) {
	e.t.Helper()
	conn, err := e.c.ConnectTCP(e.ctx, role, pass, "postgres")
	if err != nil {
		e.t.Fatalf("resolve login: %v", err)
	}
	if _, err := conn.Exec(e.ctx, "CREATE DATABASE "+db); err != nil {
		e.t.Fatal(err)
	}
	conn.Close(e.ctx)
	conn = e.connect(role, pass, db)
	defer conn.Close(e.ctx)
	_, err = conn.Exec(e.ctx, `CREATE TABLE "SM_Project" (id serial primary key, name text, data bytea);
		CREATE TABLE "Sm2TiItem" (id serial primary key, project int, payload jsonb);
		INSERT INTO "SM_Project"(name, data) SELECT 'p' || g, decode(repeat('ab', 500), 'hex') FROM generate_series(1, 50) g;
		INSERT INTO "Sm2TiItem"(project, payload) SELECT g % 50, '{"x":1}' FROM generate_series(1, 2000) g;`)
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) connect(role string, pass secrets.String, db string) *pgx.Conn {
	e.t.Helper()
	conn, err := e.c.ConnectTCP(e.ctx, role, pass, db)
	if err != nil {
		e.t.Fatalf("connect %s@%s: %v", role, db, err)
	}
	return conn
}

func (e *env) count(role string, pass secrets.String, db, table string) int {
	conn := e.connect(role, pass, db)
	defer conn.Close(e.ctx)
	var n int
	if err := conn.QueryRow(e.ctx, `SELECT count(*) FROM "`+table+`"`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) createActive(slug string) (*core.Library, secrets.String) {
	e.t.Helper()
	l, j, err := e.svc.CreateLibrary(e.ctx, admin, core.CreateInput{DisplayName: "Prueba " + slug, Slug: slug})
	if err != nil {
		e.t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	l, _ = e.svc.Library(e.ctx, l.ID)
	if l.State != core.StateAwaitingInit {
		e.t.Fatalf("state %s", l.State)
	}
	pass, err := e.svc.RevealPassword(e.ctx, admin, l.ID, "")
	if err != nil {
		e.t.Fatal(err)
	}
	e.resolveCreate(l.OwnerRole, pass, l.DBName)
	for i := 0; i < 5; i++ {
		e.svc.WatchInit(e.ctx)
	}
	l, _ = e.svc.Library(e.ctx, l.ID)
	if l.State != core.StateActive || l.TableCount != 2 {
		e.t.Fatalf("not activated: %+v", l)
	}
	return l, pass
}

func (e *env) firstBackup(l *core.Library) *core.Backup {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		// The status is "ok" one step before the L1 check writes its result.
		bs, _ := e.svc.Backups(e.ctx, core.BackupFilter{LibraryID: l.ID, Statuses: []string{core.BackupOK}})
		if len(bs) > 0 && bs[0].VerifyStatus != core.VerifyNone {
			return bs[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatal("no automatic first backup")
	return nil
}

func TestLibraryBackupRestoreLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("cliente_x")

	// Activation enqueues a first backup.
	b := e.firstBackup(l)
	if b.VerifyStatus != core.VerifyL1OK || b.TableCount != 2 || b.SizeBytes == 0 || !strings.HasPrefix(b.PGDumpVersion, "18") {
		t.Fatalf("backup %+v", b)
	}
	m, err := core.ReadManifest(strings.TrimSuffix(e.svc.AbsPath(b), ".backup") + ".manifest.json")
	if err != nil || m.RowCounts["SM_Project"] != 50 || m.RowCounts["Sm2TiItem"] != 2000 {
		t.Fatalf("manifest %+v %v", m, err)
	}
	// CREATEDB was revoked and PUBLIC cannot connect.
	conn := e.connect(l.OwnerRole, pass, "postgres")
	if _, err := conn.Exec(ctx, "CREATE DATABASE otra_cosa"); err == nil {
		t.Fatal("CREATEDB still granted")
	}
	conn.Close(ctx)

	// Level 2 verification.
	vj, _ := e.svc.Jobs.Enqueue(ctx, core.JobBackupVerify, l.ID, core.VerifyParams{BackupID: b.ID}, admin)
	e.wait(vj, jobs.Succeeded)
	b, _ = e.svc.Backup(ctx, b.ID)
	if b.VerifyStatus != core.VerifyL2OK {
		t.Fatalf("verify %s %s", b.VerifyStatus, b.VerifyDetail)
	}

	// Tampering is detected.
	tampered, _ := os.ReadFile(e.svc.AbsPath(b))
	if len(tampered) > 100 {
		orig := append([]byte(nil), tampered...)
		tampered[len(tampered)-10] ^= 0xff
		os.WriteFile(e.svc.AbsPath(b), tampered, 0o600)
		vj, _ = e.svc.Jobs.Enqueue(ctx, core.JobBackupVerify, l.ID, core.VerifyParams{BackupID: b.ID}, admin)
		e.wait(vj, jobs.Failed)
		os.WriteFile(e.svc.AbsPath(b), orig, 0o600)
		vj, _ = e.svc.Jobs.Enqueue(ctx, core.JobBackupVerify, l.ID, core.VerifyParams{BackupID: b.ID}, admin)
		e.wait(vj, jobs.Succeeded)
	}

	// Change data after the backup, then restore as a new library.
	work := e.connect(l.OwnerRole, pass, l.DBName)
	work.Exec(ctx, `DELETE FROM "SM_Project" WHERE id > 10`)
	work.Close(ctx)
	bj, err := e.svc.BackupNow(ctx, admin, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(bj, jobs.Succeeded)
	nl, nj, err := e.svc.RestoreAsNew(ctx, admin, b.ID, "cliente_x_copia", "Copia")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(nj, jobs.Succeeded)
	nl, _ = e.svc.Library(ctx, nl.ID)
	npass, _ := e.svc.RevealPassword(ctx, admin, nl.ID, "")
	if nl.State != core.StateActive || e.count(nl.OwnerRole, npass, nl.DBName, "SM_Project") != 50 {
		t.Fatalf("restored copy wrong: %+v", nl)
	}
	// Objects in the copy belong to the copy's role, which can write.
	cw := e.connect(nl.OwnerRole, npass, nl.DBName)
	if _, err := cw.Exec(ctx, `INSERT INTO "SM_Project"(name) VALUES ('nuevo')`); err != nil {
		t.Fatalf("copy not writable by its role: %v", err)
	}
	cw.Close(ctx)

	// Replace with sessions open: waits unless asked to terminate; terminate.
	open := e.connect(l.OwnerRole, pass, l.DBName)
	rj, err := e.svc.RestoreReplace(ctx, admin, b.ID, "cliente_x", true)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(rj, jobs.Succeeded)
	open.Close(ctx)
	if n := e.count(l.OwnerRole, pass, l.DBName, "SM_Project"); n != 50 {
		t.Fatalf("after replace: %d rows", n)
	}
	l, _ = e.svc.Library(ctx, l.ID)
	if l.PrevDB == "" || l.State != core.StateActive {
		t.Fatalf("prev not recorded: %+v", l)
	}
	// Revert brings back the 10-row state.
	if _, err := e.svc.Revert(ctx, admin, l.ID, "wrong"); err == nil {
		t.Fatal("revert without confirmation")
	}
	vj2, err := e.svc.Revert(ctx, admin, l.ID, "cliente_x")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(vj2, jobs.Succeeded)
	if n := e.count(l.OwnerRole, pass, l.DBName, "SM_Project"); n != 10 {
		t.Fatalf("after revert: %d rows", n)
	}

	// Trash, untrash, trash again and purge.
	tj, err := e.svc.Trash(ctx, admin, l.ID, "cliente_x")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(tj, jobs.Succeeded)
	if _, err := e.c.ConnectTCP(ctx, l.OwnerRole, pass, l.DBName); err == nil {
		t.Fatal("trashed library still reachable")
	}
	uj, _ := e.svc.RestoreFromTrash(ctx, admin, l.ID)
	e.wait(uj, jobs.Succeeded)
	if n := e.count(l.OwnerRole, pass, l.DBName, "SM_Project"); n != 10 {
		t.Fatalf("after untrash: %d", n)
	}
	tj, _ = e.svc.Trash(ctx, admin, l.ID, "cliente_x")
	e.wait(tj, jobs.Succeeded)
	pj, err := e.svc.Purge(ctx, admin, l.ID, "cliente_x")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(pj, jobs.Succeeded)
	if ok, _ := e.svc.PG.RoleExists(ctx, "cliente_x"); ok {
		t.Fatal("role survived purge")
	}
	// Slug can be reused; backups of the purged library remain.
	if err := e.svc.SlugAvailable(ctx, "cliente_x"); err != nil {
		t.Fatalf("slug not free: %v", err)
	}
	if bs, _ := e.svc.Backups(ctx, core.BackupFilter{LibraryID: l.ID, Statuses: []string{core.BackupOK}}); len(bs) == 0 {
		t.Fatal("backups lost with purge")
	}
}

func TestSwapRecoveryAfterCrash(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("crash_lib")
	b := e.firstBackup(l)
	// Simulate a crash between the two renames of a swap.
	restoreDB := pg.RestoreDB(l.Slug, "deadbeef")
	if err := e.svc.PG.CreateDatabase(ctx, restoreDB, l.OwnerRole); err != nil {
		t.Fatal(err)
	}
	prev := pg.PrevDB(l.Slug, time.Now())
	e.svc.PG.SetAllowConnections(ctx, l.DBName, false)
	if err := e.svc.PG.RenameDatabase(ctx, l.DBName, prev); err != nil {
		t.Fatal(err)
	}
	e.svc.DB.ExecContext(ctx, `INSERT INTO jobs(id, type, library_id, state, scheduled_for, created_at, created_by_type) VALUES ('J1', 'restore.replace', ?, 'running', ?, ?, 'user')`, l.ID, store.NowT(), store.NowT())
	e.svc.DB.ExecContext(ctx, `INSERT INTO job_steps(job_id, seq, name, state, started_at, data) VALUES ('J1', 1, 'intercambiar', 'running', ?, ?)`, store.NowT(),
		`{"live":"`+l.DBName+`","prev":"`+prev+`","restored":"`+restoreDB+`","phase":"rename_restored","state_before":"active"}`)
	e.svc.DB.ExecContext(ctx, `UPDATE libraries SET state = 'restoring' WHERE id = ?`, l.ID)
	interrupted, err := e.svc.Jobs.MarkInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	notes := e.svc.Reconcile(ctx, interrupted)
	if len(notes) == 0 {
		t.Fatal("no reconciliation notes")
	}
	l, _ = e.svc.Library(ctx, l.ID)
	if l.State != core.StateActive || l.PrevDB != prev {
		t.Fatalf("after reconcile: %+v notes %v", l, notes)
	}
	// The restored (empty) database is now live; the old one is kept as prev.
	if db, _ := e.svc.PG.Database(ctx, prev); db == nil {
		t.Fatal("previous database was dropped")
	}
	conn := e.connect(l.OwnerRole, pass, l.DBName)
	conn.Close(ctx)
	_ = b
}

func TestImportAndCatalogRebuild(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("importada")
	b := e.firstBackup(l)
	// Put a copy of the backup in _import and import it.
	os.MkdirAll(e.svc.ImportDir(), 0o700)
	data, _ := os.ReadFile(e.svc.AbsPath(b))
	os.WriteFile(filepath.Join(e.svc.ImportDir(), "externo.backup"), data, 0o600)
	if _, err := e.svc.Import(ctx, admin, l.ID, "../etc/passwd", false); err == nil {
		t.Fatal("path traversal accepted")
	}
	ij, err := e.svc.Import(ctx, admin, l.ID, "externo.backup", true)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(ij, jobs.Succeeded)
	bs, _ := e.svc.Backups(ctx, core.BackupFilter{LibraryID: l.ID, Statuses: []string{core.BackupOK}})
	if len(bs) != 2 || bs[0].Kind != core.KindImported {
		t.Fatalf("imported %+v", bs)
	}
	// Wipe the catalog rows and rebuild from manifests.
	e.svc.DB.ExecContext(ctx, `DELETE FROM backups`)
	e.svc.DB.ExecContext(ctx, `DELETE FROM libraries WHERE id = ?`, l.ID)
	rj, _ := e.svc.Jobs.Enqueue(ctx, core.JobCatalogRebuild, "", nil, admin)
	e.wait(rj, jobs.Succeeded)
	all, _ := e.svc.Backups(ctx, core.BackupFilter{})
	if len(all) != 2 {
		t.Fatalf("rebuilt %d backups", len(all))
	}
	for _, x := range all {
		if x.VerifyStatus != core.VerifyL1OK {
			t.Fatalf("rebuilt backup not verified: %+v", x)
		}
	}
}

func TestInitWindowExpiresAndHBA(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, j, err := e.svc.CreateLibrary(ctx, admin, core.CreateInput{DisplayName: "Vence", Slug: "vence"})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	hba, _ := os.ReadFile(filepath.Join(e.c.ConfDir, "pg_hba.conf"))
	if !strings.Contains(string(hba), "host\tvence\tvence\tall\tscram-sha-256") {
		t.Fatalf("hba:\n%s", hba)
	}
	e.svc.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	e.svc.WatchInit(ctx)
	e.svc.Now = nil
	l, _ = e.svc.Library(ctx, l.ID)
	if l.State != core.StateInitExpired {
		t.Fatalf("state %s", l.State)
	}
	if err := e.svc.ReopenInitWindow(ctx, admin, l.ID); err != nil {
		t.Fatal(err)
	}
	// Duplicate slug and reserved names are refused.
	if _, _, err := e.svc.CreateLibrary(ctx, admin, core.CreateInput{DisplayName: "x", Slug: "vence"}); err == nil {
		t.Fatal("duplicate slug")
	}
	if _, _, err := e.svc.CreateLibrary(ctx, admin, core.CreateInput{DisplayName: "x", Slug: "postgres_x"}); err == nil {
		t.Fatal("reserved slug")
	}
	// Network rules restrict the library.
	if err := e.svc.SetNetworkRules(ctx, admin, l.ID, []string{"10.9.9.0/24"}); err != nil {
		t.Fatal(err)
	}
	pass, _ := e.svc.RevealPassword(ctx, admin, l.ID, "")
	if _, err := e.c.ConnectTCP(ctx, "vence", pass, "postgres"); err == nil {
		t.Fatal("connection from 127.0.0.1 allowed by a 10.9.9.0/24 rule")
	}
	e.svc.SetNetworkRules(ctx, admin, l.ID, nil)
	conn, err := e.c.ConnectTCP(ctx, "vence", pass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
}

func TestMemberCredential(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, ownerPass := e.createActive("equipo")
	c, err := e.svc.AddMember(ctx, admin, l.ID, "ana")
	if err != nil {
		t.Fatal(err)
	}
	mpass, _ := e.svc.RevealPassword(ctx, admin, l.ID, c.ID)
	conn := e.connect(c.PGRole, mpass, l.DBName)
	if _, err := conn.Exec(ctx, `CREATE TABLE "NewTable" (id int)`); err != nil {
		t.Fatal(err)
	}
	var owner string
	conn.QueryRow(ctx, `SELECT tableowner FROM pg_tables WHERE tablename = 'NewTable'`).Scan(&owner)
	conn.Close(ctx)
	if owner != l.OwnerRole {
		t.Fatalf("object owned by %s, want %s", owner, l.OwnerRole)
	}
	if err := e.svc.RevokeMember(ctx, admin, l.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.ConnectTCP(ctx, c.PGRole, mpass, l.DBName); err == nil {
		t.Fatal("revoked member can log in")
	}
	e.connect(l.OwnerRole, ownerPass, l.DBName).Close(ctx)
}

func TestRecreateMissingKeepsPassword(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("perdida")
	e.firstBackup(l)
	// Lose the server: database and role are gone, the catalog remains.
	e.svc.PG.DropDatabase(ctx, l.DBName)
	e.svc.PG.DropRole(ctx, l.OwnerRole)
	e.svc.Reconcile(ctx, nil)
	l, _ = e.svc.Library(ctx, l.ID)
	if l.State != core.StateMissing {
		t.Fatalf("state %s", l.State)
	}
	j, err := e.svc.Recreate(ctx, admin, l.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	if n := e.count(l.OwnerRole, pass, l.DBName, "SM_Project"); n != 50 {
		t.Fatalf("recreated library has %d rows", n)
	}
}

// TestTrashAndPurgeMissingLibrary: after recovering an installation, a library
// nobody wants back can be trashed and purged although its database and roles
// are gone (SQS-L041 used to block it).
func TestTrashAndPurgeMissingLibrary(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("sobrante")
	e.firstBackup(l)
	e.svc.PG.DropDatabase(ctx, l.DBName)
	e.svc.PG.DropRole(ctx, l.OwnerRole)
	e.svc.Reconcile(ctx, nil)
	if l, _ = e.svc.Library(ctx, l.ID); l.State != core.StateMissing {
		t.Fatalf("state %s", l.State)
	}
	j, err := e.svc.Trash(ctx, admin, l.ID, l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	if l, _ = e.svc.Library(ctx, l.ID); l.State != core.StateTrashed || l.TrashDB != "" {
		t.Fatalf("after trash: %s %q", l.State, l.TrashDB)
	}
	j, err = e.svc.Purge(ctx, admin, l.ID, l.Slug)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	if l, err = e.svc.Library(ctx, l.ID); err == nil && l.State == core.StateTrashed {
		t.Fatalf("still in the trash: %+v", l)
	}
}
