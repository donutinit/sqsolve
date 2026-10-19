// SPDX-License-Identifier: AGPL-3.0-or-later

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
	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := b.Open(tampered, "library_credentials.password_enc.01X"); err == nil {
		t.Fatal("tampered value opened")
	}
}

func TestReadFilePermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s")
	os.WriteFile(p, []byte("val\n"), 0o644)
	if _, err := ReadFile(p); !errors.Is(err, ErrPermissions) {
		t.Fatalf("expected permission error, got %v", err)
	}
	os.Chmod(p, 0o440)
	s, err := ReadFile(p)
	if err != nil || s.Reveal() != "val" {
		t.Fatalf("%v %q", err, s.Reveal())
	}
}
