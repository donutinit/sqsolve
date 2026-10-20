// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestCorpus is the verification kit of docs 14 §4 and docs 22 §6: it exports
// projects and albums of a real library so they can be compared with what
// Resolve exports. It only runs with SQSOLVE_DRP_DSN set.
//
//	SQSOLVE_DRP_DSN=postgres://user:pass@host/db SQSOLVE_DRP_OUT=/tmp/out \
//	SQSOLVE_DRP_PROJECTS='folder/project,other' go test ./internal/drp -run TestCorpus -v
func TestCorpus(t *testing.T) {
	dsn := os.Getenv("SQSOLVE_DRP_DSN")
	if dsn == "" {
		t.Skip("SQSOLVE_DRP_DSN not set")
	}
	out := os.Getenv("SQSOLVE_DRP_OUT")
	if out == "" {
		out = t.TempDir()
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	t.Logf("fingerprint %s known=%v %+v", s.Fingerprint, s.KnownOK, s.Known)

	projects, err := s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, p := range strings.Split(os.Getenv("SQSOLVE_DRP_PROJECTS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			want[p] = true
		}
	}
	exported := 0
	for _, p := range projects {
		spec := p.Name
		if p.Path != "" {
			spec = p.Path + "/" + p.Name
		}
		if len(want) > 0 && !want[spec] {
			continue
		}
		f, err := os.Create(filepath.Join(out, strings.ReplaceAll(spec, "/", "__")+".drp"))
		if err != nil {
			t.Fatal(err)
		}
		info, err := s.ExportProject(ctx, p.ID, f)
		f.Close()
		if err != nil {
			t.Errorf("%s: %v", spec, err)
			continue
		}
		exported++
		t.Logf("%s: %d files, %d bytes, notes %v", spec, info.Files, info.Bytes, info.Notes)
	}
	if len(want) > 0 && exported != len(want) {
		t.Errorf("requested %d projects and exported %d", len(want), exported)
	}

	albums, err := s.Albums(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range albums {
		if a.ProjectID != "" || len(a.Stills) == 0 {
			continue
		}
		f, err := os.Create(filepath.Join(out, "album-"+SafeName(a.Name, "album")+".zip"))
		if err != nil {
			t.Fatal(err)
		}
		info, err := s.ExportAlbum(ctx, a.ViewID, f)
		f.Close()
		if err != nil {
			t.Errorf("album %s: %v", a.Name, err)
			continue
		}
		t.Logf("album %s: %d stills, %d bytes", a.Name, info.Files, info.Bytes)
	}
}
