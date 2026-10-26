// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// A missing setting is reported as absent without an error and leaves the
// destination untouched.
func TestGetSettingMissing(t *testing.T) {
	db := openTemp(t)
	out := "default"
	ok, err := db.GetSetting(context.Background(), "nothing", &out)
	if ok || err != nil || out != "default" {
		t.Fatalf("%v %v %q", ok, err, out)
	}
}

// Setting a key again replaces its value and records who changed it.
func TestSetSettingReplaces(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if err := db.SetSetting(ctx, "retention", map[string]int{"days": 30}, "ana"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, "retention", map[string]int{"days": 90}, "bea"); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if ok, err := db.GetSetting(ctx, "retention", &got); !ok || err != nil || got["days"] != 90 {
		t.Fatalf("%v %v %v", ok, err, got)
	}
	var by string
	var rows int
	db.QueryRowContext(ctx, `SELECT updated_by, (SELECT COUNT(*) FROM settings WHERE key = 'retention') FROM settings WHERE key = 'retention'`).Scan(&by, &rows)
	if by != "bea" || rows != 1 {
		t.Fatalf("updated by %q, %d rows", by, rows)
	}
}

// A value that is not JSON cannot be stored, and a stored value that does
// not decode into the destination is reported as existing with the decode
// error.
func TestSettingEncodingErrors(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if err := db.SetSetting(ctx, "bad", make(chan int), "test"); err == nil || err.Error() != "json: unsupported type: chan int" {
		t.Fatalf("set: %v", err)
	}
	if ok, _ := db.GetSetting(ctx, "bad", new(any)); ok {
		t.Fatal("a value that failed to encode was stored")
	}
	db.SetSetting(ctx, "name", "text", "test")
	var n int
	ok, err := db.GetSetting(ctx, "name", &n)
	if !ok || err == nil || !strings.Contains(err.Error(), "cannot unmarshal string into Go value of type int") {
		t.Fatalf("get: %v %v", ok, err)
	}
}

// Reading or writing settings on a closed database reports the error.
func TestSettingsOnAClosedDatabase(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	db.Close()
	if _, err := db.GetSetting(ctx, "x", new(any)); err == nil || err.Error() != "sql: database is closed" {
		t.Fatalf("get: %v", err)
	}
	if err := db.SetSetting(ctx, "x", 1, "test"); err == nil || err.Error() != "sql: database is closed" {
		t.Fatalf("set: %v", err)
	}
}
