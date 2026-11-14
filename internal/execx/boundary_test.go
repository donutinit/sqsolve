// SPDX-License-Identifier: AGPL-3.0-only

package execx

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testpgImport is the test helper package: it may start programs directly
// because only test files import it.
const testpgImport = "github.com/donutinit/sqsolve/internal/testpg"

// processStarters lists, per package, the functions that start or replace a
// process without os/exec.
var processStarters = map[string][]string{
	"os":                    {"StartProcess"},
	"syscall":               {"ForkExec", "StartProcess", "Exec"},
	"golang.org/x/sys/unix": {"Exec"},
}

// boundaryViolations parses one Go file that is not a test and returns what
// in it breaks the rule that the code that ships starts programs only
// through this package. rel is the slash path from the repository root; src
// is nil to read filename, or the source text.
func boundaryViolations(rel, filename string, src any) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	helper := strings.HasPrefix(rel, "internal/testpg/")
	var out []string
	local := map[string]string{} // name in this file -> import path
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			return nil, err
		}
		if p == "os/exec" && !helper {
			out = append(out, rel+" imports os/exec: the code that ships starts programs only through internal/execx")
		}
		if p == testpgImport {
			out = append(out, rel+" imports internal/testpg, which is only for tests")
		}
		if _, ok := processStarters[p]; !ok {
			continue
		}
		name := path.Base(p)
		if im.Name != nil {
			name = im.Name.Name
		}
		if name == "." && !helper {
			out = append(out, rel+" dot-imports "+p+": its process functions could not be told apart")
		}
		local[name] = p
	}
	if helper {
		return out, nil
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		for _, fn := range processStarters[local[x.Name]] {
			if sel.Sel.Name == fn {
				out = append(out, rel+" uses "+local[x.Name]+"."+fn+": the code that ships starts programs only through internal/execx")
			}
		}
		return true
	})
	return out, nil
}

// The code that ships starts programs only through this package, and the
// test helper package that may start them directly is imported by tests
// only (docs/16-testing.md section 4).
func TestOnlyExecxStartsProgramsInShippedCode(t *testing.T) {
	root := filepath.Join("..", "..")
	checked := 0
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			r, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(r)
			if strings.HasPrefix(rel, "internal/execx/") {
				return nil
			}
			found, err := boundaryViolations(rel, p, nil)
			if err != nil {
				return err
			}
			checked++
			for _, v := range found {
				t.Error(v)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked < 50 {
		t.Fatalf("only %d files were checked: the walk did not see the source tree", checked)
	}
}

// The check itself: every kind of violation is reported with its message,
// and what is allowed is not.
func TestBoundaryViolations(t *testing.T) {
	const ships = ": the code that ships starts programs only through internal/execx"
	for _, tc := range []struct {
		name, rel, src string
		want           []string
	}{
		{"os/exec", "internal/core/x.go", "package core\nimport \"os/exec\"\nvar _ = exec.Command\n",
			[]string{"internal/core/x.go imports os/exec" + ships}},
		{"os/exec renamed", "cmd/sqsolve/x.go", "package main\nimport run \"os/exec\"\nvar _ = run.Command\n",
			[]string{"cmd/sqsolve/x.go imports os/exec" + ships}},
		{"os.StartProcess", "internal/app/x.go", "package app\nimport \"os\"\nvar _ = os.StartProcess\n",
			[]string{"internal/app/x.go uses os.StartProcess" + ships}},
		{"syscall.ForkExec", "internal/app/x.go", "package app\nimport \"syscall\"\nvar _ = syscall.ForkExec\n",
			[]string{"internal/app/x.go uses syscall.ForkExec" + ships}},
		{"syscall.StartProcess", "internal/app/x.go", "package app\nimport \"syscall\"\nvar _ = syscall.StartProcess\n",
			[]string{"internal/app/x.go uses syscall.StartProcess" + ships}},
		{"syscall.Exec renamed", "internal/app/x.go", "package app\nimport sc \"syscall\"\nvar _ = sc.Exec\n",
			[]string{"internal/app/x.go uses syscall.Exec" + ships}},
		{"unix.Exec", "internal/app/x.go", "package app\nimport \"golang.org/x/sys/unix\"\nvar _ = unix.Exec\n",
			[]string{"internal/app/x.go uses golang.org/x/sys/unix.Exec" + ships}},
		{"dot import", "internal/app/x.go", "package app\nimport . \"syscall\"\nvar _ = ForkExec\n",
			[]string{"internal/app/x.go dot-imports syscall: its process functions could not be told apart"}},
		{"testpg in shipped code", "internal/app/x.go", "package app\nimport \"" + testpgImport + "\"\nvar _ = testpg.BinDir\n",
			[]string{"internal/app/x.go imports internal/testpg, which is only for tests"}},
		{"the helper may use os/exec", "internal/testpg/x.go", "package testpg\nimport (\n\"os/exec\"\n\"syscall\"\n)\nvar _ = exec.Command\nvar _ = syscall.ForkExec\n", nil},
		{"signals and pids are not starts", "internal/app/x.go", "package app\nimport (\n\"os\"\n\"syscall\"\n)\nvar _ = os.Getpid\nvar _ = syscall.Kill\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := boundaryViolations(tc.rel, tc.rel, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := boundaryViolations("internal/app/x.go", "internal/app/x.go", "package"); err == nil {
		t.Fatal("a file that does not parse must be an error, not a pass")
	}
}
