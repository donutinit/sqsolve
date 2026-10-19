// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/store"
)

func TestChain(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "a.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	l := New(db)
	u := Actor{Type: "user", ID: "u1", Name: "diego", IP: "192.168.1.5"}
	for i := 0; i < 5; i++ {
		if err := l.Record(ctx, u, "library.create", "library", "L1", OK, map[string]any{"slug": "x", "n": i}); err != nil {
			t.Fatal(err)
		}
	}
	broken, n, err := l.Verify(ctx)
	if err != nil || broken != 0 || n != 5 {
		t.Fatalf("verify: %d %d %v", broken, n, err)
	}
	// Tamper with entry 3.
	if _, err := db.ExecContext(ctx, `UPDATE audit_log SET outcome = 'denied' WHERE seq = 3`); err != nil {
		t.Fatal(err)
	}
	broken, _, _ = l.Verify(ctx)
	if broken != 3 {
		t.Fatalf("tampering not detected at 3, got %d", broken)
	}
	// Deleting an entry also breaks the chain.
	db.ExecContext(ctx, `UPDATE audit_log SET outcome = 'ok' WHERE seq = 3`)
	db.ExecContext(ctx, `DELETE FROM audit_log WHERE seq = 2`)
	broken, _, _ = l.Verify(ctx)
	if broken != 3 {
		t.Fatalf("deletion not detected, got %d", broken)
	}
	var buf bytes.Buffer
	n2, err := l.Export(ctx, &buf, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil || n2 != 4 || !strings.Contains(buf.String(), `"action":"library.create"`) {
		t.Fatalf("export %d %v %s", n2, err, buf.String())
	}
	list, err := l.List(ctx, 0, 2, "")
	if err != nil || len(list) != 2 || list[0].Seq != 5 {
		t.Fatalf("list %+v %v", list, err)
	}
}
