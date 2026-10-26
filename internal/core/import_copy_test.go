// SPDX-License-Identifier: AGPL-3.0-only

package core_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/donutinit/sqsolve/internal/core"
	"github.com/donutinit/sqsolve/internal/jobs"
)

// The import folder lists only finished regular files, not hidden ones,
// copies still being written (.partial) or folders. An import that keeps its
// source copies the file into the library folder and leaves the source as it
// was; on an unmarked backups volume nothing is imported.
func TestImportKeepingTheSourceCopiesIt(t *testing.T) {
	e := newQuietEnv(t)
	ctx := e.ctx
	l, _ := e.createActive("imp_copy")
	b := e.firstBackup(l)
	e.idle()
	defer core.SetImportSettle(0)()
	if c, err := e.svc.ImportCandidates(); err != nil || c != nil {
		t.Fatalf("candidates without an import folder: %v %v", c, err)
	}
	data, err := os.ReadFile(e.svc.AbsPath(b))
	if err != nil {
		t.Fatal(err)
	}
	dir := e.svc.ImportDir()
	os.MkdirAll(filepath.Join(dir, "subfolder"), 0o700)
	src := filepath.Join(dir, "kept.backup")
	for _, name := range []string{"kept.backup", ".hidden.backup", "copying.backup.partial"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := e.svc.ImportCandidates()
	if err != nil || len(c) != 1 || c[0].Name() != "kept.backup" || c[0].Size() != int64(len(data)) {
		t.Fatalf("candidates %v %v", c, err)
	}
	before, _ := os.Stat(src)

	restore := e.unmarkBackups()
	j, err := e.svc.Import(ctx, admin, l.ID, "kept.backup", false)
	if err != nil {
		t.Fatal(err)
	}
	e.failsWith(j, "SQS-S001", "SQS-S001 the volume has no sqsolve marker")
	restore()
	if n := len(e.okBackups(l.ID)); n != 1 {
		t.Fatalf("%d backups after an import into an unmarked volume", n)
	}

	j, err = e.svc.Import(ctx, admin, l.ID, "kept.backup", false)
	if err != nil {
		t.Fatal(err)
	}
	imported := e.backupNow(resultBackupID(t, e.wait(j, jobs.Succeeded)))
	if imported.Kind != core.KindImported || imported.VerifyStatus != core.VerifyL1OK || imported.SourceDB != "kept.backup" {
		t.Fatalf("imported %+v", imported)
	}
	after, err := os.Stat(src)
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("the source in the import folder changed: %v", err)
	}
	copied, err := os.Stat(e.svc.AbsPath(imported))
	if err != nil || os.SameFile(copied, after) {
		t.Fatalf("the catalogued file is not a copy of its own: %v", err)
	}
	if got, _ := os.ReadFile(e.svc.AbsPath(imported)); !bytes.Equal(got, data) {
		t.Fatal("the copy differs from the source")
	}
	if _, err := os.Stat(filepath.Join(dir, ".hidden.backup")); err != nil {
		t.Fatal("a hidden file in the import folder was touched")
	}
}
