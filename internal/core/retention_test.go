// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"
)

func items(n int, every time.Duration, end time.Time) []RetentionItem {
	out := make([]RetentionItem, n)
	for i := 0; i < n; i++ {
		out[i] = RetentionItem{ID: fmt.Sprintf("B%04d", i), Started: end.Add(-time.Duration(i) * every), Kind: KindScheduled, Verified: true}
	}
	return out
}

func TestRetainBasic(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	// 60 days of hourly backups.
	in := items(60*24, time.Hour, now)
	keep, expire := Retain(in, RetentionPolicy{KeepLast: 10, KeepDaily: 14, KeepWeekly: 8, KeepMonthly: 12, MinVerified: 1}, now)
	if len(keep)+len(expire) != len(in) {
		t.Fatal("lost items")
	}
	// 10 last + 13 more days (day 0 already covered) + weeks/months beyond 14 days.
	if len(keep) < 23 || len(keep) > 40 {
		t.Fatalf("unexpected keep count %d", len(keep))
	}
	if _, ok := keep["B0000"]; !ok {
		t.Fatal("newest not kept")
	}
}

func TestRetainNeverDeletesPinnedOrLastVerified(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 500; iter++ {
		n := rng.Intn(80) + 1
		in := make([]RetentionItem, n)
		anyVerified := false
		for i := range in {
			in[i] = RetentionItem{
				ID:       fmt.Sprintf("X%03d", i),
				Seq:      rng.Int63n(1 << 20),
				Started:  now.Add(-time.Duration(rng.Intn(400*24)) * time.Hour),
				Kind:     []string{KindScheduled, KindManual, KindPreRestore, KindPreDelete}[rng.Intn(4)],
				Pinned:   rng.Intn(15) == 0,
				Verified: rng.Intn(3) > 0,
				Aux:      rng.Intn(6) == 0,
			}
			anyVerified = anyVerified || in[i].Verified
		}
		p := RetentionPolicy{KeepLast: rng.Intn(3), KeepDaily: rng.Intn(5), KeepWeekly: rng.Intn(3), KeepMonthly: rng.Intn(3), MinVerified: 1 + rng.Intn(2)}
		keep, expire := Retain(in, p, now)
		for _, it := range in {
			if it.Pinned {
				if _, ok := keep[it.ID]; !ok {
					t.Fatalf("pinned %s expired", it.ID)
				}
			}
		}
		if anyVerified {
			kept := 0
			for _, it := range in {
				if _, ok := keep[it.ID]; ok && it.Verified {
					kept++
				}
			}
			if kept == 0 {
				t.Fatalf("no verified backup kept: policy %+v", p)
			}
		}
		// Order independence.
		shuffled := append([]RetentionItem(nil), in...)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		_, expire2 := Retain(shuffled, p, now)
		sort.Strings(expire)
		sort.Strings(expire2)
		if fmt.Sprint(expire) != fmt.Sprint(expire2) {
			t.Fatal("result depends on input order")
		}
	}
}

func TestRetainProtectedKinds(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	in := []RetentionItem{
		{ID: "new", Started: now, Kind: KindScheduled, Verified: true},
		{ID: "pre", Started: now.Add(-10 * 24 * time.Hour), Kind: KindPreRestore, Verified: true},
		{ID: "oldpre", Started: now.Add(-40 * 24 * time.Hour), Kind: KindPreDelete, Verified: true},
	}
	keep, expire := Retain(in, RetentionPolicy{KeepLast: 1, MinVerified: 1}, now)
	if _, ok := keep["pre"]; !ok {
		t.Fatal("recent pre_restore expired")
	}
	if len(expire) != 1 || expire[0] != "oldpre" {
		t.Fatalf("expire %v", expire)
	}
}

func TestRetainBucketsUseLocalTime(t *testing.T) {
	loc, _ := time.LoadLocation("America/Mexico_City")
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, loc)
	// 23:30 and 00:30 local are different days even if UTC says otherwise.
	in := []RetentionItem{
		{ID: "a", Started: time.Date(2026, 10, 17, 0, 30, 0, 0, loc), Verified: true},
		{ID: "b", Started: time.Date(2026, 10, 16, 23, 30, 0, 0, loc), Verified: true},
	}
	keep, _ := Retain(in, RetentionPolicy{KeepDaily: 2, MinVerified: 1, Location: loc}, now)
	if len(keep) != 2 {
		t.Fatalf("keep %v", keep)
	}
}

// keepLastReason and noPlaceReason are the reasons keep_last gives: a place
// among the N most recent, or a pre_restore backup kept without taking one.
func keepLastReason(n int) string { return fmt.Sprintf("among the %d most recent", n) }

func noPlaceReason(n int) string {
	return fmt.Sprintf("before a restore, fewer than %d newer backups of other kinds", n)
}

func sortedIDs(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// TestRetainPreRestoreTakesNoKeepLastPlace is the 1.2.2 acceptance in
// miniature: four restores leave eight pre_restore backups, and the
// backup just restored and the scheduled one before them stay among the
// last 3.
func TestRetainPreRestoreTakesNoKeepLastPlace(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	at := func(seq int64) time.Time { return now.Add(-time.Duration(12-seq) * time.Minute) }
	in := []RetentionItem{
		{ID: "restored", Seq: 1, Started: at(1), Kind: KindManual, Verified: true},
		{ID: "scheduled", Seq: 2, Started: at(2), Kind: KindScheduled, Verified: true},
	}
	var pre []string
	for i := 0; i < 4; i++ {
		seq := int64(3 + 2*i)
		p, prev := fmt.Sprintf("pre%d", i), fmt.Sprintf("prev%d", i)
		in = append(in,
			RetentionItem{ID: p, Seq: seq, Started: at(seq), Kind: KindPreRestore, Verified: true},
			RetentionItem{ID: prev, Seq: seq + 1, Started: at(seq + 1), Kind: KindPreRestore, Verified: true, Aux: true})
		pre = append(pre, p, prev)
	}
	in = append(in, RetentionItem{ID: "next", Seq: 11, Started: at(11), Kind: KindScheduled, Verified: true})
	keep, expire := Retain(in, RetentionPolicy{KeepLast: 3, MinVerified: 1}, now)
	if len(expire) != 0 {
		t.Fatalf("expired %v; keep %v", expire, keep)
	}
	for _, id := range []string{"restored", "scheduled", "next"} {
		if keep[id] != keepLastReason(3) {
			t.Errorf("%s kept as %q, want %q", id, keep[id], keepLastReason(3))
		}
	}
	for _, id := range pre {
		if keep[id] != "protected (Before restore)" {
			t.Errorf("%s kept as %q", id, keep[id])
		}
	}
}

// TestRetainPreRestorePastProtection: past its 30 days a pre_restore backup
// stays while fewer than keep_last newer backups of other kinds exist, and
// goes once there are keep_last of them.
func TestRetainPreRestorePastProtection(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	in := []RetentionItem{
		{ID: "old", Seq: 1, Started: now.Add(-45 * day), Kind: KindScheduled, Verified: true},
		{ID: "pre", Seq: 2, Started: now.Add(-40 * day), Kind: KindPreRestore, Verified: true},
		{ID: "prev", Seq: 3, Started: now.Add(-40*day + time.Minute), Kind: KindPreRestore, Verified: true, Aux: true},
		{ID: "after", Seq: 4, Started: now.Add(-39 * day), Kind: KindScheduled, Verified: true},
	}
	p := RetentionPolicy{KeepLast: 2, MinVerified: 1}
	keep, expire := Retain(in, p, now)
	if len(expire) != 0 {
		t.Fatalf("expired %v; keep %v", expire, keep)
	}
	for id, want := range map[string]string{"old": keepLastReason(2), "after": keepLastReason(2), "pre": noPlaceReason(2), "prev": noPlaceReason(2)} {
		if keep[id] != want {
			t.Errorf("%s kept as %q, want %q", id, keep[id], want)
		}
	}
	in = append(in, RetentionItem{ID: "new", Seq: 5, Started: now, Kind: KindScheduled, Verified: true})
	keep, expire = Retain(in, p, now)
	if got := fmt.Sprint(sortedIDs(expire)); got != "[old pre prev]" {
		t.Fatalf("expired %s, want [old pre prev]; keep %v", got, keep)
	}
	for _, id := range []string{"new", "after"} {
		if keep[id] != keepLastReason(2) {
			t.Errorf("%s kept as %q, want %q", id, keep[id], keepLastReason(2))
		}
	}
}

// TestRetainPreRestoreWithFewerOtherBackupsThanKeepLast: while the library
// has fewer than keep_last backups of other kinds, keep_last keeps every
// pre_restore backup, whatever its age.
func TestRetainPreRestoreWithFewerOtherBackupsThanKeepLast(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	var in []RetentionItem
	for i := 1; i <= 4; i++ {
		in = append(in, RetentionItem{ID: fmt.Sprintf("pre%d", i), Seq: int64(i), Started: now.Add(-time.Duration(51-i) * day), Kind: KindPreRestore, Verified: true})
	}
	in = append(in, RetentionItem{ID: "regular", Seq: 5, Started: now.Add(-46 * day), Kind: KindScheduled, Verified: true})
	keep, expire := Retain(in, RetentionPolicy{KeepLast: 3, MinVerified: 1}, now)
	if len(expire) != 0 {
		t.Fatalf("expired %v; keep %v", expire, keep)
	}
	if keep["regular"] != keepLastReason(3) {
		t.Errorf("regular kept as %q", keep["regular"])
	}
	for i := 1; i <= 4; i++ {
		if id := fmt.Sprintf("pre%d", i); keep[id] != noPlaceReason(3) {
			t.Errorf("%s kept as %q, want %q", id, keep[id], noPlaceReason(3))
		}
	}
}

// TestRetainOtherProtectedKindsTakeKeepLastPlaces: pre_delete and
// pre_upgrade backups still take keep_last places like any other kind.
func TestRetainOtherProtectedKindsTakeKeepLastPlaces(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	in := []RetentionItem{
		{ID: "old", Seq: 1, Started: now.Add(-50 * day), Kind: KindScheduled, Verified: true},
		{ID: "del", Seq: 2, Started: now.Add(-45 * day), Kind: KindPreDelete, Verified: true},
		{ID: "upg", Seq: 3, Started: now.Add(-44 * day), Kind: KindPreUpgrade, Verified: true},
	}
	keep, expire := Retain(in, RetentionPolicy{KeepLast: 2, MinVerified: 1}, now)
	if got := fmt.Sprint(sortedIDs(expire)); got != "[old]" {
		t.Fatalf("expired %s, want [old]; keep %v", got, keep)
	}
	for _, id := range []string{"del", "upg"} {
		if keep[id] != keepLastReason(2) {
			t.Errorf("%s kept as %q, want %q", id, keep[id], keepLastReason(2))
		}
	}
}

// TestRetainPreRestoreStillCountsForCalendarRules: days, weeks and months
// count every kind, so a pre_restore backup can be the one kept for its
// month and the regular backup of that month expires in its stead.
func TestRetainPreRestoreStillCountsForCalendarRules(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	day := now.Add(-40 * 24 * time.Hour)
	at := func(h int) time.Time { return time.Date(day.Year(), day.Month(), day.Day(), h, 0, 0, 0, time.UTC) }
	in := []RetentionItem{
		{ID: "regular", Seq: 1, Started: at(2), Kind: KindScheduled, Verified: true},
		{ID: "pre", Seq: 2, Started: at(15), Kind: KindPreRestore, Verified: true},
	}
	keep, expire := Retain(in, RetentionPolicy{KeepMonthly: 12, MinVerified: 1}, now)
	if keep["pre"] != "last of the month" {
		t.Fatalf("pre kept as %q", keep["pre"])
	}
	if got := fmt.Sprint(sortedIDs(expire)); got != "[regular]" {
		t.Fatalf("expired %s, want [regular]; keep %v", got, keep)
	}
}

// TestRetainPreRestoreTakesNoPlaceProperty: on random catalogs, the
// keep_last most recent backups that are not pre_restore are kept whatever
// pre_restore backups sit among them, and every backup that counting all
// kinds kept through keep_last is still kept.
func TestRetainPreRestoreTakesNoPlaceProperty(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(2))
	failed := map[string]bool{}
	fail := func(prop, format string, args ...any) {
		if !failed[prop] {
			failed[prop] = true
			t.Errorf("property "+prop+": "+format, args...)
		}
	}
	for iter := 0; iter < 500; iter++ {
		n := rng.Intn(80) + 1
		perm := rng.Perm(n)
		in := make([]RetentionItem, n)
		for i := range in {
			in[i] = RetentionItem{
				ID:       fmt.Sprintf("X%03d", i),
				Seq:      int64(perm[i] + 1),
				Started:  now.Add(-time.Duration(rng.Intn(400*24)) * time.Hour),
				Kind:     []string{KindScheduled, KindManual, KindPreRestore, KindPreDelete}[rng.Intn(4)],
				Pinned:   rng.Intn(15) == 0,
				Verified: rng.Intn(3) > 0,
				Aux:      rng.Intn(6) == 0,
			}
		}
		p := RetentionPolicy{KeepLast: rng.Intn(6), KeepDaily: rng.Intn(3), KeepWeekly: rng.Intn(3), KeepMonthly: rng.Intn(3), MinVerified: 1 + rng.Intn(3)}
		keep, _ := Retain(in, p, now)
		bySeq := append([]RetentionItem(nil), in...)
		sort.Slice(bySeq, func(i, j int) bool { return bySeq[i].Seq > bySeq[j].Seq })
		others, last := 0, 0
		for i, it := range bySeq {
			_, kept := keep[it.ID]
			if it.Kind != KindPreRestore && others < p.KeepLast && !kept {
				fail("a", "catalog %d: %s, one of the %d newest that are not pre_restore, expired", iter, it.ID, p.KeepLast)
			}
			if i < p.KeepLast && !kept {
				fail("b", "catalog %d: %s, one of the %d newest, expired", iter, it.ID, p.KeepLast)
			}
			if keep[it.ID] == keepLastReason(p.KeepLast) {
				last++
				if it.Kind == KindPreRestore {
					fail("c", "catalog %d: pre_restore %s took a keep_last place", iter, it.ID)
				}
			}
			if keep[it.ID] == noPlaceReason(p.KeepLast) && (it.Kind != KindPreRestore || others >= p.KeepLast) {
				fail("d", "catalog %d: %s (%s) kept as %q with %d newer backups of other kinds", iter, it.ID, it.Kind, keep[it.ID], others)
			}
			if it.Kind != KindPreRestore {
				others++
			}
		}
		if last > p.KeepLast {
			fail("c", "catalog %d: %d backups kept as among the %d most recent", iter, last, p.KeepLast)
		}
	}
}

func TestSuggestSlug(t *testing.T) {
	cases := map[string]string{
		"Cliente X, temporada 2": "cliente_x_temporada_2",
		"Música 2026":            "musica_2026",
		"2026 boda":              "library_2026_boda",
		"!!!":                    "library",
		"pg stuff":               "lib_pg_stuff",
		"a":                      "a_x",
	}
	for in, want := range cases {
		if got := SuggestSlug(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestParseArchiveHeader(t *testing.T) {
	out := `;
; Archive created at 2026-10-18 19:00:00 UTC
;     dbname: cliente_x
;     TOC Entries: 12
;     Compression: gzip
;     Dump Version: 1.16-0
;     Format: CUSTOM
;     Dumped from database version: 18.6
;     Dumped by pg_dump version: 18.6
;
`
	a := ParseArchiveHeader(out)
	if a.Format != "CUSTOM" || a.DumpedBy != "18.6" || a.Entries != 12 || a.Compression != "gzip" {
		t.Fatalf("%+v", a)
	}
}
