// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/drp"
)

// A synthetic library with the tables and columns the exporter knows
// (docs 22), created from the model with invented values: one project in a
// folder and a PowerGrade album with one still. Nothing in it comes from a
// Resolve library.
const (
	synthProject = "9e0a0000-0000-4000-8000-000000000001"
	synthFolder  = "9e0a0000-0000-4000-8000-000000000002"
	synthGallery = "9e0a0000-0000-4000-8000-000000000030"
	synthView    = "9e0a0000-0000-4000-8000-000000000031"
	synthRef     = "9e0a0000-0000-4000-8000-000000000032"
	synthStill   = "5711a000-0000-4000-8000-000000000033"
)

func synthResolveSchema(t *testing.T) func(*pgx.Conn) {
	return func(conn *pgx.Conn) {
		t.Helper()
		ctx := context.Background()
		m, err := drp.DefaultModel()
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range m.TableNames() {
			var defs []string
			for col, typ := range m.Tables[table] {
				defs = append(defs, pgx.Identifier{col}.Sanitize()+" "+typ)
			}
			sort.Strings(defs)
			if _, err := conn.Exec(ctx, "CREATE TABLE "+pgx.Identifier{table}.Sanitize()+" ("+strings.Join(defs, ", ")+")"); err != nil {
				t.Fatalf("create %s: %v", table, err)
			}
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO "SM_ProjectFolder"("SM_ProjectFolder_id", "DbType", "Name") VALUES ($1, 'SM_ProjectFolder', 'Clients')`, []any{synthFolder}},
			{`INSERT INTO "SM_Project"("SM_Project_id", "DbType", "ProjectName", "ProjectVersion", "Folder", "LastModTimeInSecs") VALUES ($1, 'SM_Project', 'Wedding', 17, $2, 1792418582)`, []any{synthProject, synthFolder}},
			{`INSERT INTO "Gallery::GyGallery"("Gallery::GyGallery_id", "DbType") VALUES ($1, 'Gallery::GyGallery')`, []any{synthGallery}},
			{`INSERT INTO "Gallery::GyView"("Gallery::GyView_id", "DbType", "ViewName", "Gallery::GyGallery_id") VALUES ($1, 'Gallery::GyView', 'Looks', $2)`, []any{synthView, synthGallery}},
			{`INSERT INTO "Gallery::GyGallery_Gallery::GyView"("DbOwner", "DbAssociate", "DbPropertyName", "DbIndex") VALUES ($1, $2, 'Views', 0)`, []any{synthGallery, synthView}},
			{`INSERT INTO "Gallery::GyStill"("Gallery::GyStill_id", "DbType", "Label", "RecTC", "Width", "Height", "CreateTime") VALUES ($1, 'Gallery::GyStill', '0001', '01:00:00:00', 1920, 1080, '2026-10-19 13:37:24.830')`, []any{synthStill}},
			{`INSERT INTO "Gallery::GyStillRef"("Gallery::GyStillRef_id", "DbType", "Ref0", "Gallery::GyView_id", "Label") VALUES ($1, 'Gallery::GyStillRef', $2, $3, '0001')`, []any{synthRef, synthStill, synthView}},
			{`INSERT INTO "Gallery::GyStillRef_Gallery::GyView"("DbOwner", "DbAssociate", "DbPropertyName", "DbIndex") VALUES ($1, $2, 'Stills', 0)`, []any{synthView, synthRef}},
		} {
			if _, err := conn.Exec(ctx, q.sql, q.args...); err != nil {
				t.Fatalf("%s: %v", q.sql, err)
			}
		}
	}
}

// download fetches an export and returns its status, headers and body.
func download(t *testing.T, b *browser, path string) (int, http.Header, []byte) {
	t.Helper()
	resp, err := b.c.Get(b.base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

// Downloads of .drx and album zips from a live library and from a backup
// prepared for export: the catalog lists what the library holds, each file
// comes with its name and its "ok" audit entry, nothing is sent when that
// entry cannot be written (docs 08 §9), and closing the workspace drops its
// temporary database.
func TestExportDownloadsNeedTheirAuditEntry(t *testing.T) {
	a, srv, c := fxPGPanel(t)
	ctx := context.Background()
	boss, tok := jobsLogin(t, srv, "boss", jobsAdminPass)
	l := fxActiveLibrary(t, a, c, boss, tok, "Apolo", "apolo", synthResolveSchema(t))
	st := a.Svc.Settings(ctx)
	st.ExperimentalExports = true
	if err := a.Svc.SaveSettings(ctx, audit.System, st); err != nil {
		t.Fatal(err)
	}
	op, opTok := jobsLogin(t, srv, "op", jobsOpPass)
	base := "/libraries/" + l.ID + "/export"
	if page := op.page(base); !strings.Contains(page, "Wedding") || !strings.Contains(page, "Clients") || !strings.Contains(page, "Not verified with Resolve") {
		t.Fatal("the projects view does not list the project, its folder or the schema state")
	}
	if page := op.page(base + "?view=stills"); !strings.Contains(page, "Looks") || !strings.Contains(page, `href="`+base+`/stills/`+synthStill+`"`) ||
		!strings.Contains(page, "01:00:00:00") {
		t.Fatal("the stills view does not list the album and its still")
	}

	stillName := "0001-" + synthStill[:8] + ".drx"
	code, h, body := download(t, op, base+"/stills/"+synthStill)
	if code != http.StatusOK || h.Get("Content-Disposition") != `attachment; filename="`+stillName+`"; filename*=UTF-8''`+stillName ||
		h.Get("Cache-Control") != "no-store" || h.Get("Content-Length") == "" ||
		!bytes.HasPrefix(body, []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Gallery::GyStill DbId=\""+synthStill+"\">")) {
		t.Fatalf("still: %d %v\n%.200s", code, h, body)
	}
	code, h, body = download(t, op, base+"/albums/"+strings.ToUpper(synthView))
	if code != http.StatusOK || !strings.Contains(h.Get("Content-Disposition"), `filename="apolo_Looks.zip"`) {
		t.Fatalf("album: %d %v", code, h)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil || len(zr.File) != 1 || zr.File[0].Name != stillName {
		t.Fatalf("album zip: %v %+v", err, zr)
	}
	es := auditEntries(t, a, "library.export")
	if len(es) != 2 || es[1].Details["kind"] != "still" || es[1].Details["name"] != "0001" || es[0].Details["kind"] != "album" ||
		es[0].Details["id"] != synthView || es[0].Actor.Name != "op" || es[0].Outcome != audit.OK {
		t.Fatalf("export audit %+v", es)
	}
	if code, _, body := download(t, op, base+"/stills/5711a000-0000-4000-8000-0000000000ff"); code != http.StatusNotFound ||
		!strings.Contains(string(body), "SQS-E003 does not exist in the library: still 5711a000-0000-4000-8000-0000000000ff") {
		t.Fatalf("unknown still: %d", code)
	}
	if code, _, body := download(t, op, base+"/albums/not-an-id"); code != http.StatusNotFound || !strings.Contains(string(body), "SQS-E003 does not exist in the library: invalid album identifier") {
		t.Fatalf("invalid album id: %d", code)
	}
	fxAuditDown(t, a)
	for _, p := range []string{"/stills/" + synthStill, "/albums/" + synthView} {
		code, _, body := download(t, op, base+p)
		if code != http.StatusServiceUnavailable || !strings.Contains(string(body), fxAuditDownErr) || bytes.Contains(body, []byte("Gallery::GyStill DbId")) || bytes.HasPrefix(body, []byte("PK")) {
			t.Errorf("%s without its audit entry: %d", p, code)
		}
	}
	fxAuditUp(t, a)

	// The same from a backup of the library.
	_, jl, _ := boss.do("POST", "/libraries/"+l.ID+"/backup", url.Values{"csrf": {tok}})
	waitJob(t, a, strings.TrimPrefix(jl, "/jobs/"))
	bj, _ := a.Jobs.Get(ctx, strings.TrimPrefix(jl, "/jobs/"))
	var res struct {
		BackupID string `json:"backup_id"`
	}
	json.Unmarshal(bj.Result, &res)
	b, err := a.Svc.Backup(ctx, res.BackupID)
	if err != nil {
		t.Fatalf("backup of job %s: %v", bj.ID, err)
	}
	_, jp, _ := op.do("POST", "/backups/"+b.ID+"/export/prepare", url.Values{"csrf": {opTok}})
	waitJob(t, a, strings.TrimPrefix(jp, "/jobs/"))
	bbase := "/backups/" + b.ID + "/export"
	if page := op.page(bbase + "?view=stills"); !strings.Contains(page, "Looks") || !strings.Contains(page, `href="`+bbase+`/stills/`+synthStill+`"`) {
		t.Fatal("the backup export does not list the album")
	}
	if page := op.page(bbase + "?q=wed"); !strings.Contains(page, "Wedding") {
		t.Fatal("the backup export does not find the project")
	}
	if code, h, body := download(t, op, bbase+"/stills/"+synthStill); code != http.StatusOK || !strings.Contains(h.Get("Content-Disposition"), `filename="`+stillName+`"`) ||
		!bytes.Contains(body, []byte(`<Gallery::GyStill DbId="`+synthStill+`">`)) {
		t.Fatalf("still from the backup: %d %v", code, h)
	}
	albumName := "apolo_" + b.StartedAt.UTC().Format("20060102T1504") + "_Looks.zip"
	if code, h, _ := download(t, op, bbase+"/albums/"+synthView); code != http.StatusOK || !strings.Contains(h.Get("Content-Disposition"), `filename="`+albumName+`"`) {
		t.Fatalf("album from the backup: %d %v, want %s", code, h, albumName)
	}
	if es := auditEntries(t, a, "backup.export"); len(es) != 2 || es[0].TargetID != b.ID || es[0].Details["library_id"] != l.ID || es[0].Details["kind"] != "album" {
		t.Fatalf("backup export audit %+v", es)
	}
	fxAuditDown(t, a)
	if code, _, body := download(t, op, bbase+"/stills/"+synthStill); code != http.StatusServiceUnavailable || !strings.Contains(string(body), fxAuditDownErr) ||
		bytes.Contains(body, []byte("Gallery::GyStill DbId")) {
		t.Fatalf("still from the backup without its audit entry: %d", code)
	}
	fxAuditUp(t, a)
	ws, _ := a.Svc.BackupExportWorkspace(ctx, b.ID)
	if _, loc, msg := fxPost(op, bbase+"/close", url.Values{"csrf": {opTok}}); loc != "/backups/"+b.ID || msg != "The export was closed and the temporary database was deleted." {
		t.Fatalf("close: %s %q", loc, msg)
	}
	super, err := c.Connect(ctx, "postgres", c.SuperPass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close(ctx)
	var n int
	super.QueryRow(ctx, `SELECT count(*) FROM pg_database WHERE datname = $1`, ws.DBName).Scan(&n)
	if n != 0 {
		t.Fatal("the temporary database is still there")
	}
	if code, _, body := download(t, op, bbase+"/stills/"+synthStill); code != http.StatusConflict || !strings.Contains(string(body), "SQS-E007") {
		t.Fatalf("download after closing: %d", code)
	}
}
