// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"errors"
	"testing"

	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// The row of a new library is forgotten only when its job was not queued.
// Enqueue can also fail once its transaction is committed (the request was
// cancelled before the job was read back): the job is queued then, and it
// needs the row it is about to create the library from.
func TestForgetProvisioningKeepsALibraryWithAJob(t *testing.T) {
	e := newBkEnv(t)
	cause := errors.New("the request was cancelled")
	rows := func(l *Library) (libs, policies int) {
		t.Helper()
		if err := e.svc.DB.QueryRowContext(e.ctx, `SELECT count(*) FROM libraries WHERE id = ?`, l.ID).Scan(&libs); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.DB.QueryRowContext(e.ctx, `SELECT count(*) FROM backup_policies WHERE library_id = ?`, l.ID).Scan(&policies); err != nil {
			t.Fatal(err)
		}
		return libs, policies
	}

	queued := e.library("queued_x", StateProvisioning)
	e.policy(queued, 10, 1)
	now := store.NowT()
	if _, err := e.svc.DB.ExecContext(e.ctx, `INSERT INTO jobs(id, type, library_id, state, scheduled_for, created_at, created_by_type) VALUES (?, ?, ?, 'queued', ?, ?, 'user')`,
		ulid.New(), JobLibraryCreate, queued.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.forgetProvisioning(e.ctx, queued.ID, cause); !errors.Is(err, cause) {
		t.Fatalf("got %v, want the cause", err)
	}
	if libs, policies := rows(queued); libs != 1 || policies != 1 {
		t.Fatalf("a library with a queued job lost its rows: %d library, %d policy", libs, policies)
	}

	refused := e.library("refused_x", StateProvisioning)
	e.policy(refused, 10, 1)
	if err := e.svc.forgetProvisioning(e.ctx, refused.ID, cause); !errors.Is(err, cause) {
		t.Fatalf("got %v, want the cause", err)
	}
	if libs, policies := rows(refused); libs != 0 || policies != 0 {
		t.Fatalf("a library without a job kept its rows: %d library, %d policy", libs, policies)
	}
}
