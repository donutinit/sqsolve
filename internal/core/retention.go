// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/donutinit/sqsolve/internal/events"
	"github.com/donutinit/sqsolve/internal/store"
)

// RetentionItem is the input of the retention function.
type RetentionItem struct {
	ID string
	// Seq is the insertion order in the catalog (higher is newer). It does
	// not depend on the clock; zero means unknown.
	Seq      int64
	Started  time.Time
	Kind     string
	Pinned   bool
	Verified bool
	// Aux marks a backup of an auxiliary database of the library (a
	// previous state backed up before it is dropped): an older state, so it
	// only counts towards min_verified when the live database has too few.
	Aux bool
}

// RetentionPolicy is the input policy.
type RetentionPolicy struct {
	KeepLast, KeepDaily, KeepWeekly, KeepMonthly, MinVerified int
	Location                                                  *time.Location
	ProtectedFor                                              time.Duration
}

// Retain decides which backups stay. It is a pure function: the result does
// not depend on the order of items. keep maps id to the first reason found.
//
// "Most recent" (keep_last and the minimum of verified backups) follows the
// insertion order (Seq), which a clock that jumps cannot reorder: the backup
// just taken is always among the newest. The calendar rules (days, weeks,
// months) can only use the timestamps.
func Retain(items []RetentionItem, p RetentionPolicy, now time.Time) (keep map[string]string, expire []string) {
	loc := p.Location
	if loc == nil {
		loc = time.UTC
	}
	if p.ProtectedFor == 0 {
		p.ProtectedFor = 30 * 24 * time.Hour
	}
	newerTime := func(a, b RetentionItem) bool {
		if !a.Started.Equal(b.Started) {
			return a.Started.After(b.Started)
		}
		return a.ID > b.ID
	}
	sorted := append([]RetentionItem(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return newerTime(sorted[i], sorted[j]) })
	inserted := append([]RetentionItem(nil), items...)
	sort.Slice(inserted, func(i, j int) bool {
		if inserted[i].Seq != inserted[j].Seq {
			return inserted[i].Seq > inserted[j].Seq
		}
		return newerTime(inserted[i], inserted[j])
	})
	keep = map[string]string{}
	mark := func(id, reason string) {
		if _, ok := keep[id]; !ok {
			keep[id] = reason
		}
	}
	for _, it := range sorted {
		if it.Pinned {
			mark(it.ID, "pinned")
		}
		switch it.Kind {
		case KindPreRestore, KindPreDelete, KindPreUpgrade:
			if now.Sub(it.Started) < p.ProtectedFor {
				mark(it.ID, "protected ("+KindLabel(it.Kind)+")")
			}
		}
	}
	for i, it := range inserted {
		if i < p.KeepLast {
			mark(it.ID, fmt.Sprintf("among the %d most recent", p.KeepLast))
		}
	}
	bucket := func(n int, key func(time.Time) string, label string) {
		if n <= 0 {
			return
		}
		seen := map[string]bool{}
		for _, it := range sorted {
			k := key(it.Started.In(loc))
			if seen[k] {
				continue
			}
			if len(seen) >= n {
				return
			}
			seen[k] = true
			mark(it.ID, label)
		}
	}
	bucket(p.KeepDaily, func(t time.Time) string { return t.Format("2006-01-02") }, "last of the day")
	bucket(p.KeepWeekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-%02d", y, w) }, "last of the week")
	bucket(p.KeepMonthly, func(t time.Time) string { return t.Format("2006-01") }, "last of the month")
	// The minimum of verified backups is met with backups of the live
	// database first, then, if there are too few, with auxiliary ones.
	fill := func(withAux bool) {
		verifiedKept := 0
		for _, it := range sorted {
			if _, ok := keep[it.ID]; ok && it.Verified && (withAux || !it.Aux) {
				verifiedKept++
			}
		}
		for _, it := range inserted {
			if verifiedKept >= p.MinVerified {
				break
			}
			if _, ok := keep[it.ID]; !ok && it.Verified && (withAux || !it.Aux) {
				mark(it.ID, "minimum of verified backups")
				verifiedKept++
			}
		}
	}
	fill(false)
	fill(true)
	for _, it := range sorted {
		if _, ok := keep[it.ID]; !ok {
			expire = append(expire, it.ID)
		}
	}
	return keep, expire
}

// RetentionPreview computes the effect of a policy on a library.
func (s *Service) RetentionPreview(ctx context.Context, libraryID string, p Policy) (map[string]string, []string, error) {
	items, err := s.retentionItems(ctx, libraryID)
	if err != nil {
		return nil, nil, err
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		loc = s.Cfg.Location
	}
	keep, expire := Retain(items, RetentionPolicy{KeepLast: p.KeepLast, KeepDaily: p.KeepDaily, KeepWeekly: p.KeepWeekly,
		KeepMonthly: p.KeepMonthly, MinVerified: p.MinVerified, Location: loc}, s.now())
	return keep, expire, nil
}

// retentionItems reads the ok backups of a library. The SQLite rowid of the
// catalog row is its insertion order.
func (s *Service) retentionItems(ctx context.Context, libraryID string) ([]RetentionItem, error) {
	var dbName string
	s.DB.QueryRowContext(ctx, `SELECT db_name FROM libraries WHERE id = ?`, libraryID).Scan(&dbName)
	rows, err := s.DB.QueryContext(ctx, `SELECT id, rowid, started_at, kind, pinned, verify_status, source_db FROM backups
		WHERE library_id = ? AND status = 'ok'`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []RetentionItem
	for rows.Next() {
		var it RetentionItem
		var started, verify, source string
		if err := rows.Scan(&it.ID, &it.Seq, &started, &it.Kind, &it.Pinned, &verify, &source); err != nil {
			return nil, err
		}
		it.Started = store.ParseT(started)
		it.Verified = (&Backup{VerifyStatus: verify}).Verified()
		it.Aux = auxSource(dbName, source)
		items = append(items, it)
	}
	return items, rows.Err()
}

// backupsInUse returns the backups that a queued or running restore,
// verification or export is going to read, so retention does not delete
// them underneath the job. Retention takes no library lock; this is its
// reader protection.
func (s *Service) backupsInUse(ctx context.Context) (map[string]bool, error) {
	inUse := map[string]bool{}
	rows, err := s.DB.QueryContext(ctx, `SELECT type, library_id, params FROM jobs WHERE state IN ('queued', 'running') AND type IN (?, ?, ?, ?)`,
		JobRestoreNew, JobRestoreReplace, JobRestoreRecreate, JobBackupVerify)
	if err != nil {
		return nil, err
	}
	type ref struct{ typ, lib, params string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.typ, &r.lib, &r.params); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range refs {
		var p struct {
			BackupID string `json:"backup_id"`
			Target   string `json:"target"`
		}
		json.Unmarshal([]byte(r.params), &p)
		if p.BackupID != "" {
			inUse[p.BackupID] = true
			continue
		}
		if r.typ == JobBackupVerify {
			// A verification without a backup picks the newest verified one
			// of its library when it starts.
			lib := r.lib
			if lib == "" {
				lib = p.Target
			}
			b, err := s.LatestVerified(ctx, lib)
			if err != nil {
				return nil, err
			}
			if b != nil {
				inUse[b.ID] = true
			}
		}
	}
	// Exports from a backup: the workspace names its backup.
	wrows, err := s.DB.QueryContext(ctx, `SELECT backup_id FROM export_workspaces WHERE state IN ('preparing', 'ready')`)
	if err != nil {
		return nil, err
	}
	defer wrows.Close()
	for wrows.Next() {
		var id string
		if err := wrows.Scan(&id); err != nil {
			return nil, err
		}
		inUse[id] = true
	}
	return inUse, wrows.Err()
}

// applyRetention deletes expired backups of a library and cleans failed
// leftovers. It returns how many backups were removed. Backups that a job is
// about to read are kept until a later run.
func (s *Service) applyRetention(ctx context.Context, libraryID string) (int, error) {
	p, err := s.Policy(ctx, libraryID)
	if err != nil {
		return 0, err
	}
	_, expire, err := s.RetentionPreview(ctx, libraryID, p)
	if err != nil {
		return 0, err
	}
	var inUse map[string]bool
	if len(expire) > 0 {
		if inUse, err = s.backupsInUse(ctx); err != nil {
			return 0, err
		}
	}
	n := 0
	for _, id := range expire {
		if inUse[id] {
			s.Log.Info("retention: backup in use by a job; kept until the next run", "backup", id)
			continue
		}
		b, err := s.Backup(ctx, id)
		if err != nil {
			continue
		}
		if err := s.removeBackupFiles(ctx, b, "retention"); err != nil {
			s.Log.Error("retention", "backup", id, "err", err)
			continue
		}
		n++
	}
	// Remove files of failed or interrupted backups and old failed rows.
	failed, _ := s.Backups(ctx, BackupFilter{LibraryID: libraryID, Statuses: []string{BackupFailed, BackupInterrupted}})
	for _, b := range failed {
		abs := s.AbsPath(b)
		for _, p := range []string{abs, abs + ".partial", manifestPath(abs)} {
			_ = removeIfExists(p)
		}
		if s.now().Sub(b.StartedAt) > 30*24*time.Hour {
			s.DB.ExecContext(ctx, `DELETE FROM backups WHERE id = ?`, b.ID)
		}
	}
	if n > 0 {
		l, _ := s.Library(ctx, libraryID)
		name := libraryID
		if l != nil {
			name = l.DisplayName
		}
		s.Events.Emit(ctx, events.Event{Type: events.BackupExpired, LibraryID: libraryID,
			Message: fmt.Sprintf("Retention deleted %d backups of %q", n, name), Data: map[string]any{"count": n}})
	}
	return n, nil
}
