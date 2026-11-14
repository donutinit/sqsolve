// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/drp"
	"github.com/donutinit/sqsolve/internal/store"
)

// What a reader without the diagnostics permission gets of a failed
// connection to PostgreSQL (pg.Public): connectionHidden alone, or after the
// message of SQS-E005 (exportHidden).
const (
	connectionHidden = "the connection to PostgreSQL failed (the cause is in the sqsolve log)"
	exportHidden     = "SQS-E005 the library is not available for export: " + connectionHidden
)

// The experimental export (ADR 0019) is off until an administrator enables
// it, reads only live libraries and usable backups, reports a server it
// cannot reach as unavailable, and a download is never served without its
// audit entry. Each refusal maps to the HTTP status the panel answers with.
func TestExportGuards(t *testing.T) {
	ctx := context.Background()
	s, l, b := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	_, err := s.OpenExport(ctx, l)
	refused(t, "export with the feature off", err, "SQS-E002 experimental export is disabled; an administrator can enable it in Settings")
	if ExportStatus(err) != 409 {
		t.Errorf("status of a disabled export: %d", ExportStatus(err))
	}
	_, _, err = s.PrepareBackupExport(ctx, by, b.ID)
	if !errors.Is(err, ErrExportsDisabled) {
		t.Errorf("prepare with the feature off: %v", err)
	}
	st := s.Settings(ctx)
	st.ExperimentalExports = true
	if err := s.SaveSettings(ctx, by, st); err != nil {
		t.Fatal(err)
	}

	trashed := *l
	trashed.State = StateTrashed
	_, err = s.OpenExport(ctx, &trashed)
	refused(t, "export of a trashed library", err, "SQS-E005 the library is not available for export: it is in state In the trash")
	if ExportStatus(err) != 503 {
		t.Errorf("status of an unavailable library: %d", ExportStatus(err))
	}
	// The catalog of this test has no PostgreSQL behind it.
	_, err = s.OpenExport(ctx, l)
	if !errors.Is(err, ErrExportUnavailable) || !strings.HasPrefix(err.Error(), "SQS-E005 the library is not available for export: ") {
		t.Errorf("export with the server down: %v", err)
	}

	s.DB.ExecContext(ctx, `UPDATE backups SET status = 'failed' WHERE id = ?`, b.ID)
	_, _, err = s.PrepareBackupExport(ctx, by, b.ID)
	refused(t, "prepare from a failed backup", err, "SQS-E005 the backup is not available (it failed or was deleted)")
	_, _, err = s.PrepareBackupExport(ctx, by, "NOPE")
	refused(t, "prepare from an unknown backup", err, "SQS-B001 backup not found")
	if _, _, err := s.OpenBackupExport(ctx, b.ID); !errors.Is(err, ErrExportNotReady) || ExportStatus(err) != 409 {
		t.Errorf("session without a workspace: %v", err)
	}
	if err := s.CloseBackupExport(ctx, by, b.ID); err != nil {
		t.Errorf("closing a backup without a workspace: %v", err)
	}

	breakAudit(t, s)
	sess, info := &drp.Session{Fingerprint: "sha256:x"}, &drp.ExportInfo{Name: "Wedding", Files: 1, Bytes: 10}
	for what, err := range map[string]error{
		"download from the library": s.AuditExport(ctx, by, l, sess, "project", "1", info),
		"download from a backup":    s.AuditBackupExport(ctx, by, b, sess, "project", "1", info),
	} {
		if !errors.Is(err, audit.ErrUnavailable) || ExportStatus(err) != 503 {
			t.Errorf("%s without its audit entry: %v (status %d)", what, err, ExportStatus(err))
		}
	}
	if ExportStatus(drp.ErrNotFound) != 404 || ExportStatus(drp.ErrSchema) != 422 {
		t.Errorf("statuses: not found %d, unknown schema %d", ExportStatus(drp.ErrNotFound), ExportStatus(drp.ErrSchema))
	}
}

// TestExportErrorsHideTheConnection: when the connection to PostgreSQL
// fails, a reader without the diagnostics permission gets the message up to
// the driver's text (no socket path, role or database), a reader with it gets
// the whole error, and the whole error goes to the log once per request.
// Other errors are left alone and not logged.
func TestExportErrorsHideTheConnection(t *testing.T) {
	ctx := context.Background()
	s, l, b := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	st := s.Settings(ctx)
	st.ExperimentalExports = true
	if err := s.SaveSettings(ctx, by, st); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	s.Log = slog.New(slog.NewJSONHandler(&buf, nil))
	type logLine struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
		Path  string `json:"path"`
		Err   string `json:"err"`
	}
	logged := func() []logLine {
		t.Helper()
		var out []logLine
		for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if raw == "" {
				continue
			}
			var ln logLine
			if err := json.Unmarshal([]byte(raw), &ln); err != nil {
				t.Fatalf("log line %q: %v", raw, err)
			}
			out = append(out, ln)
		}
		return out
	}
	// unreachable checks the error of a session whose database cannot be
	// reached: SQS-E005 with the driver's error still in its chain.
	unreachable := func(what string, err error, db string) {
		t.Helper()
		prefix := "SQS-E005 the library is not available for export: failed to connect to `user=sqsolve_admin database=" + db + "`: "
		if !errors.Is(err, ErrExportUnavailable) || ExportStatus(err) != 503 || !errors.As(err, new(*pgconn.ConnectError)) || !strings.HasPrefix(err.Error(), prefix) {
			t.Fatalf("%s: %v (status %d), want %q...", what, err, ExportStatus(err), prefix)
		}
	}
	// public checks what a reader without the permission gets.
	public := func(what string, err error, internals ...string) {
		t.Helper()
		refused(t, what, err, exportHidden)
		for _, internal := range append(internals, s.PG.SocketDir, ".s.PGSQL", "sqsolve_admin", "database=") {
			if err != nil && strings.Contains(err.Error(), internal) {
				t.Errorf("%s names %q: %v", what, internal, err)
			}
		}
	}

	// A library: the catalog of this test has no PostgreSQL behind it.
	_, err := s.OpenExport(ctx, l)
	unreachable("export of a library", err, "lib")
	public("a library for a reader without the permission", s.ExportError(err, "/libraries/L1/export", false))
	if lines := logged(); len(lines) != 1 || lines[0].Level != "WARN" || lines[0].Msg != "export: the connection to PostgreSQL failed" ||
		lines[0].Path != "/libraries/L1/export" || lines[0].Err != err.Error() {
		t.Fatalf("log after one request: %+v", lines)
	}
	if got := s.ExportError(err, "/libraries/L1/export", true); got != err {
		t.Errorf("a library for a reader with the permission: %v", got)
	}
	if lines := logged(); len(lines) != 2 || lines[1].Err != err.Error() {
		t.Fatalf("log after a second request: %+v", lines)
	}

	// A ready workspace of a backup.
	now := time.Now()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO export_workspaces(`+workspaceCols+`) VALUES ('W1', ?, 'sqsolve_export_w1', '', 'ready', '', ?, ?)`,
		b.ID, store.T(now), store.T(now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.OpenBackupExport(ctx, b.ID)
	unreachable("export of a backup", err, "sqsolve_export_w1")
	public("a backup for a reader without the permission", s.ExportError(err, "/backups/B1/export", false), "sqsolve_export")
	if lines := logged(); len(lines) != 3 || lines[2].Path != "/backups/B1/export" || lines[2].Err != err.Error() {
		t.Fatalf("log after a backup request: %+v", lines)
	}

	// The driver's error with nothing of sqsolve before it.
	_, bare := s.PG.ConnectDB(ctx, "lib")
	refused(t, "the driver's error alone", s.ExportError(bare, "/x", false), connectionHidden)

	// Not a connection error.
	before := len(logged())
	if got := s.ExportError(ErrExportsDisabled, "/x", false); got != ErrExportsDisabled {
		t.Errorf("another error was changed: %v", got)
	}
	if got := s.ExportError(nil, "/x", false); got != nil {
		t.Errorf("no error became %v", got)
	}
	if lines := logged(); len(lines) != before {
		t.Errorf("errors that are not a failed connection were logged: %+v", lines[before:])
	}
}
