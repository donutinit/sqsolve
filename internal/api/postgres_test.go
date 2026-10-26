// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/config"
	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/drp"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/testpg"
)

// pgServer is apiServer on a throwaway PostgreSQL cluster (internal/testpg)
// with the job workers running, so queued jobs run as in production. The
// other background loops (scheduler, initialization watcher, monitor) are
// not started: the tests drive what they need. It skips the test when
// PostgreSQL is not installed.
func pgServer(t *testing.T) (*apiEnv, *testpg.Cluster) {
	t.Helper()
	c := testpg.Start(t)
	ctx, cancel := context.WithCancel(context.Background())
	base := t.TempDir()
	cfg := &config.Config{AppDir: filepath.Join(base, "app"), BackupDir: filepath.Join(base, "backups"), PGDir: c.Base, PGDataDir: c.DataDir,
		PGSocket: c.SocketDir, PGPort: c.Port, PGListen: "127.0.0.1", PGBinDir: c.BinDir, PGMaxConnections: 50, PGMemoryMB: 256,
		UID: os.Getuid(), GID: os.Getgid(), PGUID: os.Getuid(), PGGID: os.Getgid(), LogLevel: "warn", LogFormat: "text",
		Workers: 2, HeavyJobs: 1, Location: time.UTC, InsecureHTTP: true, Listen: "127.0.0.1:0"}
	if err := os.WriteFile(cfg.SuperuserPasswordFile(), []byte(c.SuperPass.Reveal()+"\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := app.Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.PG.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.PG.Open(ctx); err != nil {
		t.Fatal(err)
	}
	a.Jobs.Start(ctx)
	t.Cleanup(func() {
		a.Jobs.Shutdown(10 * time.Second)
		cancel()
	})
	if _, err := a.Auth.CreateUser(ctx, audit.System, "boss", "", "tungsten light 3200K", auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	raw, p, _, err := a.Auth.Login(ctx, "boss", "tungsten light 3200K", "127.0.0.1", "")
	if err != nil {
		t.Fatal(err)
	}
	e := &apiEnv{a: a, cookie: raw, csrfTok: p.CSRF}
	if e.admin, _, err = a.Auth.CreateToken(ctx, p, "admin", []string{"admin"}, 0); err != nil {
		t.Fatal(err)
	}
	if e.read, _, err = a.Auth.CreateToken(ctx, p, "read", []string{"read"}, 0); err != nil {
		t.Fatal(err)
	}
	m := http.NewServeMux()
	(&API{A: a}).Register(m)
	e.srv = httptest.NewServer(m)
	t.Cleanup(e.srv.Close)
	return e, c
}

// waitJob waits for a job to end and fails the test unless it succeeded.
func (e *apiEnv) waitJob(t *testing.T, id string) {
	t.Helper()
	ctx := context.Background()
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		j, err := e.a.Jobs.Get(ctx, id)
		if err != nil || !j.State.Finished() {
			continue
		}
		if j.State != jobs.Succeeded {
			logs, _ := e.a.Jobs.Logs(ctx, id, 0)
			t.Fatalf("job %s %s: %s\n%+v", j.Type, j.State, j.ErrorDetail, logs)
		}
		return
	}
	t.Fatalf("job %s did not end", id)
}

type createdLibrary struct {
	Library LibraryDTO `json:"library"`
	Job     jobs.Job   `json:"job"`
}

// activeLibrary creates a library through the API and initializes it as
// Resolve would: its owner role creates the database and, with schema, its
// tables; the initialization watcher then sees them stay. It returns the
// library and the owner's password.
func (e *apiEnv) activeLibrary(t *testing.T, c *testpg.Cluster, name, slug string, schema func(*pgx.Conn) error) (LibraryDTO, string) {
	t.Helper()
	ctx := context.Background()
	r := e.call(t, "POST", "/api/v1/libraries", e.admin, fmt.Sprintf(`{"display_name":%q,"slug":%q}`, name, slug), false)
	var cr createdLibrary
	decodeInto(t, r, &cr)
	if r.status != 202 || r.header.Get("Location") != "/api/v1/jobs/"+cr.Job.ID || cr.Library.State != "provisioning" || cr.Library.Slug != slug {
		t.Fatalf("create %s: %d %s", slug, r.status, r.body)
	}
	e.waitJob(t, cr.Job.ID)
	r = e.call(t, "POST", "/api/v1/libraries/"+cr.Library.ID+"/connection/reveal", e.admin, "", false)
	var cred struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	decodeInto(t, r, &cred)
	conn, err := c.ConnectTCP(ctx, cred.User, secrets.New(cred.Password), "postgres")
	if err != nil {
		t.Fatalf("owner login: %v", err)
	}
	_, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{slug}.Sanitize())
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if conn, err = c.ConnectTCP(ctx, cred.User, secrets.New(cred.Password), slug); err != nil {
		t.Fatal(err)
	}
	err = schema(conn)
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		e.a.Svc.WatchInit(ctx)
		if l, _ := e.a.Svc.Library(ctx, cr.Library.ID); l.State == core.StateActive {
			var dto LibraryDTO
			decodeInto(t, e.call(t, "GET", "/api/v1/libraries/"+l.ID, e.read, "", false), &dto)
			return dto, cred.Password
		}
	}
	t.Fatalf("%s was not activated", slug)
	return LibraryDTO{}, ""
}

// Object ids of the synthetic library.
const (
	idFolder   = "00000000-0000-4000-8000-000000000001"
	idProject  = "00000000-0000-4000-8000-000000000002"
	idNoPool   = "00000000-0000-4000-8000-000000000003"
	idGallery  = "00000000-0000-4000-8000-000000000010"
	idView     = "00000000-0000-4000-8000-000000000011"
	idRef      = "00000000-0000-4000-8000-000000000012"
	idStill    = "00000000-0000-4000-8000-000000000013"
	idGallery2 = "00000000-0000-4000-8000-000000000020"
	idView2    = "00000000-0000-4000-8000-000000000021"
	idRef2     = "00000000-0000-4000-8000-000000000022"
	idStill2   = "00000000-0000-4000-8000-000000000023"
)

// modelSchema creates every table and column the experimental export reads
// (the drp model) and fills one project in a project folder, with its
// gallery album, and a PowerGrade album; each album holds one still. The
// values are invented: nothing comes from a Resolve library.
func modelSchema(conn *pgx.Conn) error {
	ctx := context.Background()
	m, err := drp.DefaultModel()
	if err != nil {
		return err
	}
	for _, table := range m.TableNames() {
		var defs []string
		for col, typ := range m.Tables[table] {
			defs = append(defs, pgx.Identifier{col}.Sanitize()+" "+typ)
		}
		sort.Strings(defs)
		if _, err := conn.Exec(ctx, "CREATE TABLE "+pgx.Identifier{table}.Sanitize()+" ("+strings.Join(defs, ", ")+")"); err != nil {
			return fmt.Errorf("create %s: %w", table, err)
		}
	}
	created := time.Date(2026, 10, 19, 13, 37, 24, 830_000_000, time.UTC)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO "SM_ProjectFolder"("SM_ProjectFolder_id", "DbType", "Name") VALUES ($1, 'SM_ProjectFolder', 'Clients')`, []any{idFolder}},
		// The media pool it names does not exist: the export skips it with a note.
		{`INSERT INTO "SM_Project"("SM_Project_id", "DbType", "ProjectName", "ProjectVersion", "Folder", "MediaPool", "LastModTimeInSecs")
			VALUES ($1, 'SM_Project', 'First Look', 17, $2, $3, 1792418582)`, []any{idProject, idFolder, idNoPool}},
		{`INSERT INTO "Gallery::GyGallery"("Gallery::GyGallery_id", "DbType", "SM_Project_id") VALUES ($1, 'Gallery::GyGallery', $2), ($3, 'Gallery::GyGallery', NULL)`,
			[]any{idGallery, idProject, idGallery2}},
		{`INSERT INTO "Gallery::GyView"("Gallery::GyView_id", "DbType", "ViewName", "Gallery::GyGallery_id") VALUES ($1, 'Gallery::GyView', 'current', $2), ($3, 'Gallery::GyView', 'PowerGrade', $4)`,
			[]any{idView, idGallery, idView2, idGallery2}},
		{`INSERT INTO "Gallery::GyGallery_Gallery::GyView"("DbOwner", "DbAssociate", "DbPropertyName", "DbIndex") VALUES ($1, $2, 'Views', 0), ($3, $4, 'Views', 0)`,
			[]any{idGallery, idView, idGallery2, idView2}},
		{`INSERT INTO "Gallery::GyStill"("Gallery::GyStill_id", "DbType", "Label", "RecTC", "Width", "Height", "CreateTime")
			VALUES ($1, 'Gallery::GyStill', '1.1', '01:00:00:00', 1920, 1080, $2), ($3, 'Gallery::GyStill', '2.1', '02:00:00:00', 3840, 2160, $2)`,
			[]any{idStill, created, idStill2}},
		{`INSERT INTO "Gallery::GyStillRef"("Gallery::GyStillRef_id", "DbType", "Ref0", "Gallery::GyView_id", "Label") VALUES ($1, 'Gallery::GyStillRef', $2, $3, '1.1'), ($4, 'Gallery::GyStillRef', $5, $6, '2.1')`,
			[]any{idRef, idStill, idView, idRef2, idStill2, idView2}},
		{`INSERT INTO "Gallery::GyStillRef_Gallery::GyView"("DbOwner", "DbAssociate", "DbPropertyName", "DbIndex") VALUES ($1, $2, 'Stills', 0), ($3, $4, 'Stills', 0)`,
			[]any{idView, idRef, idView2, idRef2}},
	} {
		if _, err := conn.Exec(ctx, q.sql, q.args...); err != nil {
			return fmt.Errorf("%s: %w", q.sql, err)
		}
	}
	return nil
}

// Without Resolve's tables a library is still a library: a few tables with rows.
func plainSchema(conn *pgx.Conn) error {
	_, err := conn.Exec(context.Background(), `CREATE TABLE "SM_Project" (id serial primary key, name text);
		INSERT INTO "SM_Project"(name) SELECT 'p' || g FROM generate_series(1, 20) g`)
	return err
}

// The life of a library through the API on a real PostgreSQL: create (and
// the refusals of a taken identifier and an unknown strategy), sessions,
// maintenance that ends the sessions of the library roles, a connection
// limit applied to the roles, back up now, the download of that backup,
// verify, restore as new and restore by replacing.
func TestAPILibraryLifecycleWithPostgreSQL(t *testing.T) {
	e, c := pgServer(t)
	ctx := context.Background()
	l, pass := e.activeLibrary(t, c, "Wedding Ana", "wedding", plainSchema)
	if l.State != "active" || l.Tables != 1 || l.Fingerprint == "" {
		t.Fatalf("activated library: %+v", l)
	}
	wantProblem(t, "taken identifier", e.call(t, "POST", "/api/v1/libraries", e.admin, `{"display_name":"Again","slug":"wedding"}`, false),
		422, `SQS-L004 a library with identifier "wedding" already exists`)
	wantProblem(t, "unknown strategy", e.call(t, "POST", "/api/v1/libraries", e.admin, `{"display_name":"Other","slug":"other","strategy":"clone"}`, false),
		422, "SQS-L007 unknown creation strategy")

	// A workstation is connected: the sessions show it, and maintenance with
	// terminate_sessions ends it and refuses new logins.
	ws, err := c.ConnectTCP(ctx, "wedding", secrets.New(pass), "wedding")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close(ctx)
	r := e.call(t, "GET", "/api/v1/libraries/"+l.ID+"/sessions", e.read, "", false)
	var ss struct {
		Items []struct {
			PID  int32  `json:"pid"`
			User string `json:"user"`
		} `json:"items"`
	}
	decodeInto(t, r, &ss)
	// A backup may have its own session on the database meanwhile.
	owners := 0
	for _, s := range ss.Items {
		if s.User == "wedding" && s.PID != 0 {
			owners++
		}
	}
	if r.status != 200 || owners != 1 {
		t.Fatalf("sessions: %d %s", r.status, r.body)
	}
	r = e.call(t, "POST", "/api/v1/libraries/"+l.ID+"/maintenance", e.admin, `{"enabled":true,"terminate_sessions":true}`, false)
	var mt struct {
		Maintenance bool `json:"maintenance"`
		Terminated  int  `json:"terminated_sessions"`
	}
	decodeInto(t, r, &mt)
	if r.status != 200 || !mt.Maintenance || mt.Terminated != 1 || ws.Ping(ctx) == nil {
		t.Fatalf("maintenance on: %d %s", r.status, r.body)
	}
	if conn, err := c.ConnectTCP(ctx, "wedding", secrets.New(pass), "wedding"); err == nil {
		conn.Close(ctx)
		t.Fatal("the owner logged in during maintenance")
	}
	if !e.hasEntry(t, l.ID, "library.maintenance", audit.OK) {
		t.Errorf("no maintenance result: %+v", e.entries(t, l.ID))
	}
	r = e.call(t, "POST", "/api/v1/libraries/"+l.ID+"/maintenance", e.admin, `{"enabled":false}`, false)
	decodeInto(t, r, &mt)
	if r.status != 200 || mt.Maintenance || mt.Terminated != 0 {
		t.Fatalf("maintenance off: %d %s", r.status, r.body)
	}

	r = e.call(t, "PATCH", "/api/v1/libraries/"+l.ID, e.admin, `{"connection_limit":5}`, false)
	var patched LibraryDTO
	decodeInto(t, r, &patched)
	var limit int
	admin, err := c.Connect(ctx, "postgres", c.SuperPass, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	admin.QueryRow(ctx, `SELECT rolconnlimit FROM pg_roles WHERE rolname = 'wedding'`).Scan(&limit)
	if r.status != 200 || patched.ConnLimit != 5 || limit != 5 {
		t.Fatalf("connection limit: %d %s, role limit %d", r.status, r.body, limit)
	}

	r = e.call(t, "POST", "/api/v1/libraries/"+l.ID+"/backups", e.token(t, "backup"), "", false)
	var bj jobs.Job
	decodeInto(t, r, &bj)
	if r.status != 202 {
		t.Fatalf("back up now: %d %s", r.status, r.body)
	}
	e.waitJob(t, bj.ID)
	r = e.call(t, "GET", "/api/v1/libraries/"+l.ID+"/backups?status=ok", e.read, "", false)
	var list struct {
		Items []BackupDTO `json:"items"`
	}
	decodeInto(t, r, &list)
	var b BackupDTO
	for _, x := range list.Items {
		if x.JobID == bj.ID {
			b = x
		}
	}
	if b.ID == "" || b.SizeBytes == 0 || b.SHA256 == "" || b.PGDumpVersion == "" || b.FinishedAt == nil {
		t.Fatalf("backup of job %s: %s", bj.ID, r.body)
	}
	r = e.call(t, "GET", "/api/v1/backups/"+b.ID+"/download", e.token(t, "restore"), "", false)
	sum := sha256.Sum256([]byte(r.body))
	if r.status != 200 || int64(len(r.body)) != b.SizeBytes || hex.EncodeToString(sum[:]) != b.SHA256 || r.header.Get("X-Sqsolve-Sha256") != b.SHA256 {
		t.Fatalf("download: %d, %d bytes, sha256 header %q, backup %+v", r.status, len(r.body), r.header.Get("X-Sqsolve-Sha256"), b)
	}
	r = e.call(t, "POST", "/api/v1/backups/"+b.ID+"/verify", e.admin, "", false)
	var vj jobs.Job
	decodeInto(t, r, &vj)
	if r.status != 202 {
		t.Fatalf("verify: %d %s", r.status, r.body)
	}
	e.waitJob(t, vj.ID)

	restore := e.token(t, "restore")
	r = e.call(t, "POST", "/api/v1/backups/"+b.ID+"/restore", restore, `{"slug":"wedding_copy","display_name":"Wedding copy"}`, false)
	var rn createdLibrary
	decodeInto(t, r, &rn)
	if r.status != 202 || rn.Library.Slug != "wedding_copy" || rn.Job.Type != core.JobRestoreNew {
		t.Fatalf("restore as new: %d %s", r.status, r.body)
	}
	e.waitJob(t, rn.Job.ID)
	var copyLib LibraryDTO
	decodeInto(t, e.call(t, "GET", "/api/v1/libraries/"+rn.Library.ID, e.read, "", false), &copyLib)
	if copyLib.State != "active" || copyLib.DisplayName != "Wedding copy" {
		t.Fatalf("restored copy: %+v", copyLib)
	}
	r = e.call(t, "POST", "/api/v1/backups/"+b.ID+"/restore", restore, `{"mode":"replace","confirm":"wedding","terminate_sessions":true}`, false)
	var rj jobs.Job
	decodeInto(t, r, &rj)
	if r.status != 202 || rj.Type != core.JobRestoreReplace || rj.LibraryID != l.ID {
		t.Fatalf("restore by replacing: %d %s", r.status, r.body)
	}
	e.waitJob(t, rj.ID)
	for _, en := range []struct{ target, action string }{{rn.Library.ID, "restore.new"}, {l.ID, "restore.replace"}} {
		if !e.hasEntry(t, en.target, en.action, audit.OK) {
			t.Errorf("no %s result: %+v", en.action, e.entries(t, en.target))
		}
	}

	// With the audit log down, neither a creation nor a restore as new
	// queues its job (ADR 0026).
	e.auditDown(t)
	wantProblemPrefix(t, "create with the audit log down", e.call(t, "POST", "/api/v1/libraries", e.admin, `{"display_name":"Later","slug":"later"}`, false), 503, x040)
	wantProblemPrefix(t, "restore as new with the audit log down", e.call(t, "POST", "/api/v1/backups/"+b.ID+"/restore", restore,
		`{"slug":"later_copy","display_name":"Later copy"}`, false), 503, x040)
	for typ, want := range map[string]int{core.JobLibraryCreate: 1, core.JobRestoreNew: 1} {
		if js, _ := e.a.Jobs.List(ctx, jobs.Filter{Type: typ}); len(js) != want {
			t.Errorf("%d %s jobs, want %d", len(js), typ, want)
		}
	}
}

// The experimental export on a real PostgreSQL, from the library and from a
// backup: the listings, the three downloads with their names, notes and ok
// entries, 404 with SQS-E003 for what does not exist, no download without
// its entry, the workspace of a backup from preparation to closing, and 422
// with SQS-E001 for a library whose tables are not the ones the export
// knows.
func TestAPIExportWithPostgreSQL(t *testing.T) {
	e, c := pgServer(t)
	ctx := context.Background()
	e.enableExports(t)
	l, pass := e.activeLibrary(t, c, "Grading", "grading", modelSchema)
	export := e.token(t, "export")
	lib := "/api/v1/libraries/" + l.ID

	r := e.call(t, "GET", lib+"/projects", e.read, "", false)
	var projects struct {
		Items        []ProjectDTO `json:"items"`
		Schema       SchemaDTO    `json:"schema"`
		Experimental bool         `json:"experimental"`
	}
	decodeInto(t, r, &projects)
	if r.status != 200 || len(projects.Items) != 1 || projects.Items[0].ID != idProject || projects.Items[0].Name != "First Look" ||
		projects.Items[0].Path != "Clients" || projects.Items[0].Modified == nil || projects.Items[0].Modified.Unix() != 1792418582 ||
		!projects.Experimental || projects.Schema.Verified || !strings.HasPrefix(projects.Schema.Fingerprint, "sha256:") {
		t.Fatalf("projects: %d %s", r.status, r.body)
	}
	r = e.call(t, "GET", lib+"/grades", e.read, "", false)
	var grades struct {
		Albums []AlbumDTO `json:"albums"`
		Schema SchemaDTO  `json:"schema"`
	}
	decodeInto(t, r, &grades)
	if r.status != 200 || len(grades.Albums) != 2 || grades.Albums[0].ViewID != idView2 || grades.Albums[0].ProjectID != "" ||
		grades.Albums[1].ProjectName != "First Look" || len(grades.Albums[1].Stills) != 1 || grades.Albums[1].Stills[0].ID != idStill ||
		grades.Albums[1].Stills[0].Width != 1920 || grades.Albums[1].Stills[0].Created != "2026-10-19T13:37:24.830" || grades.Schema.Fingerprint != projects.Schema.Fingerprint {
		t.Fatalf("grades: %d %s", r.status, r.body)
	}

	r = e.call(t, "GET", lib+"/projects/"+strings.ToUpper(idProject)+"/export", export, "", false)
	if r.status != 200 || !strings.HasPrefix(r.body, "PK") || r.header.Get("Content-Disposition") != `attachment; filename="`+idProject+`"` ||
		r.header.Get("X-Sqsolve-Name") != "grading_First Look.drp" || r.header.Get("X-Sqsolve-Experimental") != "true" ||
		!strings.Contains(r.header.Get("X-Sqsolve-Notes"), idNoPool) || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("project export: %d %v %.200q", r.status, r.header, r.body)
	}
	r = e.call(t, "GET", lib+"/grades/"+idStill+"/export", export, "", false)
	if r.status != 200 || !strings.Contains(r.body, `<Gallery::GyStill DbId="`+idStill+`">`) || r.header.Get("X-Sqsolve-Name") != "1.1-"+idStill[:8]+".drx" {
		t.Fatalf("still export: %d %v %.200q", r.status, r.header, r.body)
	}
	r = e.call(t, "GET", lib+"/grades/albums/"+idView2+"/export", export, "", false)
	if r.status != 200 || !strings.HasPrefix(r.body, "PK") || r.header.Get("X-Sqsolve-Name") != "grading_PowerGrade.zip" || r.header.Get("X-Sqsolve-Notes") != "" {
		t.Fatalf("album export: %d %v", r.status, r.header)
	}
	kinds := map[string]bool{}
	for _, en := range e.entries(t, l.ID) {
		if en.Action == "library.export" && en.Outcome == audit.OK && en.Details["fingerprint"] == projects.Schema.Fingerprint {
			kinds[en.Details["kind"].(string)] = true
		}
	}
	if len(kinds) != 3 {
		t.Fatalf("export entries: %+v", e.entries(t, l.ID))
	}
	const missing = "00000000-0000-4000-8000-0000000000ff"
	for _, c := range []struct{ path, detail string }{
		{lib + "/projects/" + missing + "/export", "SQS-E003 does not exist in the library: project " + missing},
		{lib + "/projects/not-an-id/export", "SQS-E003 does not exist in the library: invalid project identifier"},
		{lib + "/grades/" + missing + "/export", "SQS-E003 does not exist in the library: still " + missing},
		{lib + "/grades/albums/" + missing + "/export", "SQS-E003 does not exist in the library: album " + missing},
	} {
		wantProblem(t, c.path, e.call(t, "GET", c.path, export, "", false), 404, c.detail)
	}

	// From a backup: prepare the workspace, read it, download, close it.
	r = e.call(t, "POST", lib+"/backups", e.admin, "", false)
	var bj jobs.Job
	decodeInto(t, r, &bj)
	e.waitJob(t, bj.ID)
	var bs struct {
		Items []BackupDTO `json:"items"`
	}
	decodeInto(t, e.call(t, "GET", lib+"/backups?status=ok", e.read, "", false), &bs)
	var b BackupDTO
	for _, x := range bs.Items {
		if x.JobID == bj.ID {
			b = x
		}
	}
	bk := "/api/v1/backups/" + b.ID + "/export"
	r = e.call(t, "POST", bk, export, "", false)
	var prep struct {
		Job jobs.Job `json:"job"`
	}
	decodeInto(t, r, &prep)
	if r.status != 202 {
		t.Fatalf("prepare: %d %s", r.status, r.body)
	}
	e.waitJob(t, prep.Job.ID)
	r = e.call(t, "GET", bk, e.read, "", false)
	var st struct {
		Ready     bool                 `json:"ready"`
		Workspace core.ExportWorkspace `json:"workspace"`
		Projects  []ProjectDTO         `json:"projects"`
		Albums    []AlbumDTO           `json:"albums"`
		Schema    SchemaDTO            `json:"schema"`
	}
	decodeInto(t, r, &st)
	if r.status != 200 || !st.Ready || st.Workspace.State != core.WorkspaceReady || len(st.Projects) != 1 || st.Projects[0].Path != "Clients" ||
		st.Projects[0].Modified == nil || len(st.Albums) != 2 || len(st.Albums[0].Stills) != 1 || st.Schema.Fingerprint != projects.Schema.Fingerprint {
		t.Fatalf("ready workspace: %d %s", r.status, r.body)
	}
	date := b.StartedAt.UTC().Format("20060102T1504")
	for _, c := range []struct {
		path, name string
		noted      bool
	}{
		{bk + "/projects/" + idProject, "grading_" + date + "_First Look.drp", true},
		{bk + "/grades/" + idStill2, "2.1-" + idStill2[:8] + ".drx", false},
		{bk + "/grades/albums/" + idView, "grading_" + date + "_current.zip", false},
	} {
		r := e.call(t, "GET", c.path, export, "", false)
		if r.status != 200 || r.header.Get("X-Sqsolve-Name") != c.name || r.header.Get("X-Sqsolve-Experimental") != "true" || r.body == "" ||
			strings.Contains(r.header.Get("X-Sqsolve-Notes"), idNoPool) != c.noted {
			t.Errorf("%s: %d %v", c.path, r.status, r.header)
		}
	}
	wantProblem(t, "missing still in the backup", e.call(t, "GET", bk+"/grades/"+missing, export, "", false), 404, "SQS-E003 does not exist in the library: still "+missing)
	n := 0
	for _, en := range e.entries(t, b.ID) {
		if en.Action == "backup.export" && en.Outcome == audit.OK && en.Details["library_id"] == l.ID {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("backup export entries: %+v", e.entries(t, b.ID))
	}

	// No download goes out without its entry.
	e.auditDown(t)
	for _, p := range []string{lib + "/projects/" + idProject + "/export", lib + "/grades/" + idStill + "/export", bk + "/grades/albums/" + idView2} {
		r := e.call(t, "GET", p, export, "", false)
		wantProblemPrefix(t, p+" with the audit log down", r, 503, x040)
		if r.header.Get("X-Sqsolve-Name") != "" || strings.Contains(r.body, "PK") {
			t.Errorf("%s went out without its entry", p)
		}
	}
	e.exec(t, `DROP TRIGGER audit_down`)

	if r := e.call(t, "DELETE", bk, export, "", false); r.status != 204 {
		t.Fatalf("close: %d %s", r.status, r.body)
	}
	var closed struct {
		Ready     bool                 `json:"ready"`
		Workspace core.ExportWorkspace `json:"workspace"`
	}
	decodeInto(t, e.call(t, "GET", bk, e.read, "", false), &closed)
	if closed.Ready || closed.Workspace.State != core.WorkspaceClosed || !e.hasEntry(t, b.ID, "backup.export_close", audit.OK) {
		t.Fatalf("after closing: %+v", closed)
	}
	wantProblem(t, "download after closing", e.call(t, "GET", bk+"/projects/"+idProject, export, "", false), 409, "SQS-E007 the backup is not prepared for export; prepare it first")

	// A column the export reads goes away: the library no longer matches.
	owner, err := c.ConnectTCP(ctx, "grading", secrets.New(pass), "grading")
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Exec(ctx, `ALTER TABLE "SM_Project" DROP COLUMN "ProjectName"`)
	owner.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantProblem(t, "changed schema", e.call(t, "GET", lib+"/projects", e.read, "", false), 422,
		"SQS-E001 the library does not have the tables and columns the experimental export knows: SM_Project.ProjectName")
}
