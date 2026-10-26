// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/events"
)

// The Events tab filters by minimum severity and by type; the audit log
// tab and its JSON Lines export are for audit.view (an operator, not a
// viewer); the export holds every entry in order, and the tab tells when
// the hash chain is broken.
func TestActivityEventsAndAuditLog(t *testing.T) {
	a, srv, _ := jobsPanel(t)
	ctx := context.Background()
	fxViewer(t, a)
	a.Events.Emit(ctx, events.Event{Type: "backup.failed", Severity: events.Error, Message: "Backup of apolo failed: disk full"})
	a.Events.Emit(ctx, events.Event{Type: "backup.stale", Severity: events.Warning, Message: "No verified backup of hera for 50 hours"})
	a.Events.Emit(ctx, events.Event{Type: "library.created", Severity: events.Info, Message: "Created zeus"})
	eve, _ := jobsLogin(t, srv, "eve", fxViewerPass)
	all := eve.page("/activity")
	for _, m := range []string{"disk full", "for 50 hours", "Created zeus"} {
		if !strings.Contains(all, m) {
			t.Errorf("events tab lacks %q", m)
		}
	}
	if strings.Contains(all, `href="/activity?tab=audit"`) {
		t.Error("a viewer is offered the audit log")
	}
	if page := eve.page("/activity?sev=error"); !strings.Contains(page, "disk full") || strings.Contains(page, "for 50 hours") || strings.Contains(page, "Created zeus") ||
		!strings.Contains(page, `<option value="error" selected>`) {
		t.Error("the severity filter does not narrow the events")
	}
	if page := eve.page("/activity?type=library."); !strings.Contains(page, "Created zeus") || strings.Contains(page, "disk full") {
		t.Error("the type filter does not narrow the events")
	}
	if page := eve.page("/activity?sev=critical"); !strings.Contains(page, "No events.") {
		t.Error("an empty filter does not say so")
	}
	if st, _, body := eve.do("GET", "/activity?tab=audit", nil); st != http.StatusForbidden || !strings.Contains(body, "SQS-A030 Your role does not allow viewing the audit log.") {
		t.Fatalf("viewer audit tab: %d", st)
	}
	if st, _, _ := eve.do("GET", "/activity/audit.jsonl", nil); st != http.StatusForbidden {
		t.Fatalf("viewer audit export: %d", st)
	}

	op, _ := jobsLogin(t, srv, "op", jobsOpPass)
	total, _ := a.Audit.List(ctx, 0, 1000, "")
	if page := op.page("/activity?tab=audit"); !strings.Contains(page, "Chain intact: ") || !strings.Contains(page, "user.create") {
		t.Fatal("the audit tab does not show the chain or its entries")
	}
	resp, err := op.c.Get(srv.URL + "/activity/audit.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-ndjson" ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="sqsolve-audit-`+time.Now().Format("20060102")+`.jsonl"` {
		t.Fatalf("export: %d %v", resp.StatusCode, resp.Header)
	}
	sc := bufio.NewScanner(resp.Body)
	var seqs []int64
	actions := map[string]bool{}
	for sc.Scan() {
		var e audit.Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		seqs = append(seqs, e.Seq)
		actions[e.Action] = true
	}
	resp.Body.Close()
	// The export itself is no audited action, so it holds what the list had
	// plus the logins since.
	if len(seqs) < len(total) || !actions["user.create"] || !actions["auth.login"] {
		t.Fatalf("export holds %d entries (%v), the log had %d", len(seqs), actions, len(total))
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("export out of order: %v", seqs)
		}
	}

	if _, err := a.DB.ExecContext(ctx, `UPDATE audit_log SET actor_name = 'mallory' WHERE seq = 2`); err != nil {
		t.Fatal(err)
	}
	if page := op.page("/activity?tab=audit"); !strings.Contains(page, "The audit chain is broken at entry 2: someone modified the sqsolve database.") {
		t.Fatal("a broken chain is not reported")
	}
}
