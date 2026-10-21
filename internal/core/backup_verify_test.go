// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/execx"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// quiesce waits until no job is queued or running.
func (e *env) quiesce() {
	e.t.Helper()
	for i := 0; i < 1200; i++ {
		q, r, err := e.svc.Jobs.Counts(context.WithoutCancel(e.ctx))
		if err == nil && q == 0 && r == 0 && e.svc.Jobs.Busy() == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// newQuietEnv is newEnv that drains the job queue when the test ends, so
// the teardown never cancels the dispatcher in the middle of a dispatch.
func newQuietEnv(t *testing.T) *env {
	e := newEnv(t)
	t.Cleanup(e.quiesce)
	return e
}

// setManifestDBSize rewrites the database size recorded in a manifest. The
// manifest is not part of the checksum, so the backup stays valid.
func (e *env) setManifestDBSize(b *core.Backup, size int64) {
	e.t.Helper()
	mp := strings.TrimSuffix(e.svc.AbsPath(b), ".backup") + ".manifest.json"
	m, err := core.ReadManifest(mp)
	if err != nil {
		e.t.Fatal(err)
	}
	m.Server.DatabaseSize = size
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(mp, raw, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// okBackups lists the ok backups of a library, newest first; there must be
// at least one.
func (e *env) okBackups(libraryID string) []*core.Backup {
	e.t.Helper()
	bs, err := e.svc.Backups(e.ctx, core.BackupFilter{LibraryID: libraryID, Statuses: []string{core.BackupOK}})
	if err != nil || len(bs) == 0 {
		e.t.Fatalf("backups: %d %v", len(bs), err)
	}
	return bs
}

// backupNow reads a backup from the catalog.
func (e *env) backupNow(id string) *core.Backup {
	e.t.Helper()
	b, err := e.svc.Backup(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func (e *env) eventsOf(typ, libraryID string) []events.Event {
	e.t.Helper()
	evs, err := e.svc.Events.List(e.ctx, events.Filter{LibraryID: libraryID, TypePrefix: typ, Limit: 200})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []events.Event
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func (e *env) activityPolicy(l *core.Library) {
	e.t.Helper()
	pol, _ := e.svc.Policy(e.ctx, l.ID)
	pol.Mode, pol.CheckMinutes = core.ModeActivity, 15
	if err := e.svc.SetPolicy(e.ctx, admin, l.ID, pol); err != nil {
		e.t.Fatal(err)
	}
}

// TestVerifySpaceShortageKeepsBackupUsable: an L2 verification that cannot
// run for lack of space on the database volume is skipped with a notice; it
// never turns a good backup into an unusable one (audit H5, repro R5).
func TestVerifySpaceShortageKeepsBackupUsable(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("nospace")
	b := e.firstBackup(l)
	e.quiesce()
	e.setManifestDBSize(b, 1<<60)
	vj, err := e.svc.Jobs.Enqueue(ctx, core.JobBackupVerify, l.ID, core.VerifyParams{BackupID: b.ID}, admin)
	if err != nil {
		t.Fatal(err)
	}
	g := e.wait(vj, jobs.Failed)
	if g.ErrorCode != "SQS-B070" ||
		!strings.HasPrefix(g.ErrorDetail, "SQS-B070 the verification could not run: SQS-B022 not enough space on the database volume for the test restore: ") {
		t.Fatalf("job error %s: %s", g.ErrorCode, g.ErrorDetail)
	}
	got := e.backupNow(b.ID)
	if got.VerifyStatus != core.VerifyL1OK || !got.Usable() {
		t.Fatalf("a space shortage changed the verification: %s usable=%v", got.VerifyStatus, got.Usable())
	}
	if !strings.Contains(got.VerifyDetail, "could not run") || !strings.Contains(got.VerifyDetail, "SQS-B022") {
		t.Fatalf("detail not recorded: %q", got.VerifyDetail)
	}
	if lv, _ := e.svc.LatestVerified(ctx, l.ID); lv == nil || lv.ID != b.ID {
		t.Fatalf("latest verified changed: %+v", lv)
	}
	if evs := e.eventsOf(events.BackupVerifyFailed, l.ID); len(evs) != 0 {
		t.Fatalf("verify_failed emitted: %+v", evs)
	}
	skipped := e.eventsOf(core.EventVerifySkipped, l.ID)
	if len(skipped) != 1 || skipped[0].Severity != events.Warning || skipped[0].JobID != vj.ID {
		t.Fatalf("skipped events %+v", skipped)
	}
	// The backup can still be restored.
	e.setManifestDBSize(b, 1<<20)
	_, rj, err := e.svc.RestoreAsNew(ctx, admin, b.ID, "nospace_copy", "Copy")
	if err != nil {
		t.Fatalf("restore refused: %v", err)
	}
	e.wait(rj, jobs.Succeeded)
}

// TestCancelVerifyKeepsStatus: cancelling a running L2 verification is no
// verdict on the backup (audit H5, repro R6).
func TestCancelVerifyKeepsStatus(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("cancelled")
	conn := e.connect(l.OwnerRole, pass, l.DBName)
	if _, err := conn.Exec(ctx, `INSERT INTO "Sm2TiItem"(project, payload) SELECT g % 50, jsonb_build_object('x', md5(g::text)) FROM generate_series(1, 600000) g`); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	e.firstBackup(l)
	e.quiesce()
	bj, err := e.svc.BackupNow(ctx, admin, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(bj, jobs.Succeeded)
	bs := e.okBackups(l.ID)
	if len(bs) < 2 {
		t.Fatalf("%d backups", len(bs))
	}
	b := bs[0]
	vj, err := e.svc.Jobs.Enqueue(ctx, core.JobBackupVerify, l.ID, core.VerifyParams{BackupID: b.ID}, admin)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20000; i++ {
		if j, err := e.svc.Jobs.Get(ctx, vj.ID); err == nil && j.State == jobs.Running && j.CurrentStep == "test_restore" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := e.svc.Jobs.Cancel(ctx, vj.ID); err != nil {
		t.Fatal(err)
	}
	g := e.wait(vj, jobs.Cancelled)
	got := e.backupNow(b.ID)
	if got.VerifyStatus != core.VerifyL1OK || !got.Usable() {
		t.Fatalf("cancelling changed the verification: %s (%s)", got.VerifyStatus, got.VerifyDetail)
	}
	if !strings.Contains(g.ErrorDetail, "SQS-B070") {
		t.Fatalf("job error %q", g.ErrorDetail)
	}
	if evs := e.eventsOf(events.BackupVerifyFailed, l.ID); len(evs) != 0 {
		t.Fatalf("verify_failed emitted: %+v", evs)
	}
}

// TestVerifyMismatchIsVerdict: a test restore whose rows differ from the
// manifest is still a verdict: the backup is marked failed and
// backup.verify_failed is emitted.
func TestVerifyMismatchIsVerdict(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("mismatch")
	b := e.firstBackup(l)
	e.quiesce()
	mp := strings.TrimSuffix(e.svc.AbsPath(b), ".backup") + ".manifest.json"
	m, err := core.ReadManifest(mp)
	if err != nil {
		t.Fatal(err)
	}
	m.RowCounts["SM_Project"] = 999
	raw, _ := json.Marshal(m)
	os.WriteFile(mp, raw, 0o600)
	vj, err := e.svc.Jobs.Enqueue(ctx, core.JobBackupVerify, l.ID, core.VerifyParams{BackupID: b.ID}, admin)
	if err != nil {
		t.Fatal(err)
	}
	g := e.wait(vj, jobs.Failed)
	if g.ErrorCode != "SQS-R012" || !strings.Contains(g.ErrorDetail, "SM_Project: 50 instead of 999") {
		t.Fatalf("job error %s: %s", g.ErrorCode, g.ErrorDetail)
	}
	if got := e.backupNow(b.ID); got.VerifyStatus != core.VerifyFailed || got.Usable() {
		t.Fatalf("mismatch not recorded: %s", got.VerifyStatus)
	}
	if evs := e.eventsOf(events.BackupVerifyFailed, l.ID); len(evs) != 1 {
		t.Fatalf("verify_failed events %+v", evs)
	}
	if evs := e.eventsOf(core.EventVerifySkipped, l.ID); len(evs) != 0 {
		t.Fatalf("verify_skipped emitted for a verdict: %+v", evs)
	}
}

// TestVerifyL1Outcomes: only a verdict on the content of a backup marks it
// failed; a cancelled job, a missing volume or a tool that cannot start
// leave its status as it was (audit H5).
func TestVerifyL1Outcomes(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("verdict")
	b := e.firstBackup(l)
	e.quiesce()
	status := func() string { return e.backupNow(b.ID).VerifyStatus }
	notRun := func(what string, err error, cause string) {
		t.Helper()
		if err == nil || !strings.HasPrefix(err.Error(), "SQS-B070 the verification could not run: ") || !strings.Contains(err.Error(), cause) {
			t.Fatalf("%s: %v", what, err)
		}
		if st := status(); st != core.VerifyL1OK {
			t.Fatalf("%s changed the status to %s", what, st)
		}
	}
	// A cancelled job.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	err := e.svc.VerifyL1(cctx, b.ID)
	notRun("cancelled", err, "context canceled")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	// The backups volume is not mounted (its marker is gone).
	marker := filepath.Join(e.svc.Cfg.BackupDir, ".sqsolve-volume")
	os.Rename(marker, marker+".away")
	notRun("volume without marker", e.svc.VerifyL1(ctx, b.ID), "SQS-S001")
	os.Rename(marker+".away", marker)
	// pg_restore cannot be started.
	saved := e.svc.Exec
	t.Setenv("PATH", "")
	e.svc.Exec = execx.NewRunner(t.TempDir())
	if _, err := e.svc.Exec.Path("pg_restore"); err == nil {
		t.Log("pg_restore found in a well-known directory; skipping the tool-start case")
	} else {
		notRun("pg_restore missing", e.svc.VerifyL1(ctx, b.ID), "SQS-X002")
	}
	e.svc.Exec = saved
	if n := len(e.eventsOf(core.EventVerifySkipped, l.ID)); n < 2 {
		t.Fatalf("%d verify_skipped events", n)
	}
	// Verdicts: the file is gone from a mounted volume, then it is back.
	abs := e.svc.AbsPath(b)
	os.Rename(abs, abs+".away")
	err = e.svc.VerifyL1(ctx, b.ID)
	if err == nil || err.Error() != "SQS-B020 verification failed: the backup file does not exist" || status() != core.VerifyFailed {
		t.Fatalf("missing file: %v (%s)", err, status())
	}
	os.Rename(abs+".away", abs)
	if err := e.svc.VerifyL1(ctx, b.ID); err != nil || status() != core.VerifyL1OK {
		t.Fatalf("back on disk: %v (%s)", err, status())
	}
	// An archive pg_restore refuses to read. The manifest and the catalog
	// carry its checksum, so the index check is what decides.
	raw, _ := os.ReadFile(abs)
	garbage := bytes.Repeat([]byte{'x'}, len(raw))
	os.WriteFile(abs, garbage, 0o600)
	sum := sha256.Sum256(garbage)
	hexSum := hex.EncodeToString(sum[:])
	mp := strings.TrimSuffix(abs, ".backup") + ".manifest.json"
	m, _ := core.ReadManifest(mp)
	m.SHA256 = hexSum
	mb, _ := json.Marshal(m)
	os.WriteFile(mp, mb, 0o600)
	e.svc.DB.ExecContext(ctx, `UPDATE backups SET sha256 = ? WHERE id = ?`, hexSum, b.ID)
	err = e.svc.VerifyL1(ctx, b.ID)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-B020 verification failed: SQS-B041 ") || status() != core.VerifyFailed {
		t.Fatalf("garbage archive: %v (%s)", err, status())
	}
}

// TestImportReadsWholeArchive: a file cut short (still being copied) is not
// catalogued; a complete one is read in full before it is (audit M5, R7).
func TestImportReadsWholeArchive(t *testing.T) {
	defer core.SetImportSettle(100 * time.Millisecond)()
	e := newQuietEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("truncated")
	conn := e.connect(l.OwnerRole, pass, l.DBName)
	conn.Exec(ctx, `INSERT INTO "Sm2TiItem"(project, payload) SELECT g % 50, jsonb_build_object('x', md5(g::text)) FROM generate_series(1, 50000) g`)
	conn.Close(ctx)
	e.firstBackup(l)
	e.quiesce()
	bj, err := e.svc.BackupNow(ctx, admin, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(bj, jobs.Succeeded)
	full, err := os.ReadFile(e.svc.AbsPath(e.okBackups(l.ID)[0]))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(e.svc.ImportDir(), 0o700)
	cutPath := filepath.Join(e.svc.ImportDir(), "copying.backup")
	os.WriteFile(cutPath, full[:len(full)*6/10], 0o600)
	j, err := e.svc.Import(ctx, admin, l.ID, "copying.backup", true)
	if err != nil {
		t.Fatal(err)
	}
	g := e.wait(j, jobs.Failed)
	if g.ErrorCode != "SQS-B072" || !strings.HasPrefix(g.ErrorDetail, "SQS-B072 the archive is incomplete or damaged: ") {
		t.Fatalf("truncated import: %s %s", g.ErrorCode, g.ErrorDetail)
	}
	all := e.okBackups(l.ID)
	for _, x := range all {
		if x.Kind == core.KindImported {
			t.Fatalf("truncated archive catalogued: %+v", x)
		}
	}
	if !onDiskT(cutPath) {
		t.Fatal("the source was deleted although the import failed")
	}
	if left := filesUnder(t, filepath.Join(e.svc.Cfg.BackupDir, "libraries", l.Slug), l.Slug+"_"); len(left) != len(all) {
		t.Fatalf("leftovers in the library folder: %v", left)
	}
	// The complete file is read in full and catalogued as l1_ok; the source
	// goes only after that.
	os.WriteFile(cutPath, full, 0o600)
	if j, err = e.svc.Import(ctx, admin, l.ID, "copying.backup", true); err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	bs := e.okBackups(l.ID)
	if bs[0].Kind != core.KindImported || bs[0].VerifyStatus != core.VerifyL1OK || !strings.Contains(bs[0].VerifyDetail, "read in full") {
		t.Fatalf("imported %+v", bs[0])
	}
	if onDiskT(cutPath) {
		t.Fatal("source kept although delete_source was set")
	}
	if got, _ := os.ReadFile(e.svc.AbsPath(bs[0])); !bytes.Equal(got, full) {
		t.Fatal("catalogued file differs from the source")
	}
}

// TestImportRefusesFileStillBeingWritten: the size of a file in _import must
// stay the same for a moment before it is imported (audit M5).
func TestImportRefusesFileStillBeingWritten(t *testing.T) {
	defer core.SetImportSettle(400 * time.Millisecond)()
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("growing")
	b := e.firstBackup(l)
	e.quiesce()
	data, _ := os.ReadFile(e.svc.AbsPath(b))
	os.MkdirAll(e.svc.ImportDir(), 0o700)
	src := filepath.Join(e.svc.ImportDir(), "growing.backup")
	os.WriteFile(src, data, 0o600)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		f, err := os.OpenFile(src, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				f.Write([]byte("more"))
			}
		}
	}()
	j, err := e.svc.Import(ctx, admin, l.ID, "growing.backup", false)
	if err != nil {
		t.Fatal(err)
	}
	g := e.wait(j, jobs.Failed)
	close(stop)
	<-done
	if g.ErrorCode != "SQS-B071" || !strings.HasPrefix(g.ErrorDetail, "SQS-B071 growing.backup is still being written") {
		t.Fatalf("growing file: %s %s", g.ErrorCode, g.ErrorDetail)
	}
	if !onDiskT(src) {
		t.Fatal("source gone")
	}
}

// TestImportCrashKeepsSource: a cut while the imported file sits as .partial
// in the library folder must not cost the user's only copy: recovery deletes
// .partial files there, so the source in _import is never moved (audit Low).
func TestImportCrashKeepsSource(t *testing.T) {
	defer core.SetImportSettle(10 * time.Millisecond)()
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("powercut")
	b := e.firstBackup(l)
	e.quiesce()
	data, _ := os.ReadFile(e.svc.AbsPath(b))
	os.MkdirAll(e.svc.ImportDir(), 0o700)
	src := filepath.Join(e.svc.ImportDir(), "only-copy.backup")
	os.WriteFile(src, data, 0o600)
	staged := ""
	restore := core.SetImportStagedHook(func(partial string) error {
		staged = partial
		return errors.New("simulated power cut")
	})
	j, err := e.svc.Import(ctx, admin, l.ID, "only-copy.backup", true)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Failed)
	restore()
	if staged == "" {
		t.Fatal("the import never staged the file")
	}
	e.svc.Reconcile(ctx, nil)
	if onDiskT(staged) {
		t.Fatalf("recovery left %s", staged)
	}
	got, err := os.ReadFile(src)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the source in _import was lost: %v", err)
	}
}

// TestImportKeepsReplacedSource: with delete_source, a file the user puts
// under the same name while the import runs is not the one imported, so it
// stays in _import (with a warning) and the catalogued backup is the
// original.
func TestImportKeepsReplacedSource(t *testing.T) {
	defer core.SetImportSettle(10 * time.Millisecond)()
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("replaced")
	b := e.firstBackup(l)
	e.quiesce()
	data, _ := os.ReadFile(e.svc.AbsPath(b))
	os.MkdirAll(e.svc.ImportDir(), 0o700)
	src := filepath.Join(e.svc.ImportDir(), "swap.backup")
	os.WriteFile(src, data, 0o600)
	newer := []byte("the user's next file, not imported yet")
	defer core.SetImportStagedHook(func(string) error {
		tmp := src + ".tmp"
		os.WriteFile(tmp, newer, 0o600)
		return os.Rename(tmp, src)
	})()
	j, err := e.svc.Import(ctx, admin, l.ID, "swap.backup", true)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	if got, err := os.ReadFile(src); err != nil || !bytes.Equal(got, newer) {
		t.Fatalf("the replacing file was deleted or changed: %v", err)
	}
	imported := e.okBackups(l.ID)[0]
	if got, _ := os.ReadFile(e.svc.AbsPath(imported)); imported.Kind != core.KindImported || !bytes.Equal(got, data) {
		t.Fatalf("catalogued backup is not the original file: %+v", imported)
	}
	logs, _ := e.svc.Jobs.Logs(ctx, j.ID, 0)
	warned := false
	for _, lg := range logs {
		warned = warned || lg.Level == "warn" && strings.Contains(lg.Message, "Kept swap.backup in the import folder")
	}
	if !warned {
		t.Fatalf("no warning in the job log: %+v", logs)
	}
	if evs := e.eventsOf(core.EventImportSourceKept, l.ID); len(evs) != 1 || evs[0].Severity != events.Warning {
		t.Fatalf("source-kept events %+v", evs)
	}
}

// TestImportRefusesSourceWrittenInPlace: with delete_source the import
// links the source; a write into it during the import changes the
// catalogued file too, so nothing is imported and the file stays.
func TestImportRefusesSourceWrittenInPlace(t *testing.T) {
	defer core.SetImportSettle(10 * time.Millisecond)()
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("inplace")
	b := e.firstBackup(l)
	e.quiesce()
	data, _ := os.ReadFile(e.svc.AbsPath(b))
	os.MkdirAll(e.svc.ImportDir(), 0o700)
	src := filepath.Join(e.svc.ImportDir(), "growing-later.backup")
	os.WriteFile(src, data, 0o600)
	defer core.SetImportStagedHook(func(string) error {
		f, err := os.OpenFile(src, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Write([]byte("appended while importing"))
		return err
	})()
	j, err := e.svc.Import(ctx, admin, l.ID, "growing-later.backup", true)
	if err != nil {
		t.Fatal(err)
	}
	g := e.wait(j, jobs.Failed)
	if g.ErrorCode != "SQS-B074" || g.ErrorDetail != "SQS-B074 growing-later.backup changed during the import; nothing was imported and the file is still in the import folder: import it again once it no longer changes" {
		t.Fatalf("in-place write: %s %s", g.ErrorCode, g.ErrorDetail)
	}
	if got, err := os.ReadFile(src); err != nil || len(got) != len(data)+len("appended while importing") {
		t.Fatalf("source lost or changed: %v", err)
	}
	for _, x := range e.okBackups(l.ID) {
		if x.Kind == core.KindImported {
			t.Fatalf("changed file catalogued: %+v", x)
		}
	}
	if left := filesUnder(t, filepath.Join(e.svc.Cfg.BackupDir, "libraries", l.Slug), l.Slug+"_"); len(left) != len(e.okBackups(l.ID)) {
		t.Fatalf("leftovers in the library folder: %v", left)
	}
}

// TestBackupGateUsesVerifiedBackups: the activity gate compares with the
// newest usable verified backup; an unchanged library whose backup failed
// its verification gets a fresh one (audit H5).
func TestBackupGateUsesVerifiedBackups(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("gate")
	b := e.firstBackup(l)
	e.quiesce()
	e.activityPolicy(l)
	if run, why := e.svc.BackupGate(ctx, l.ID); run {
		t.Fatalf("gate open without changes: %s", why)
	}
	e.svc.DB.ExecContext(ctx, `UPDATE backups SET verify_status = 'failed' WHERE id = ?`, b.ID)
	if run, _ := e.svc.BackupGate(ctx, l.ID); !run {
		t.Fatal("gate closed although the only backup with that mark failed its verification")
	}
}

// TestActivityMarkReadBeforeSnapshot: a write that lands between the mark
// and the snapshot of a backup is in the dump and not in the mark, so the
// gate stays open; the reverse order would close the gate on a change the
// dump does not have (audit H5).
func TestActivityMarkReadBeforeSnapshot(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("mark")
	e.firstBackup(l)
	e.quiesce()
	e.activityPolicy(l)
	restore := core.SetMarkAndSnapshotHook(func() {
		w := e.connect(l.OwnerRole, pass, l.DBName)
		defer w.Close(ctx)
		w.Exec(ctx, `INSERT INTO "SM_Project"(name) VALUES ('between')`)
		w.Exec(ctx, `SELECT pg_stat_force_next_flush()`)
		w.Exec(ctx, `SELECT 1`)
	})
	j, err := e.svc.BackupNow(ctx, admin, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	restore()
	bs := e.okBackups(l.ID)
	m, err := core.ReadManifest(strings.TrimSuffix(e.svc.AbsPath(bs[0]), ".backup") + ".manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	run, _ := e.svc.BackupGate(ctx, l.ID)
	if m.RowCounts["SM_Project"] != 51 || !run {
		t.Fatalf("rows in the dump %d (want 51), gate open %v (want true)", m.RowCounts["SM_Project"], run)
	}
}

// TestRestoreRefusedWithoutSpace: a restore into a new library checks the
// database volume first, like the L2 verification (ops M3).
func TestRestoreRefusedWithoutSpace(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("noroom")
	b := e.firstBackup(l)
	e.quiesce()
	e.setManifestDBSize(b, 1<<60)
	nl, j, err := e.svc.RestoreAsNew(ctx, admin, b.ID, "noroom_copy", "Copy")
	if err != nil {
		t.Fatal(err)
	}
	g := e.wait(j, jobs.Failed)
	if g.ErrorCode != "SQS-B022" || !strings.HasPrefix(g.ErrorDetail, "SQS-B022 not enough space on the database volume for the restore: ") {
		t.Fatalf("restore error %s: %s", g.ErrorCode, g.ErrorDetail)
	}
	if db, _ := e.svc.PG.Database(ctx, "noroom_copy"); db != nil {
		t.Fatal("database created although there was no room")
	}
	if nl, err = e.svc.Library(ctx, nl.ID); err != nil || nl.State != core.StatePurged {
		t.Fatalf("new library left in %+v (%v)", nl, err)
	}
	if got := e.backupNow(b.ID); got.VerifyStatus != core.VerifyL1OK {
		t.Fatalf("source backup changed: %s", got.VerifyStatus)
	}
}

// TestExportWorkspaceRowBeforeJob: the export job finds its workspace row
// however fast it starts. The row used to be inserted after the job was
// enqueued, so a quick dispatcher failed it with SQS-E007 (flaky
// TestExportFromBackup).
func TestExportWorkspaceRowBeforeJob(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("exportrow")
	b := e.firstBackup(l)
	e.quiesce()
	st := e.svc.Settings(ctx)
	st.ExperimentalExports = true
	if err := e.svc.SaveSettings(ctx, admin, st); err != nil {
		t.Fatal(err)
	}
	// Let the job run to its end before PrepareBackupExport goes on.
	restore := core.SetExportEnqueuedHook(func(jobID string) {
		for i := 0; i < 2400; i++ {
			if j, err := e.svc.Jobs.Get(context.WithoutCancel(ctx), jobID); err == nil && j.State.Finished() {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	})
	_, j, err := e.svc.PrepareBackupExport(ctx, admin, b.ID)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	if ws, err := e.svc.BackupExportWorkspace(ctx, b.ID); err != nil || ws.State != core.WorkspaceReady || ws.JobID != j.ID {
		t.Fatalf("workspace %+v %v", ws, err)
	}
	if err := e.svc.CloseBackupExport(ctx, admin, b.ID); err != nil {
		t.Fatal(err)
	}
}

// TestReplaceNeedsRoomForRestoredCopyOnly: a replace renames the live
// database to the previous state, which keeps the space it already takes;
// the free space needed is only the restored copy.
func TestReplaceNeedsRoomForRestoredCopyOnly(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("replroom")
	b := e.firstBackup(l)
	e.quiesce()
	// The restored copy needs 118.8 MiB (99 MiB plus a fifth); 120 MiB are
	// free. Counting the live database (several MiB) on top would refuse it.
	e.setManifestDBSize(b, 99<<20)
	defer core.SetDBVolumeFree(120 << 20)()
	j, err := e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, true)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(j, jobs.Succeeded)
	// One byte less than the copy needs is refused, with the copy's size.
	e.quiesce()
	b = e.okBackups(l.ID)[0]
	e.setManifestDBSize(b, 100<<20+5)
	j, err = e.svc.RestoreReplace(ctx, admin, b.ID, l.Slug, true)
	if err != nil {
		t.Fatal(err)
	}
	g := e.wait(j, jobs.Failed)
	if g.ErrorDetail != "SQS-B022 not enough space on the database volume for the replacement: 120.0 MB needed, 120.0 MB free" {
		t.Fatalf("replace error: %s", g.ErrorDetail)
	}
}

// TestConnLimitCountsWorkstationsOnly: the per-library limit is set on the
// library roles, so sqsolve's own connections (snapshot, pg_dump) never
// count against it (audit M).
func TestConnLimitCountsWorkstationsOnly(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, pass := e.createActive("zeta")
	e.firstBackup(l)
	e.quiesce()
	if err := e.svc.UpdateLibrary(ctx, admin, l.ID, core.UpdateInput{DisplayName: l.DisplayName, ConnLimit: 2}); err != nil {
		t.Fatal(err)
	}
	pool, err := e.svc.PG.Pool()
	if err != nil {
		t.Fatal(err)
	}
	roleLimit := func(role string) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT rolconnlimit FROM pg_roles WHERE rolname = $1`, role).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	dbLimit := func() int {
		db, err := e.svc.PG.Database(ctx, l.DBName)
		if err != nil || db == nil {
			t.Fatalf("database %s: %v", l.DBName, err)
		}
		return db.ConnLimit
	}
	ws := e.connect(l.OwnerRole, pass, l.DBName) // one workstation session
	defer ws.Close(ctx)
	bj, err := e.svc.BackupNow(ctx, admin, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.wait(bj, jobs.Succeeded)
	if roleLimit(l.OwnerRole) != 2 || dbLimit() != -1 {
		t.Fatalf("role limit %d, database limit %d", roleLimit(l.OwnerRole), dbLimit())
	}
	// A member added later gets the same limit.
	c, err := e.svc.AddMember(ctx, admin, l.ID, "ana")
	if err != nil {
		t.Fatal(err)
	}
	if n := roleLimit(c.PGRole); n != 2 {
		t.Fatalf("member limit %d", n)
	}
	// A limit an older version left on the database moves to the roles at
	// startup.
	if err := e.svc.PG.SetDatabaseConnLimit(ctx, l.DBName, 2); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PG.SetRoleConnLimit(ctx, l.OwnerRole, -1); err != nil {
		t.Fatal(err)
	}
	e.svc.Reconcile(ctx, nil)
	if roleLimit(l.OwnerRole) != 2 || dbLimit() != -1 {
		t.Fatalf("after reconcile: role limit %d, database limit %d", roleLimit(l.OwnerRole), dbLimit())
	}
}

func onDiskT(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// filesUnder lists the backup files (and partials) under dir.
func filesUnder(t *testing.T, dir, contains string) []string {
	t.Helper()
	var out []string
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.Contains(filepath.Base(p), contains) && !strings.HasSuffix(p, ".manifest.json") {
			out = append(out, p)
		}
		return nil
	})
	return out
}
