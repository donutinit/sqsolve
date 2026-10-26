// SPDX-License-Identifier: AGPL-3.0-only

package core

// Requests that change or destroy a library are checked before anything is
// queued, audited or sent to PostgreSQL: unconfirmed, aimed at a library or
// backup in a state that does not allow them, or with invalid values.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/donutinit/sqsolve/internal/audit"
	"github.com/donutinit/sqsolve/internal/jobs"
	"github.com/donutinit/sqsolve/internal/pg"
	"github.com/donutinit/sqsolve/internal/secrets"
	"github.com/donutinit/sqsolve/internal/store"
	"github.com/donutinit/sqsolve/internal/ulid"
)

// refused checks that err is exactly the error want.
func refused(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Errorf("%s: got %v, want %q", what, err, want)
	}
}

// untouched checks that nothing was queued or audited.
func untouched(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	if list, _ := s.Jobs.List(ctx, jobs.Filter{}); len(list) != 0 {
		t.Errorf("%d jobs queued by refused requests", len(list))
	}
	if entries, _ := s.Audit.List(ctx, 0, 100, ""); len(entries) != 0 {
		t.Errorf("%d audit entries written by refused requests: %+v", len(entries), entries)
	}
}

// Delete, purge, restore, recreate and backup requests name an existing
// library or backup, are confirmed with the identifier and match the state
// of the library; otherwise they are refused with their code, nothing is
// queued and nothing is audited.
func TestDestructiveRequestsAreRefusedBeforeQueueing(t *testing.T) {
	ctx := context.Background()
	s, l, b := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	set := func(st State) {
		t.Helper()
		if _, err := s.DB.ExecContext(ctx, `UPDATE libraries SET state = ? WHERE id = ?`, string(st), l.ID); err != nil {
			t.Fatal(err)
		}
	}
	const noLibrary, noBackup = "SQS-L002 library not found", "SQS-B001 backup not found"
	_, err := s.Trash(ctx, by, "NOPE", "lib")
	refused(t, "trash of an unknown library", err, noLibrary)
	_, err = s.RestoreFromTrash(ctx, by, "NOPE")
	refused(t, "untrash of an unknown library", err, noLibrary)
	_, err = s.Purge(ctx, by, "NOPE", "lib")
	refused(t, "purge of an unknown library", err, noLibrary)
	_, err = s.BackupNow(ctx, by, "NOPE")
	refused(t, "backup of an unknown library", err, noLibrary)
	_, err = s.Recreate(ctx, by, "NOPE", "", "lib")
	refused(t, "recreate of an unknown library", err, noLibrary)
	_, err = s.RestoreReplace(ctx, by, "NOPE", "lib", false)
	refused(t, "replace from an unknown backup", err, noBackup)
	_, _, err = s.RestoreAsNew(ctx, by, "NOPE", "copy_lib", "Copy")
	refused(t, "restore of an unknown backup", err, noBackup)

	// Not confirmed.
	_, err = s.Trash(ctx, by, l.ID, "LIB")
	refused(t, "trash typed with another identifier", err, "SQS-L040 type the library identifier to confirm")
	_, err = s.RestoreReplace(ctx, by, b.ID, "", false)
	refused(t, "replace without confirmation", err, "SQS-R002 type the library identifier to confirm")

	// An active library is not in the trash and has its database.
	_, err = s.RestoreFromTrash(ctx, by, l.ID)
	refused(t, "untrash of an active library", err, "SQS-L042 the library is not in the trash")
	_, err = s.Purge(ctx, by, l.ID, l.Slug)
	refused(t, "purge of an active library", err, "SQS-L044 only libraries in the trash can be purged")
	_, err = s.Recreate(ctx, by, l.ID, "", l.Slug)
	refused(t, "recreate of an active library", err, "SQS-R040 only libraries with no database on the server can be recreated")

	// A restore in progress blocks the rest.
	set(StateRestoring)
	_, err = s.Trash(ctx, by, l.ID, l.Slug)
	refused(t, "trash while restoring", err, "SQS-L041 cannot delete a library in state Restoring")
	_, err = s.RestoreReplace(ctx, by, b.ID, l.Slug, false)
	refused(t, "replace while restoring", err, "SQS-R003 cannot replace a library in state Restoring")
	_, err = s.BackupNow(ctx, by, l.ID)
	refused(t, "backup while restoring", err, "SQS-B003 cannot back up a library in state Restoring")

	// In the trash, the purge is confirmed with the identifier too.
	set(StateTrashed)
	_, err = s.Purge(ctx, by, l.ID, "wrong")
	refused(t, "purge typed with another identifier", err, "SQS-L040 type the library identifier to confirm")

	// A missing library is recreated only when confirmed and from a verified backup.
	set(StateMissing)
	_, err = s.Recreate(ctx, by, l.ID, "", "wrong")
	refused(t, "recreate typed with another identifier", err, "SQS-R002 type the library identifier to confirm")
	s.DB.ExecContext(ctx, `UPDATE backups SET verify_status = 'failed' WHERE library_id = ?`, l.ID)
	_, err = s.Recreate(ctx, by, l.ID, "", l.Slug)
	refused(t, "recreate without verified backups", err, "SQS-R041 the library has no verified backups")

	// A backup that failed cannot be restored, in place or as a new library.
	set(StateActive)
	s.DB.ExecContext(ctx, `UPDATE backups SET status = 'failed' WHERE id = ?`, b.ID)
	_, err = s.RestoreReplace(ctx, by, b.ID, l.Slug, false)
	refused(t, "replace from a failed backup", err, "SQS-R001 the backup cannot be restored (it failed or was deleted)")
	_, _, err = s.RestoreAsNew(ctx, by, b.ID, "copy_lib", "Copy")
	refused(t, "restore of a failed backup", err, "SQS-R001 the backup cannot be restored (it failed or was deleted)")
	s.DB.ExecContext(ctx, `UPDATE backups SET status = 'ok', verify_status = 'l1_ok' WHERE id = ?`, b.ID)
	_, _, err = s.RestoreAsNew(ctx, by, b.ID, "copy_lib", " ")
	refused(t, "restore without a name", err, "SQS-L003 the display name must be 1 to 80 characters long")
	_, _, err = s.RestoreAsNew(ctx, by, b.ID, "copy_lib", "Copy\x07")
	refused(t, "restore with a control character", err, "SQS-L003 the display name cannot contain control characters")
	_, _, err = s.RestoreAsNew(ctx, by, b.ID, l.Slug, "Copy")
	refused(t, "restore onto a taken identifier", err, `SQS-L004 a library with identifier "lib" already exists`)
	_, _, err = s.RestoreAsNew(ctx, by, b.ID, "Copy Lib", "Copy")
	if !errors.Is(err, pg.ErrInvalidSlug) {
		t.Errorf("restore with an invalid identifier: %v", err)
	}
	if n := countLibraries(t, s); n != 1 {
		t.Errorf("%d libraries after refused restores", n)
	}
	untouched(t, s)
}

// insertLibrary stores a library row and returns its id.
func insertLibrary(t *testing.T, s *Service, slug string, st State) string {
	t.Helper()
	id, now := ulid.New(), store.NowT()
	if _, err := s.DB.ExecContext(context.Background(), `INSERT INTO libraries(id, slug, display_name, state, db_name, owner_role, creation_strategy, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'resolve', ?, ?)`, id, slug, slug, string(st), slug, slug, now, now); err != nil {
		t.Fatal(err)
	}
	return id
}

func countLibraries(t *testing.T, s *Service) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRowContext(context.Background(), `SELECT count(*) FROM libraries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Edits, maintenance, members, credentials and imports validate their
// values and the state of the library before they reach PostgreSQL or the
// audit log; a credential is only reachable through its own library.
func TestLibraryChangesAreValidatedFirst(t *testing.T) {
	ctx := context.Background()
	s, l, _ := auditedCatalog(t)
	by := audit.Actor{Type: "user", ID: "u1", Name: "diego"}
	for _, c := range []struct {
		in   UpdateInput
		want string
	}{
		{UpdateInput{DisplayName: "Lib", ConnLimit: -2}, "SQS-L012 invalid connection limit (-1 for unlimited, up to 1000)"},
		{UpdateInput{DisplayName: "Lib", ConnLimit: 1001}, "SQS-L012 invalid connection limit (-1 for unlimited, up to 1000)"},
		{UpdateInput{DisplayName: "Lib", ResolveVersion: strings.Repeat("9", 21)}, "SQS-L013 Resolve version too long"},
		{UpdateInput{DisplayName: "Lib", Notes: strings.Repeat("n", 10001)}, "SQS-L014 notes too long"},
		{UpdateInput{DisplayName: strings.Repeat("x", 81)}, "SQS-L003 the display name must be 1 to 80 characters long"},
	} {
		refused(t, "update", s.UpdateLibrary(ctx, by, l.ID, c.in), c.want)
	}
	refused(t, "update of an unknown library", s.UpdateLibrary(ctx, by, "NOPE", UpdateInput{DisplayName: "x"}), "SQS-L002 library not found")
	if got, _ := s.Library(ctx, l.ID); got.DisplayName != l.DisplayName || got.ConnLimit != l.ConnLimit {
		t.Errorf("a refused update changed the library: %+v", got)
	}
	_, err := s.SetMaintenance(ctx, by, l.ID, false, false)
	refused(t, "leave maintenance of an active library", err, "SQS-L015 cannot change maintenance in state Active")
	refused(t, "reopen the window of an active library", s.ReopenInitWindow(ctx, by, l.ID), "SQS-L011 the library is not waiting for initialization")
	_, err = s.AddMember(ctx, by, l.ID, "Ana Maria")
	refused(t, "a member with an invalid name", err, "SQS-L001 invalid identifier: a person name uses 2 to 20 lowercase letters or digits, starting with a letter")
	refused(t, "revoke the owner", s.RevokeMember(ctx, by, l.ID, "C1"), "SQS-L034 the primary credential cannot be revoked; rotate its password")
	refused(t, "revoke an unknown credential", s.RevokeMember(ctx, by, l.ID, "NOPE"), "SQS-L030 credential not found")
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b"} {
		_, err := s.Import(ctx, by, l.ID, name, false)
		refused(t, "import of "+name, err, "SQS-B050 invalid file name")
	}
	_, err = s.Import(ctx, by, "NOPE", "x.backup", false)
	refused(t, "import into an unknown library", err, "SQS-L002 library not found")

	// A revoked credential is neither shown nor rotated.
	enc, _ := s.Box.Seal(secrets.New("gone"), credAAD("C2"))
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO library_credentials(id, library_id, kind, pg_role, person_label, password_enc, created_at, revoked_at) VALUES ('C2', ?, 'member', 'lib__bea', 'bea', ?, ?, ?)`,
		l.ID, enc, store.NowT(), store.NowT()); err != nil {
		t.Fatal(err)
	}
	_, err = s.RevealPassword(ctx, by, l.ID, "C2")
	refused(t, "reveal a revoked credential", err, "SQS-L031 the credential is revoked")
	refused(t, "rotate a revoked credential", s.RotatePassword(ctx, by, l.ID, "C2"), "SQS-L031 the credential is revoked")
	// A credential of one library is not found through another.
	other := insertLibrary(t, s, "other_lib", StateActive)
	_, err = s.RevealPassword(ctx, by, other, "C1")
	refused(t, "reveal through another library", err, "SQS-L030 credential not found")
	_, err = s.Connection(ctx, other, "C1")
	refused(t, "connection through another library", err, "SQS-L030 credential not found")
	refused(t, "rotate through another library", s.RotatePassword(ctx, by, other, "C1"), "SQS-L030 credential not found")

	// Trashed libraries take no members.
	s.DB.ExecContext(ctx, `UPDATE libraries SET state = 'trashed' WHERE id = ?`, l.ID)
	_, err = s.AddMember(ctx, by, l.ID, "ana")
	refused(t, "a member for a trashed library", err, "SQS-L032 people can only be added to active libraries")
	_, err = s.SetMaintenance(ctx, by, l.ID, true, false)
	refused(t, "maintenance of a trashed library", err, "SQS-L015 cannot change maintenance in state In the trash")
	untouched(t, s)
}

// The connection data of a library is what a workstation types in Resolve:
// the advertised address, the port, the database and the role, never the
// password.
func TestConnectionInfo(t *testing.T) {
	ctx := context.Background()
	s, l, _ := auditedCatalog(t)
	s.Cfg.PGPort, s.Cfg.AdvertisedHost = 5432, "10.0.0.5"
	c, err := s.Connection(ctx, l.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if *c != (ConnectionInfo{LibraryName: l.DisplayName, Host: "10.0.0.5", Port: 5432, Database: l.DBName, User: "lib"}) {
		t.Fatalf("connection %+v", c)
	}
	st := s.Settings(ctx)
	st.AdvertisedHost = "nas.local"
	if err := s.SaveSettings(ctx, audit.Actor{Type: "user", ID: "u1", Name: "diego"}, st); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Connection(ctx, l.ID, ""); c.Host != "nas.local" {
		t.Fatalf("the address in Settings is not used: %+v", c)
	}
	if _, err := s.Connection(ctx, "NOPE", ""); !errors.Is(err, ErrLibraryNotFound) {
		t.Fatalf("connection of an unknown library: %v", err)
	}
}
