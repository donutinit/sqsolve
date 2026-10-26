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
