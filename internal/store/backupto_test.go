// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The copy is a complete database with what was committed, readable on its
// own, and a stale .partial file of an earlier copy is replaced.
func TestBackupToCopiesTheData(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if err := db.SetSetting(ctx, "k", "v", "test"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy.db")
	os.WriteFile(dst+".partial", []byte("left by a crash"), 0o644)
	if err := db.BackupTo(ctx, dst); err != nil {
		t.Fatal(err)
	}
	cp, err := sql.Open("sqlite", "file:"+dst+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var v, check string
	if err := cp.QueryRow(`SELECT value FROM settings WHERE key = 'k'`).Scan(&v); err != nil || v != `"v"` {
		t.Fatalf("copy: %q %v", v, err)
	}
	cp.QueryRow(`PRAGMA integrity_check`).Scan(&check)
	if check != "ok" {
		t.Fatalf("copy integrity: %s", check)
	}
}

// When the finished copy cannot take its name (a folder is there) the
// error is SQS-X030 and the .partial file is removed.
func TestBackupToCleansUpAFailedRename(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "copy.db")
	os.MkdirAll(filepath.Join(dst, "inside"), 0o700)
	err := db.BackupTo(ctx, dst)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-X030 could not copy the database to "+dst+": ") {
		t.Fatalf("want SQS-X030, got %v", err)
	}
	if _, err := os.Stat(dst + ".partial"); !os.IsNotExist(err) {
		t.Fatal("the .partial file was left behind")
	}
}

// A copy over an existing file replaces it.
func TestBackupToReplacesAnOlderCopy(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	dst := filepath.Join(t.TempDir(), "copy.db")
	os.WriteFile(dst, []byte("old"), 0o600)
	if err := db.BackupTo(ctx, dst); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	if !strings.HasPrefix(string(b), "SQLite format 3\x00") {
		t.Fatalf("the old file is still there: %q", b[:min(len(b), 16)])
	}
}
