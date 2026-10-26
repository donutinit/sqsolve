// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/drp"
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
