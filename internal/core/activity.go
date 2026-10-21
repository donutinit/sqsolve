// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/donutinit/sqsolve/internal/store"
)

// Backups according to activity (ADR 0022). A library's activity mark is the
// count of rows written since its statistics were reset (pg_stat_database);
// a backup records the mark of its snapshot, so comparing marks says whether
// anything changed since, without reading a single Resolve table.

// activityCache keeps the marks of every database for a few seconds, so the
// dashboard asks PostgreSQL once per page and not once per library.
type activityCache struct {
	mu    sync.Mutex
	at    time.Time
	marks map[string]string
}

const activityCacheTTL = 20 * time.Second

// ActivityMarks returns the current mark of every database, cached briefly.
func (s *Service) ActivityMarks(ctx context.Context) map[string]string {
	c := &s.activity
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.marks != nil && s.now().Sub(c.at) < activityCacheTTL {
		return c.marks
	}
	marks, err := s.PG.ActivityMarks(ctx)
	if err != nil {
		return c.marks
	}
	c.marks, c.at = marks, s.now()
	return marks
}

// UnchangedSince returns the latest verified backup of l when the library has
// not changed since it was taken, or nil when it changed, when there is no
// verified backup, or when either mark is unknown (older backups have none).
func (s *Service) UnchangedSince(ctx context.Context, l *Library) *Backup {
	b, err := s.LatestVerified(ctx, l.ID)
	if err != nil || b == nil || b.ActivityMark == "" || b.SourceDB != l.DBName {
		return nil
	}
	cur := s.ActivityMarks(ctx)[l.DBName]
	if cur == "" || cur != b.ActivityMark {
		return nil
	}
	return b
}

// BackupGate decides whether a due scheduled backup runs. In activity mode it
// is skipped while the rows have not changed since the newest usable
// verified backup: a backup that failed its verification, or was never
// verified, does not count, so an unchanged library still gets a good one.
// Every check is recorded on the policy so the panel can show it.
func (s *Service) BackupGate(ctx context.Context, libraryID string) (bool, string) {
	pol, err := s.Policy(ctx, libraryID)
	if err != nil || !pol.Adaptive() {
		return true, ""
	}
	l, err := s.Library(ctx, libraryID)
	if err != nil {
		return true, ""
	}
	var lastMark, lastAt string
	err = s.DB.QueryRowContext(ctx, `SELECT activity_mark, started_at FROM backups WHERE library_id = ? AND status = 'ok' AND source_db = ?
		AND verify_status IN ('l1_ok', 'l2_ok', 'partial') ORDER BY started_at DESC LIMIT 1`, l.ID, l.DBName).Scan(&lastMark, &lastAt)
	if err != nil || lastMark == "" {
		s.recordActivityCheck(ctx, l.ID, false)
		return true, "no earlier verified backup with an activity mark"
	}
	cur, err := s.PG.ActivityMark(ctx, l.DBName)
	if err != nil || cur == "" {
		return true, "could not read the activity"
	}
	same := cur == lastMark
	s.recordActivityCheck(ctx, l.ID, same)
	if same {
		return false, fmt.Sprintf("no changes since the backup from %s", store.ParseT(lastAt).In(s.Cfg.Location).Format("2006-01-02 15:04"))
	}
	return true, ""
}

func (s *Service) recordActivityCheck(ctx context.Context, libraryID string, same bool) {
	s.DB.ExecContext(ctx, `UPDATE backup_policies SET last_check_at = ?, last_check_same = ? WHERE library_id = ?`, store.T(s.now()), same, libraryID)
}
