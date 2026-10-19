// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/donutinit/sqsolve/internal/events"
)

// RetentionItem is the input of the retention function.
type RetentionItem struct {
	ID       string
	Started  time.Time
	Kind     string
	Pinned   bool
	Verified bool
}

// RetentionPolicy is the input policy.
type RetentionPolicy struct {
	KeepLast, KeepDaily, KeepWeekly, KeepMonthly, MinVerified int
	Location                                                  *time.Location
	ProtectedFor                                              time.Duration
}

// Retain decides which backups stay. It is a pure function: the result does
// not depend on the order of items. keep maps id to the first reason found.
func Retain(items []RetentionItem, p RetentionPolicy, now time.Time) (keep map[string]string, expire []string) {
	loc := p.Location
	if loc == nil {
		loc = time.UTC
	}
	if p.ProtectedFor == 0 {
		p.ProtectedFor = 30 * 24 * time.Hour
	}
	sorted := append([]RetentionItem(nil), items...)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].Started.Equal(sorted[j].Started) {
			return sorted[i].Started.After(sorted[j].Started)
		}
		return sorted[i].ID > sorted[j].ID
	})
	keep = map[string]string{}
	mark := func(id, reason string) {
		if _, ok := keep[id]; !ok {
			keep[id] = reason
		}
	}
	for _, it := range sorted {
		if it.Pinned {
			mark(it.ID, "fijado")
		}
		switch it.Kind {
		case KindPreRestore, KindPreDelete, KindPreUpgrade:
			if now.Sub(it.Started) < p.ProtectedFor {
				mark(it.ID, "protegido ("+KindLabel(it.Kind)+")")
			}
		}
	}
	for i, it := range sorted {
		if i < p.KeepLast {
			mark(it.ID, fmt.Sprintf("entre los %d más recientes", p.KeepLast))
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
	bucket(p.KeepDaily, func(t time.Time) string { return t.Format("2006-01-02") }, "último del día")
	bucket(p.KeepWeekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-%02d", y, w) }, "último de la semana")
	bucket(p.KeepMonthly, func(t time.Time) string { return t.Format("2006-01") }, "último del mes")
	verifiedKept := 0
	for _, it := range sorted {
		if _, ok := keep[it.ID]; ok && it.Verified {
			verifiedKept++
		}
	}
	for _, it := range sorted {
		if verifiedKept >= p.MinVerified {
			break
		}
		if _, ok := keep[it.ID]; !ok && it.Verified {
			mark(it.ID, "mínimo de respaldos verificados")
			verifiedKept++
		}
	}
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

func (s *Service) retentionItems(ctx context.Context, libraryID string) ([]RetentionItem, error) {
	backups, err := s.Backups(ctx, BackupFilter{LibraryID: libraryID, Statuses: []string{BackupOK}})
	if err != nil {
		return nil, err
	}
	items := make([]RetentionItem, 0, len(backups))
	for _, b := range backups {
		items = append(items, RetentionItem{ID: b.ID, Started: b.StartedAt, Kind: b.Kind, Pinned: b.Pinned, Verified: b.Verified()})
	}
	return items, nil
}

// applyRetention deletes expired backups of a library and cleans failed
// leftovers. It returns how many backups were removed.
func (s *Service) applyRetention(ctx context.Context, libraryID string) (int, error) {
	p, err := s.Policy(ctx, libraryID)
	if err != nil {
		return 0, err
	}
	_, expire, err := s.RetentionPreview(ctx, libraryID, p)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range expire {
		b, err := s.Backup(ctx, id)
		if err != nil {
			continue
		}
		if err := s.removeBackupFiles(ctx, b, "retención"); err != nil {
			s.Log.Error("retención", "backup", id, "err", err)
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
			Message: fmt.Sprintf("La retención borró %d respaldos de %q", n, name), Data: map[string]any{"count": n}})
	}
	return n, nil
}
