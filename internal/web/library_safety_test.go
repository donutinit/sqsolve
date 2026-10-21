// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/core"
)

// TestRecreateAsksForTheIdentifier: the Recreate form of a missing library
// posts a typed confirmation, like the other destructive forms, and offers
// Revert when the previous state is still on the server. Revert is not
// offered for a library in the trash.
func TestRecreateAsksForTheIdentifier(t *testing.T) {
	l := &core.Library{ID: "01M5A5X0JZQYHV8GE4H1PK6VG9", Slug: "apolo", DisplayName: "Apolo", State: core.StateMissing, PrevDB: "apolo__prev_202610191200"}
	out := renderPage(t, "library", map[string]any{"L": l, "Tab": "connection"})
	for _, want := range []string{`action="/libraries/01M5A5X0JZQYHV8GE4H1PK6VG9/recreate"`, `name="confirm"`, `data-confirm-match="apolo"`, `data-needs-match>Recreate from the last backup`,
		`href="/libraries/01M5A5X0JZQYHV8GE4H1PK6VG9/revert">Revert</a>`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing library page lacks %s", want)
		}
	}
	l.State = core.StateTrashed
	out = renderPage(t, "library", map[string]any{"L": l, "Tab": "danger"})
	if strings.Contains(out, "/revert") {
		t.Error("Revert offered for a library in the trash")
	}
	l.State = core.StateMaintenance
	out = renderPage(t, "library", map[string]any{"L": l, "Tab": "danger"})
	if !strings.Contains(out, "/revert") {
		t.Error("Revert not offered for a library in maintenance")
	}
}
