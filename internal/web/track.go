// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/donutinit/sqsolve/internal/core"
)

// Track is the protection timeline of a library: the last days drawn like a
// timeline track, with each backup as a marker and the periods without a
// recent verified backup shaded. It is rendered as inline SVG (attributes,
// no inline styles, so it works under the strict CSP).
type Track struct {
	Label     string
	LibraryID string
	Days      int
	Marks     []TrackMark
	Gaps      []TrackSpan
	Ticks     []float64
	Summary   string // accessible description
	LastAgo   string
	Tone      string // ok, warn, bad, off
	GapHours  int
}

// TrackMark is one backup on the track.
type TrackMark struct {
	X     float64
	Kind  string // l2, l1, unverified, failed
	Title string
	ID    string
}

// TrackSpan is a shaded period.
type TrackSpan struct {
	X, W float64
}

const trackWidth = 1000.0

func (s *Server) track(ctx context.Context, l *core.Library, days int) Track {
	now := time.Now()
	start := now.Add(-time.Duration(days) * 24 * time.Hour)
	t := Track{Label: l.Slug, LibraryID: l.ID, Days: days, Tone: "off"}
	x := func(at time.Time) float64 {
		v := float64(at.Sub(start)) / float64(now.Sub(start)) * trackWidth
		if v < 0 {
			return 0
		}
		// Keep the newest markers clear of the right edge and the "now" line.
		if v > trackWidth-6 {
			return trackWidth - 6
		}
		return v
	}
	loc := s.A.Cfg.Location
	day := time.Date(start.In(loc).Year(), start.In(loc).Month(), start.In(loc).Day(), 0, 0, 0, 0, loc).Add(24 * time.Hour)
	for ; day.Before(now); day = day.Add(24 * time.Hour) {
		t.Ticks = append(t.Ticks, x(day))
	}
	bs, _ := s.A.Svc.Backups(ctx, core.BackupFilter{LibraryID: l.ID, Statuses: []string{core.BackupOK, core.BackupFailed, core.BackupDeleted}, Limit: 600})
	var verified []time.Time
	var before *time.Time
	okCount, failCount := 0, 0
	for _, b := range bs {
		usable := b.Status == core.BackupOK || (b.Status == core.BackupDeleted && b.Verified())
		if b.StartedAt.Before(start) {
			if usable && b.Verified() && before == nil {
				st := b.StartedAt
				before = &st
			}
			continue
		}
		m := TrackMark{X: x(b.StartedAt), ID: b.ID}
		when := b.StartedAt.In(loc).Format("2006-01-02 15:04")
		switch {
		case b.Status == core.BackupFailed:
			m.Kind = "failed"
			m.Title = when + ": backup failed"
			failCount++
		case b.VerifyStatus == core.VerifyL2OK:
			m.Kind = "l2"
			m.Title = when + ": test restore OK"
		case b.VerifyStatus == core.VerifyL1OK || b.VerifyStatus == core.VerifyPartial:
			m.Kind = "l1"
			m.Title = when + ": checksum and index OK"
		default:
			m.Kind = "unverified"
			m.Title = when + ": unverified"
		}
		if b.Status == core.BackupDeleted {
			m.Title += " (already deleted by retention)"
		}
		if m.Kind == "l1" || m.Kind == "l2" {
			verified = append(verified, b.StartedAt)
			okCount++
		}
		t.Marks = append(t.Marks, m)
	}
	sort.Slice(verified, func(i, j int) bool { return verified[i].Before(verified[j]) })

	stale := time.Duration(s.A.Svc.Settings(ctx).StaleHours) * time.Hour
	origin := start
	if l.CreatedAt.After(origin) {
		origin = l.CreatedAt
	}
	cursor := origin
	if before != nil && before.After(cursor.Add(-stale)) {
		cursor = *before
	}
	var gapTotal time.Duration
	addGap := func(from, to time.Time) {
		if from.Before(origin) {
			from = origin
		}
		if !to.After(from) {
			return
		}
		gapTotal += to.Sub(from)
		t.Gaps = append(t.Gaps, TrackSpan{X: x(from), W: x(to) - x(from)})
	}
	live := l.State == core.StateActive || l.State == core.StateMaintenance
	for _, v := range verified {
		if v.Sub(cursor) > stale {
			addGap(cursor.Add(stale), v)
		}
		cursor = v
	}
	// An old backup that still matches the data is current, not late (ADR 0022).
	unchanged := live && now.Sub(cursor) > stale && s.A.Svc.UnchangedSince(ctx, l) != nil
	if live && now.Sub(cursor) > stale && !unchanged {
		addGap(cursor.Add(stale), now)
	}
	t.GapHours = int(gapTotal.Hours())

	var last time.Time
	if len(verified) > 0 {
		last = verified[len(verified)-1]
	} else if before != nil {
		last = *before
	}
	switch {
	case !live:
		t.Tone = "off"
	case last.IsZero():
		t.Tone = "bad"
	case now.Sub(last) > stale && !unchanged:
		t.Tone = "bad"
	case failCount > 0 || len(t.Gaps) > 0:
		t.Tone = "warn"
	default:
		t.Tone = "ok"
	}
	switch {
	case last.IsZero():
		t.LastAgo = "no verified backup"
	case unchanged:
		t.LastAgo = humanAgo(now.Sub(last)) + ", unchanged"
	default:
		t.LastAgo = humanAgo(now.Sub(last))
	}
	t.Summary = fmt.Sprintf("%s: %d verified and %d failed backups in %d days; last verified %s", l.Slug, okCount, failCount, days, t.LastAgo)
	if t.GapHours > 0 {
		t.Summary += fmt.Sprintf("; %d hours without a recent verified backup", t.GapHours)
	}
	return t
}
