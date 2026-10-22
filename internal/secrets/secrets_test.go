// SPDX-License-Identifier: AGPL-3.0-only

package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStringNeverPrints(t *testing.T) {
	s := New("hunter2-supersecret")
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&buf, nil))
	l.Info("x", "pw", s)
	j, _ := json.Marshal(map[string]any{"pw": s})
	out := buf.String() + fmt.Sprintf("%v %s %+v %#v", s, s, s, s) + string(j)
	if strings.Contains(out, "hunter2") {
		t.Fatalf("secret leaked: %s", out)
	}
	if s.Reveal() != "hunter2-supersecret" {
		t.Fatal("reveal")
	}
}

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		p := GeneratePassword(24).Reveal()
		if len(p) != 24 || strings.ContainsAny(p, "0O1lI") {
			t.Fatalf("bad password %q", p)
		}
		if seen[p] {
			t.Fatal("duplicate")
		}
		seen[p] = true
	}
}

func TestBoxRoundTripAndAAD(t *testing.T) {
	b, err := NewBox(RandomBytes(32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.Seal(New("abc"), "library_credentials.password_enc.01X")
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Open(sealed, "library_credentials.password_enc.01X")
	if err != nil || got.Reveal() != "abc" {
		t.Fatalf("open: %v %q", err, got.Reveal())
	}
	if _, err := b.Open(sealed, "library_credentials.password_enc.01Y"); !errors.Is(err, ErrDecrypt) {
		t.Fatal("aad not enforced")
	}
	other, _ := NewBox(RandomBytes(32))
	if _, err := other.Open(sealed, "library_credentials.password_enc.01X"); !errors.Is(err, ErrDecrypt) {
		t.Fatal("other key opened it")
	}
	// Change one character inside the authentication tag (the last character may carry only
	// padding bits that the decoder ignores), always to a different value.
	i := len(sealed) - 10
	c := byte('A')
	if sealed[i] == 'A' {
		c = 'B'
	}
	tampered := sealed[:i] + string(c) + sealed[i+1:]
	if _, err := b.Open(tampered, "library_credentials.password_enc.01X"); err == nil {
		t.Fatal("tampered value opened")
	}
}

// Audit AUD-003: one reader accepted group read for every secret, so a
// master key left at 0440 started although docs 08 and doctor require 0400.
func TestReadFilePermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s")
	os.WriteFile(p, []byte("val\n"), 0o400)
	for _, mode := range []os.FileMode{0o644, 0o440, 0o404, 0o410, 0o420} {
		os.Chmod(p, mode)
		_, err := ReadFile(p)
		want := fmt.Sprintf("SQS-C010 permissions too open on a secrets file: %s has mode %o and may have at most 600", p, mode)
		if !errors.Is(err, ErrPermissions) || err.Error() != want {
			t.Fatalf("mode %o: want %q, got %v", mode, want, err)
		}
	}
	for _, mode := range []os.FileMode{0o400, 0o600} {
		os.Chmod(p, mode)
		if s, err := ReadFile(p); err != nil || s.Reveal() != "val" {
			t.Fatalf("mode %o: %v %q", mode, err, s.Reveal())
		}
	}
}

// The PostgreSQL superuser password is shared with the postgres group.
func TestReadGroupFilePermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s")
	os.WriteFile(p, []byte("val\n"), 0o440)
	for _, mode := range []os.FileMode{0o444, 0o460, 0o441, 0o640} {
		os.Chmod(p, mode)
		_, err := ReadGroupFile(p)
		want := fmt.Sprintf("SQS-C010 permissions too open on a secrets file: %s has mode %o and may have at most 640", p, mode)
		if mode == 0o640 {
			if err != nil {
				t.Fatalf("mode 640: %v", err)
			}
			continue
		}
		if !errors.Is(err, ErrPermissions) || err.Error() != want {
			t.Fatalf("mode %o: want %q, got %v", mode, want, err)
		}
	}
	os.Chmod(p, 0o440)
	if s, err := ReadGroupFile(p); err != nil || s.Reveal() != "val" {
		t.Fatalf("%v %q", err, s.Reveal())
	}
}
