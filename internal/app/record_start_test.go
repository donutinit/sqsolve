// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"testing"

	"github.com/donutinit/sqsolve/internal/events"
)

// Every start is an event; a start with another version than the last one
// is also an upgrade, in the events and in the audit log.
func TestRecordStartDetectsAnUpgrade(t *testing.T) {
	ctx := context.Background()
	a := openApp(t, ctx, testConfig(t))
	defer a.Close()
	saved := Version
	defer func() { Version = saved }()
	count := func(typ string) (int, string) {
		t.Helper()
		evs, err := a.Events.List(ctx, events.Filter{TypePrefix: typ})
		if err != nil {
			t.Fatal(err)
		}
		n, last := 0, ""
		for _, e := range evs {
			if e.Type == typ {
				if n == 0 {
					last = e.Message
				}
				n++
			}
		}
		return n, last
	}
	upgrades := func() []map[string]any {
		t.Helper()
		list, err := a.Audit.List(ctx, 0, 50, "")
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]any
		for _, e := range list {
			if e.Action == "system.upgrade" {
				out = append(out, e.Details)
			}
		}
		return out
	}

	Version = "1.2.0"
	a.RecordStart(ctx)
	if n, msg := count(events.SystemStarted); n != 1 || msg != "sqsolve 1.2.0 started" {
		t.Fatalf("start events %d %q", n, msg)
	}
	if n, _ := count(events.SystemUpgraded); n != 0 || len(upgrades()) != 0 {
		t.Fatal("the first start was taken for an upgrade")
	}
	a.RecordStart(ctx)
	if n, _ := count(events.SystemUpgraded); n != 0 {
		t.Fatal("a start with the same version was taken for an upgrade")
	}

	Version = "1.2.1"
	a.RecordStart(ctx)
	if n, msg := count(events.SystemUpgraded); n != 1 || msg != "sqsolve was upgraded from 1.2.0 to 1.2.1" {
		t.Fatalf("upgrade events %d %q", n, msg)
	}
	if u := upgrades(); len(u) != 1 || u[0]["from"] != "1.2.0" || u[0]["to"] != "1.2.1" {
		t.Fatalf("upgrade audit entries %+v", u)
	}
	var last string
	if ok, err := a.DB.GetSetting(ctx, "last_version", &last); !ok || err != nil || last != "1.2.1" {
		t.Fatalf("last version %q %v %v", last, ok, err)
	}
}
