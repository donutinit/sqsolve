// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"
	"unicode/utf8"
)

// retentionItems builds backups from fuzz bytes, three bytes each: hours
// before now, flags (pinned, verified, aux, kind) and the insertion order.
func retentionItems(data []byte, now time.Time) []RetentionItem {
	kinds := []string{KindManual, KindScheduled, KindPreRestore, KindPreDelete}
	var items []RetentionItem
	for i := 0; i+2 < len(data) && len(items) < 60; i += 3 {
		flags := data[i+1]
		items = append(items, RetentionItem{
			ID:       fmt.Sprintf("B%02d", len(items)),
			Seq:      int64(data[i+2]),
			Started:  now.Add(-time.Duration(data[i]) * 7 * time.Hour),
			Kind:     kinds[int(flags>>3)%len(kinds)],
			Pinned:   flags&1 != 0,
			Verified: flags&2 != 0,
			Aux:      flags&4 != 0,
		})
	}
	return items
}

// Properties of docs 16 §2: a pinned backup is never expired, the minimum of
// verified backups is kept when there are enough, every backup is either
// kept or expired, and the result does not depend on the input order.
func FuzzRetain(f *testing.F) {
	f.Add([]byte{1, 3, 1, 30, 2, 2, 200, 1, 3}, uint8(1), uint8(2), uint8(1), uint8(1), uint8(2))
	f.Add([]byte{0, 0, 0, 0, 0, 0, 5, 18, 9, 90, 10, 4}, uint8(0), uint8(0), uint8(0), uint8(0), uint8(3))
	f.Fuzz(func(t *testing.T, data []byte, last, daily, weekly, monthly, minVerified uint8) {
		now := time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC)
		loc, _ := time.LoadLocation("Europe/Madrid")
		items := retentionItems(data, now)
		p := RetentionPolicy{KeepLast: int(last % 20), KeepDaily: int(daily % 20), KeepWeekly: int(weekly % 10), KeepMonthly: int(monthly % 10),
			MinVerified: int(minVerified % 5), Location: loc}
		keep, expire := Retain(items, p, now)
		if len(keep)+len(expire) != len(items) {
			t.Fatalf("%d kept and %d expired of %d", len(keep), len(expire), len(items))
		}
		verified, verifiedKept := 0, 0
		for _, it := range items {
			_, kept := keep[it.ID]
			if it.Pinned && !kept {
				t.Fatalf("pinned %s expired", it.ID)
			}
			if kept && slices.Contains(expire, it.ID) {
				t.Fatalf("%s kept and expired", it.ID)
			}
			if it.Verified {
				verified++
				if kept {
					verifiedKept++
				}
			}
		}
		if verifiedKept < min(verified, p.MinVerified) {
			t.Fatalf("%d verified kept, minimum %d of %d", verifiedKept, p.MinVerified, verified)
		}
		reversed := slices.Clone(items)
		slices.Reverse(reversed)
		keep2, _ := Retain(reversed, p, now)
		if !maps.Equal(keep, keep2) {
			t.Fatalf("the order of the input changed the result")
		}
	})
}

// An accepted display name is trimmed, 1 to 80 characters, has no control
// characters and is accepted again unchanged.
func FuzzValidateDisplayName(f *testing.F) {
	for _, s := range []string{"Wedding Ana and Luis", "  x  ", "", "a\tb", "Boda Ñandú", string([]byte{0xff, 'a'})} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := ValidateDisplayName(s)
		if err != nil {
			return
		}
		if c := utf8.RuneCountInString(n); c < 1 || c > 80 {
			t.Fatalf("accepted %q with %d characters", n, c)
		}
		for _, r := range n {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("accepted a control character in %q", n)
			}
		}
		if again, err := ValidateDisplayName(n); err != nil || again != n {
			t.Fatalf("%q is not stable: %q %v", n, again, err)
		}
	})
}
