// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/donutinit/sqsolve/internal/app"
	"github.com/donutinit/sqsolve/internal/auth"
	"github.com/donutinit/sqsolve/internal/config"
)

// testServer builds the panel on a real App with SQLite only.
func testServer(t *testing.T) *Server {
	t.Helper()
	base := t.TempDir()
	cfg := &config.Config{AppDir: filepath.Join(base, "app"), BackupDir: filepath.Join(base, "backups"), PGDir: filepath.Join(base, "pg"),
		PGDataDir: filepath.Join(base, "pg", "data"), PGSocket: filepath.Join(base, "sock"), PGPort: 1, PGListen: "127.0.0.1",
		PGMaxConnections: 50, PGMemoryMB: 256, UID: os.Getuid(), GID: os.Getgid(), PGUID: os.Getuid(), PGGID: os.Getgid(),
		LogLevel: "warn", LogFormat: "text", Workers: 1, HeavyJobs: 1, Location: time.UTC, InsecureHTTP: true, Listen: "127.0.0.1:0"}
	os.MkdirAll(filepath.Dir(cfg.SuperuserPasswordFile()), 0o700)
	if err := os.WriteFile(cfg.SuperuserPasswordFile(), []byte("x\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := app.Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	s, err := New(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Audit M2: when every Argon2id slot stays taken, the login form comes back
// with 503, Retry-After and the coded message, without a redirect.
func TestLoginBusyIsServiceUnavailable(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", strings.NewReader("username=boss&password=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.loginBusy(w, r, "/libraries", auth.ErrBusy)
	body := w.Body.String()
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "5" || w.Header().Get("Location") != "" {
		t.Fatalf("busy login: %d %v", w.Code, w.Header())
	}
	for _, want := range []string{`<div class="flash error" role="alert">SQS-A050 the server is checking too many passwords at once; try again in a few seconds</div>`,
		`name="next" value="/libraries"`, `value="boss"`, `name="csrf" value="`} {
		if !strings.Contains(body, want) {
			t.Errorf("busy login page lacks %s", want)
		}
	}
}

// Audit M4: next=/<TAB>/host passed safeNext; browsers strip the tab and
// follow //host to another origin.
func TestSafeNext(t *testing.T) {
	for _, bad := range []string{"", "/\t/evil.example/x", "/\n/evil.example", "/\r/evil.example", "/\x00/x", "/\x7f/x",
		"/ /evil.example", "/x y", "/ /evil.example", "/ /evil.example", "/%2F/evil.example", "/%2f/evil.example",
		"/%5C/evil.example", "/%09/evil.example", "/%0a/evil.example", "/\\evil.example", "\\/evil.example", "/\\/evil.example",
		"//evil.example", "///evil.example", "https://evil.example/", "http:/evil.example", "javascript:alert(1)", "evil.example",
		"./x", "?next=/", "#/x", "/%zz"} {
		if got := safeNext(bad); got != "/" {
			t.Errorf("safeNext(%q) = %q", bad, got)
		}
	}
	for _, ok := range []string{"/", "/libraries", "/libraries?view=trash", "/jobs/01ABC?x=1&y=2", "/settings?tab=users#top", "/backups/x%20y"} {
		if got := safeNext(ok); got != ok {
			t.Errorf("safeNext(%q) = %q", ok, got)
		}
	}
}

// Every password field asks for the policy minimum, not more (the forms
// asked for 12 while the server accepts 8).
func TestPasswordFieldsUsePolicyMinimum(t *testing.T) {
	input := regexp.MustCompile(`<input[^>]*type="password"[^>]*>`)
	minlen := regexp.MustCompile(`minlength="(\d+)"`)
	want := fmt.Sprint(auth.MinPasswordLength)
	n := 0
	err := fs.WalkDir(templatesFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(templatesFS, path)
		if err != nil {
			return err
		}
		for _, in := range input.FindAllString(string(b), -1) {
			m := minlen.FindStringSubmatch(in)
			if m == nil {
				continue // the login form checks no length
			}
			n++
			if m[1] != want {
				t.Errorf("%s: %s", path, in)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 7 {
		t.Fatalf("only %d password fields with minlength", n)
	}
}

// The setup form states the password policy the server applies (minimum
// length and the list of common passwords), not an older 12 characters.
func TestSetupHintStatesThePasswordPolicy(t *testing.T) {
	b, err := fs.ReadFile(templatesFS, "templates/setup.html")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`<p class="hint">At least %d characters and not a common password. A phrase of several words works well.</p>`, auth.MinPasswordLength)
	if !strings.Contains(string(b), want) {
		t.Fatalf("setup.html does not contain %s", want)
	}
}
