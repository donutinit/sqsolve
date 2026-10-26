// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/events"
)

// GET /events lists newest first and filters by library, minimum severity
// and type prefix, with the id of the last event seen as cursor.
func TestAPIEvents(t *testing.T) {
	e := apiServer(t)
	ctx := context.Background()
	for _, ev := range []events.Event{
		{ID: "E1", Type: events.BackupSucceeded, Severity: events.Info, LibraryID: "L1"},
		{ID: "E2", Type: events.BackupFailed, Severity: events.Error, LibraryID: "L1"},
		{ID: "E3", Type: "storage.low", Severity: events.Warning},
		{ID: "E4", Type: "postgres.down", Severity: events.Critical},
	} {
		ev.Message = "message of " + ev.ID
		e.a.Events.Emit(ctx, ev)
	}
	for query, want := range map[string]string{
		"?library_id=L1":                   "E2,E1",
		"?min_severity=warning":            "E4,E3,E2",
		"?min_severity=critical":           "E4",
		"?type=backup.":                    "E2,E1",
		"?type=backup.&min_severity=error": "E2",
		"?cursor=E3&type=":                 "E2,E1",
		"?library_id=L9":                   "",
	} {
		r := e.call(t, "GET", "/api/v1/events"+query, e.read, "", false)
		var out struct {
			Items []events.Event `json:"items"`
		}
		decodeInto(t, r, &out)
		var ids []string
		for _, ev := range out.Items {
			if strings.HasPrefix(ev.ID, "E") {
				ids = append(ids, ev.ID)
			}
		}
		if r.status != 200 || strings.Join(ids, ",") != want {
			t.Errorf("events%s: %d %v, want %s", query, r.status, ids, want)
		}
	}
	r := e.call(t, "GET", "/api/v1/events?library_id=L1&type=backup.failed", e.read, "", false)
	var out struct {
		Items []events.Event `json:"items"`
	}
	decodeInto(t, r, &out)
	if len(out.Items) != 1 || out.Items[0].Message != "message of E2" || out.Items[0].Severity != events.Error || out.Items[0].TS.IsZero() {
		t.Fatalf("event: %s", r.body)
	}
}

// GET /audit lists entries newest first with their hash chain, filters by
// target and pages with the sequence number as cursor; it needs audit.view,
// which operators have and viewers do not.
func TestAPIAuditLog(t *testing.T) {
	e := apiServer(t)
	ctx := context.Background()
	by := audit.Actor{Type: "user", ID: "U9", Name: "diego", IP: "192.0.2.4"}
	for i := 1; i <= 3; i++ {
		if err := e.a.Audit.Record(ctx, by, "library.update", "library", "L1", audit.OK, map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	e.a.Audit.Record(ctx, by, "library.update", "library", "L2", audit.OK, nil)
	list := func(who *apiEnv, token, query string) []audit.Entry {
		t.Helper()
		r := who.call(t, "GET", "/api/v1/audit"+query, token, "", false)
		var out struct {
			Items []audit.Entry `json:"items"`
		}
		decodeInto(t, r, &out)
		if r.status != 200 {
			t.Fatalf("audit%s: %d %s", query, r.status, r.body)
		}
		return out.Items
	}
	l1 := list(e, e.admin, "?target_id=L1")
	if len(l1) != 3 || l1[0].Details["n"] != float64(3) || l1[2].Details["n"] != float64(1) || l1[0].Actor != by || l1[0].Seq <= l1[1].Seq {
		t.Fatalf("entries of L1: %+v", l1)
	}
	all := list(e, e.admin, "")
	for i := 0; i+1 < len(all); i++ {
		if all[i].PrevHash != all[i+1].Hash || all[i].Hash == "" {
			t.Fatalf("the chain is not in order at %d: %+v", i, all[i])
		}
	}
	if older := list(e, e.admin, "?target_id=L1&cursor="+strconv.FormatInt(l1[1].Seq, 10)); len(older) != 1 || older[0].Seq != l1[2].Seq {
		t.Fatalf("page after %d: %+v", l1[1].Seq, older)
	}
	if got := list(e.as(t, "olga", auth.RoleOperator), "cookie", "?target_id=L2"); len(got) != 1 || got[0].TargetID != "L2" {
		t.Fatalf("operator: %+v", got)
	}
	wantProblem(t, "viewer", e.as(t, "vera", auth.RoleViewer).call(t, "GET", "/api/v1/audit", "cookie", "", false), 403, a030)
}
