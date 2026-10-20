// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/drp"
)

// Errors of the experimental export (ADR 0019, docs 22 §8).
var (
	ErrExportsDisabled   = errors.New("SQS-E002 experimental export is disabled; an administrator can enable it in Settings")
	ErrExportUnavailable = errors.New("SQS-E005 the library is not available for export")
)

// OpenExport opens a read-only export session on a library. The caller must
// Close it. It fails when the feature is off, the library is not live or its
// schema is not the one the exporter knows.
func (s *Service) OpenExport(ctx context.Context, l *Library) (*drp.Session, error) {
	if !s.Settings(ctx).ExperimentalExports {
		return nil, ErrExportsDisabled
	}
	if l.State != StateActive && l.State != StateMaintenance {
		return nil, fmt.Errorf("%w: it is in state %s", ErrExportUnavailable, l.State.Label())
	}
	conn, err := s.PG.ConnectDB(ctx, l.DBName)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExportUnavailable, err)
	}
	// drp.Open owns conn from here on, also when it fails.
	return drp.Open(ctx, conn)
}

// AuditExport records a download in the audit log.
func (s *Service) AuditExport(ctx context.Context, by audit.Actor, l *Library, sess *drp.Session, kind, id string, info *drp.ExportInfo) {
	s.Audit.Record(ctx, by, "library.export", "library", l.ID, audit.OK, map[string]any{
		"kind": kind, "id": id, "name": info.Name, "files": info.Files, "bytes": info.Bytes,
		"fingerprint": sess.Fingerprint, "verified": sess.KnownOK, "notes": info.Notes,
	})
}

// ExportStatus maps an export error to an HTTP status.
func ExportStatus(err error) int {
	switch {
	case errors.Is(err, ErrExportsDisabled), errors.Is(err, ErrExportNotReady):
		return 409
	case errors.Is(err, drp.ErrNotFound):
		return 404
	case errors.Is(err, ErrExportUnavailable):
		return 503
	}
	return 422
}
