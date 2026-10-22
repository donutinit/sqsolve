// SPDX-License-Identifier: AGPL-3.0-only

package scheduler

import (
	"testing"
	"time"
)

// Every expression Validate accepts gives a run strictly after the
// reference, or an error (a date that never comes, such as 31 February);
// never a panic or a run in the past.
func FuzzNext(f *testing.F) {
	for _, s := range []string{"0 2 * * *", "0 9-21/4 * * 1-6", "*/15 * * * *", "0 0 31 2 *", "59 23 29 2 *", "0 0 * * 7"} {
		f.Add(s, int64(1792627200))
	}
	madrid, _ := time.LoadLocation("Europe/Madrid")
	f.Fuzz(func(t *testing.T, cron string, unix int64) {
		if _, err := Validate(cron, "UTC"); err != nil {
			return
		}
		ref := time.Unix(unix%(1<<34), 0).UTC()
		for _, loc := range []*time.Location{time.UTC, madrid} {
			next, err := Next(cron, loc, ref, 0)
			if err == nil && !next.After(ref) {
				t.Fatalf("%q after %s gave %s", cron, ref, next)
			}
		}
	})
}
