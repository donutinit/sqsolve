// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/core"
)

// Verifying a backup queues its job for whoever may verify; pinning it
// keeps it from retention and is for administrators. A backup that does
// not exist is a 404 with its code.
func TestBackupVerifyAndPinForms(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	fxViewer(t, a)
	l := fxLibrary(t, a, "apolo", core.StateActive)
	id := fxBackup(t, a, l.ID, core.BackupOK, core.VerifyL1OK, time.Now().Add(-2*time.Hour))
	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	st, loc, _ := op.do("POST", "/backups/"+id+"/verify", url.Values{"csrf": {opTok}})
	j, err := a.Jobs.Get(ctx, strings.TrimPrefix(loc, "/jobs/"))
	if st != http.StatusSeeOther || err != nil || j.Type != core.JobBackupVerify || j.LibraryID != l.ID || !strings.Contains(string(j.Params), id) || j.CreatedBy.Name != "op" {
		t.Fatalf("verify: %d %s %+v", st, loc, j)
	}
	if st, _, body := op.do("POST", "/backups/01NOSUCHBACKUP000000000000/verify", url.Values{"csrf": {opTok}}); st != http.StatusNotFound || !strings.Contains(body, "SQS-B001 Backup not found.") {
		t.Fatalf("verify of an unknown backup: %d", st)
	}
	eve, eveTok := jobsLogin(t, srv, "eve", fxViewerPass)
	if st, _, body := eve.do("POST", "/backups/"+id+"/verify", url.Values{"csrf": {eveTok}}); st != http.StatusForbidden || !strings.Contains(body, fxRoleRefused) {
		t.Fatalf("viewer verify: %d", st)
	}

	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	if _, loc, msg := fxPost(boss, "/backups/"+id+"/pin", url.Values{"csrf": {tok}, "pin": {"1"}}); loc != "/backups/"+id || msg != "Backup pinned: retention will not delete it." {
		t.Fatalf("pin: %s %q", loc, msg)
	}
	if b, _ := a.Svc.Backup(ctx, id); !b.Pinned {
		t.Fatal("backup not pinned")
	}
	if es := auditEntries(t, a, "backup.pin"); len(es) != 1 || es[0].Outcome != audit.OK || es[0].Details["pinned"] != true || es[0].Actor.Name != "boss" {
		t.Fatalf("pin audit %+v", es)
	}
	if st, _, body := op.do("POST", "/backups/"+id+"/pin", url.Values{"csrf": {opTok}, "pin": {"0"}}); st != http.StatusForbidden || !strings.Contains(body, fxRoleRefused) {
		t.Fatalf("operator unpin: %d", st)
	}
	if _, _, msg := fxPost(boss, "/backups/"+id+"/pin", url.Values{"csrf": {tok}, "pin": {"0"}}); msg != "Backup unpinned: retention treats it like the others." {
		t.Fatalf("unpin: %q", msg)
	}
	if b, _ := a.Svc.Backup(ctx, id); b.Pinned {
		t.Fatal("backup still pinned")
	}
	if page := boss.page("/backups/" + id); !strings.Contains(page, `action="/backups/`+id+`/pin"`) {
		t.Error("the backup page has no pin form")
	}
	if page := boss.page("/backups?library=" + l.ID + "&state=ok"); !strings.Contains(page, `href="/backups/`+id+`"`) {
		t.Error("the filtered catalog does not list the backup")
	}
	if page := boss.page("/backups?state=failed"); strings.Contains(page, `href="/backups/`+id+`"`) {
		t.Error("the failed filter lists a good backup")
	}
}

// uploadImport posts the import form as multipart, with the CSRF token as
// the first part like the browser form, and returns where the panel sent
// the browser and its notice.
func uploadImport(t *testing.T, b *browser, tok string, fields [][2]string, file string, data []byte) (string, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("csrf", tok)
	for _, f := range fields {
		mw.WriteField(f[0], f[1])
	}
	if file != "" {
		fw, _ := mw.CreateFormFile("upload", file)
		fw.Write(data)
	}
	mw.Close()
	u, _ := url.Parse(b.base)
	b.c.Jar.SetCookies(u, []*http.Cookie{{Name: "sqsolve_flash", Value: "", Path: "/", MaxAge: -1}})
	req, _ := http.NewRequest("POST", b.base+"/backups/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload: %d", resp.StatusCode)
	}
	return resp.Header.Get("Location"), flashOf(b)
}

// Importing a backup, from the import folder or by upload: each refusal
// carries its code, an uploaded file never stays in the import folder when
// its job is not queued (a refusal or the audit log down), the size limit
// of the settings applies to uploads, and only administrators import.
func TestImportForms(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	l := fxLibrary(t, a, "apolo", core.StateActive)
	dir := a.Svc.ImportDir()
	os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, "studio.backup"), []byte("PGDMP archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	if page := boss.page("/backups/import?library=" + l.ID); !strings.Contains(page, "studio.backup") || !strings.Contains(page, `value="`+l.ID+`" selected`) {
		t.Fatal("the import page does not list the waiting file or the chosen library")
	}
	if page := boss.page("/backups"); !strings.Contains(page, `href="/backups/import"`) {
		t.Fatal("the catalog does not point to the waiting import")
	}
	for _, c := range []struct {
		form url.Values
		msg  string
	}{
		{url.Values{"library": {l.ID}, "file": {"../etc/passwd"}}, "SQS-B050 invalid file name"},
		{url.Values{"library": {l.ID}, "file": {""}}, "SQS-B050 invalid file name"},
		{url.Values{"library": {"01NOSUCHLIBRARY00000000000"}, "file": {"studio.backup"}}, "SQS-L002 library not found"},
	} {
		c.form.Set("csrf", tok)
		if _, loc, msg := fxPost(boss, "/backups/import", c.form); loc != "/backups/import" || msg != c.msg {
			t.Errorf("import %v: %s %q, want %q", c.form, loc, msg, c.msg)
		}
	}
	_, loc, _ := boss.do("POST", "/backups/import", url.Values{"csrf": {tok}, "library": {l.ID}, "file": {"studio.backup"}, "delete": {"on"}})
	j := fxQueuedJob(t, a, loc, core.JobBackupImport, l.ID, "backup.import", "boss")
	if string(j.Params) != `{"path":"studio.backup","delete_source":true}` {
		t.Fatalf("import params %s", j.Params)
	}

	// Uploads.
	inFolder := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	lib := [2]string{"library", l.ID}
	for _, c := range []struct {
		fields [][2]string
		file   string
		msg    string
	}{
		{[][2]string{lib}, "", "SQS-B050 choose a file from the import folder or upload one"},
		{[][2]string{lib}, ".hidden.backup", "SQS-B050 invalid file name"},
		{[][2]string{lib}, "half.backup.partial", "SQS-B050 invalid file name"},
		{[][2]string{lib}, "studio.backup", "SQS-B054 there is already a file named studio.backup in the import folder; import it from the list or rename it"},
		{[][2]string{{"library", "01NOSUCHLIBRARY00000000000"}}, "orphan.backup", "SQS-L002 library not found"},
	} {
		if loc, msg := uploadImport(t, boss, tok, c.fields, c.file, []byte("PGDMP upload")); loc != "/backups/import" || msg != c.msg {
			t.Errorf("upload %q: %s %q, want %q", c.file, loc, msg, c.msg)
		}
	}
	if inFolder("orphan.backup") || inFolder(".hidden.backup") {
		t.Fatal("a refused upload stayed in the import folder")
	}
	fxAuditDown(t, a)
	if _, msg := uploadImport(t, boss, tok, [][2]string{lib}, "night.backup", []byte("PGDMP upload")); msg != fxAuditDownErr {
		t.Fatalf("upload without its audit entry: %q", msg)
	}
	if inFolder("night.backup") {
		t.Fatal("the upload stayed in the import folder without its job")
	}
	fxAuditUp(t, a)
	// A folder path in the file name is dropped, and the job deletes the
	// transfer copy when it ends.
	loc, _ = uploadImport(t, boss, tok, [][2]string{lib}, `C:\Users\ana\night.backup`, []byte("PGDMP upload"))
	j = fxQueuedJob(t, a, loc, core.JobBackupImport, l.ID, "backup.import", "boss")
	if string(j.Params) != `{"path":"night.backup","delete_source":true}` {
		t.Fatalf("upload params %s", j.Params)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "night.backup")); err != nil || string(b) != "PGDMP upload" {
		t.Fatalf("uploaded file: %q %v", b, err)
	}

	st := a.Svc.Settings(ctx)
	st.ImportMaxMB = 1
	if err := a.Svc.SaveSettings(ctx, audit.System, st); err != nil {
		t.Fatal(err)
	}
	if _, msg := uploadImport(t, boss, tok, [][2]string{lib}, "big.backup", bytes.Repeat([]byte("x"), 2<<20)); msg != "SQS-B052 the upload was interrupted: SQS-C060 The request body is too large" {
		t.Fatalf("upload over the limit: %q", msg)
	}
	if inFolder("big.backup") || inFolder("big.backup.partial") {
		t.Fatal("an upload over the limit left a file behind")
	}

	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	if st, _, _ := op.do("GET", "/backups/import", nil); st != http.StatusForbidden {
		t.Fatalf("operator import page: %d", st)
	}
	if st, _, body := op.do("POST", "/backups/import", url.Values{"csrf": {opTok}, "library": {l.ID}, "file": {"studio.backup"}}); st != http.StatusForbidden || !strings.Contains(body, fxRoleRefused) {
		t.Fatalf("operator import: %d", st)
	}
}

// Restoring by replacing is refused to a role that only restores as a
// copy, asks for the identifier and a library that can be replaced, and
// queues its job with the "accepted" entry; a restore as a copy validates
// its name and identifier before anything is created.
func TestRestoreFormRefusals(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	l := fxLibrary(t, a, "apolo", core.StateActive)
	waiting := fxLibrary(t, a, "zeus", core.StateAwaitingInit)
	good := fxBackup(t, a, l.ID, core.BackupOK, core.VerifyL1OK, time.Now().Add(-3*time.Hour))
	bad := fxBackup(t, a, l.ID, core.BackupFailed, core.VerifyNone, time.Now().Add(-2*time.Hour))
	other := fxBackup(t, a, waiting.ID, core.BackupOK, core.VerifyL1OK, time.Now().Add(-time.Hour))
	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	if st, _, body := op.do("POST", "/backups/"+good+"/restore", url.Values{"csrf": {opTok}, "mode": {"replace"}, "confirm": {"apolo"}}); st != http.StatusForbidden ||
		!strings.Contains(body, "SQS-A030 Your role does not allow replacing a library.") {
		t.Fatalf("operator replace: %d", st)
	}
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	// Resolve reconnects after the swap and saves what it has open
	// (acceptance 1.2.2), so the replace form always says to close it first.
	if page := boss.page("/backups/" + good + "/restore?mode=replace"); !strings.Contains(page, "Close the project in Resolve on every workstation first.") {
		t.Error("the replace form does not ask to close the project in Resolve")
	}
	for _, c := range []struct {
		backup string
		form   url.Values
		loc    string
		msg    string
	}{
		{good, url.Values{"mode": {"replace"}, "confirm": {"Apolo"}}, "/backups/" + good + "/restore?mode=replace", "SQS-R002 type the library identifier to confirm"},
		{bad, url.Values{"mode": {"replace"}, "confirm": {"apolo"}}, "/backups/" + bad + "/restore?mode=replace", "SQS-R001 the backup cannot be restored (it failed or was deleted)"},
		{other, url.Values{"mode": {"replace"}, "confirm": {"zeus"}}, "/backups/" + other + "/restore?mode=replace", "SQS-R003 cannot replace a library in state Waiting for Resolve"},
		{"01NOSUCHBACKUP000000000000", url.Values{"mode": {"replace"}, "confirm": {"apolo"}}, "/backups/01NOSUCHBACKUP000000000000/restore?mode=replace", "SQS-B001 backup not found"},
		{bad, url.Values{"mode": {"new"}, "slug": {"apolo_copy"}, "name": {"Copy"}}, "/backups/" + bad + "/restore", "SQS-R001 the backup cannot be restored (it failed or was deleted)"},
		{good, url.Values{"mode": {"new"}, "slug": {"apolo_copy"}, "name": {" "}}, "/backups/" + good + "/restore", "SQS-L003 the display name must be 1 to 80 characters long"},
		{good, url.Values{"mode": {"new"}, "slug": {"zeus"}, "name": {"Copy"}}, "/backups/" + good + "/restore", `SQS-L004 a library with identifier "zeus" already exists`},
	} {
		c.form.Set("csrf", tok)
		if _, loc, msg := fxPost(boss, "/backups/"+c.backup+"/restore", c.form); loc != c.loc || msg != c.msg {
			t.Errorf("restore %v: %s %q, want %s %q", c.form, loc, msg, c.loc, c.msg)
		}
	}
	fxAuditDown(t, a)
	if _, _, msg := fxPost(boss, "/backups/"+good+"/restore", url.Values{"csrf": {tok}, "mode": {"replace"}, "confirm": {"apolo"}}); msg != fxAuditDownErr {
		t.Fatalf("replace without its audit entry: %q", msg)
	}
	if n := jobCount(t, a); n != 0 {
		t.Fatalf("%d jobs queued by refused restores", n)
	}
	fxAuditUp(t, a)
	_, loc, _ := boss.do("POST", "/backups/"+good+"/restore", url.Values{"csrf": {tok}, "mode": {"replace"}, "confirm": {" apolo "}, "terminate": {"on"}})
	j := fxQueuedJob(t, a, loc, core.JobRestoreReplace, l.ID, "restore.replace", "boss")
	if !strings.Contains(string(j.Params), `"backup_id":"`+good+`"`) || !strings.Contains(string(j.Params), `"terminate_sessions":true`) {
		t.Fatalf("replace params %s", j.Params)
	}
}
