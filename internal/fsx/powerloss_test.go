// SPDX-License-Identifier: AGPL-3.0-only

package fsx

import (
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fsOp is one change to a folder, or a sync of one, as trace reports it.
type fsOp struct{ op, path, to string }

// survivors lists the states of the files that a power loss can leave while
// ops run on start. The model is POSIX without the help of a journal: a
// folder keeps its changes once it is synced, and until then any subset of
// its pending changes may survive, in order. A file is traced as created once
// its content is synced, so a surviving name always has its full content;
// created gives that content.
func survivors(start map[string]string, ops []fsOp, created map[string]string) []map[string]string {
	full, durable := maps.Clone(start), maps.Clone(start)
	type change struct {
		dir   string
		apply func(map[string]string)
	}
	var pending []change
	var out []map[string]string
	emit := func() {
		for mask := 0; mask < 1<<len(pending); mask++ {
			s := maps.Clone(durable)
			for i, c := range pending {
				if mask&(1<<i) != 0 {
					c.apply(s)
				}
			}
			out = append(out, s)
		}
	}
	emit()
	for _, o := range ops {
		var c change
		switch o.op {
		case "create":
			p, v := o.path, created[o.path]
			c = change{filepath.Dir(p), func(s map[string]string) { s[p] = v }}
		case "rename":
			from, to, v := o.path, o.to, full[o.path]
			c = change{filepath.Dir(to), func(s map[string]string) { delete(s, from); s[to] = v }}
		case "remove":
			p := o.path
			c = change{filepath.Dir(p), func(s map[string]string) { delete(s, p) }}
		case "sync":
			var left []change
			for _, p := range pending {
				if p.dir == o.path {
					p.apply(durable)
				} else {
					left = append(left, p)
				}
			}
			pending = left
			emit()
			continue
		}
		c.apply(full)
		pending = append(pending, c)
		emit()
	}
	return out
}

// materialize replaces the files of dirs with state.
func materialize(t *testing.T, dirs []string, state map[string]string) {
	t.Helper()
	for _, d := range dirs {
		os.RemoveAll(d)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for p, v := range state {
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func traced(fn func()) []fsOp {
	var ops []fsOp
	trace = func(op, path, to string) { ops = append(ops, fsOp{op, path, to}) }
	defer func() { trace = nil }()
	fn()
	return ops
}

func describe(s map[string]string) string {
	var parts []string
	for p, v := range s {
		parts = append(parts, filepath.Base(p)+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// Audit AUD-002: WritePair synced its folders only after both renames, so a
// power loss could keep the first rename and lose the second .partial file
// that RecoverPair needs to finish the pair. RecoverPair itself removed its
// two .partial files without a sync in between. Every state a power loss can
// leave, during the write or during a recovery after it, must recover to the
// old pair or to the new one.
func TestWritePairSurvivesPowerLoss(t *testing.T) {
	for _, layout := range []string{"one folder", "two folders"} {
		base := t.TempDir()
		kdir, cdir := filepath.Join(base, "a"), filepath.Join(base, "a")
		if layout == "two folders" {
			cdir = filepath.Join(base, "b")
		}
		dirs := []string{kdir, cdir}
		key, crt := filepath.Join(kdir, "k"), filepath.Join(cdir, "c")
		old := map[string]string{key: "k1", crt: "c1"}
		materialize(t, dirs, old)
		ops := traced(func() {
			if err := WritePair(key, []byte("k2"), 0o600, crt, []byte("c2"), 0o644); err != nil {
				t.Fatal(err)
			}
		})
		created := map[string]string{key + ".partial": "k2", crt + ".partial": "c2"}
		for _, s1 := range survivors(old, ops, created) {
			materialize(t, dirs, s1)
			rec := traced(func() {
				if _, err := RecoverPair(key, crt); err != nil {
					t.Fatalf("%s, recovering %s: %v", layout, describe(s1), err)
				}
			})
			for _, s2 := range survivors(s1, rec, nil) {
				materialize(t, dirs, s2)
				if _, err := RecoverPair(key, crt); err != nil {
					t.Fatalf("%s, recovering %s: %v", layout, describe(s2), err)
				}
				got := readPair(t, key, crt)
				left, _ := filepath.Glob(filepath.Join(base, "*", "*.partial"))
				if (got != "k1+c1" && got != "k2+c2") || len(left) != 0 {
					t.Fatalf("%s: a power loss left %s (then %s), recovered to %s with %v", layout, describe(s1), describe(s2), got, left)
				}
			}
		}
	}
}

// RecoverPair reports a .partial file it cannot remove instead of going on.
func TestRecoverPairReportsRemovalErrors(t *testing.T) {
	dir := t.TempDir()
	key, crt := filepath.Join(dir, "k"), filepath.Join(dir, "c")
	os.MkdirAll(filepath.Join(key+".partial", "x"), 0o700)
	if _, err := RecoverPair(key, crt); err == nil || !strings.Contains(err.Error(), "directory not empty") {
		t.Fatalf("want the removal error, got %v", err)
	}
}

// durableAfter is what survives any power loss once ops are done: the
// changes of every folder synced after them.
func durableAfter(start map[string]string, ops []fsOp) map[string]string {
	durable := maps.Clone(start)
	var pending []fsOp
	for _, o := range ops {
		if o.op != "sync" {
			pending = append(pending, o)
			continue
		}
		var left []fsOp
		for _, p := range pending {
			if filepath.Dir(p.path) == o.path && p.op == "remove" {
				delete(durable, p.path)
			} else {
				left = append(left, p)
			}
		}
		pending = left
	}
	return durable
}

// PLAN 1.2.2: when RemoveFiles returns, its removals survive a power loss,
// so the catalog can record the backup deleted; every state a cut leaves
// before that has the files or not, never more than were there.
func TestRemoveFilesIsDurableWhenItReturns(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "x", "f.backup"), filepath.Join(base, "x", "f.manifest.json")
	start := map[string]string{a: "dump", b: "manifest"}
	materialize(t, []string{filepath.Join(base, "x")}, start)
	ops := traced(func() {
		if err := RemoveFiles(a, b, filepath.Join(base, "x", "gone")); err != nil {
			t.Fatal(err)
		}
	})
	if got := durableAfter(start, ops); len(got) != 0 {
		t.Fatalf("after RemoveFiles returned, a power loss could bring back %s", describe(got))
	}
	for _, s := range survivors(start, ops, nil) {
		for p := range s {
			if p != a && p != b {
				t.Fatalf("a power loss left %s", describe(s))
			}
		}
	}
	if _, err := os.Stat(a); err == nil {
		t.Fatal("the files are still there")
	}
}

// A folder that cannot be synced is an error: the caller must not record the
// files as deleted.
func TestRemoveFilesReportsASyncFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	dir := filepath.Join(t.TempDir(), "x")
	os.MkdirAll(dir, 0o700)
	f := filepath.Join(dir, "f.backup")
	os.WriteFile(f, nil, 0o600)
	os.Chmod(dir, 0o300) // removable, not readable: the sync cannot open it
	defer os.Chmod(dir, 0o700)
	if err := RemoveFiles(f); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("want the sync error, got %v", err)
	}
	// Called again once the file is gone, it still syncs the folder, which
	// the first call could not.
	if err := RemoveFiles(f); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("a file already gone skipped the sync of its folder: %v", err)
	}
	os.Chmod(dir, 0o700)
	ops := traced(func() {
		if err := RemoveFiles(f, filepath.Join(t.TempDir(), "missing", "x")); err != nil {
			t.Fatal(err)
		}
	})
	if len(ops) != 1 || ops[0] != (fsOp{"sync", dir, ""}) {
		t.Fatalf("ops %+v", ops)
	}
}
