// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SQS-C010 belongs to the secrets package (permissions too open). Failing to
// read the master key has its own code so each code means one thing.
func TestReadMasterKeyErrorCodes(t *testing.T) {
	cfg := testConfig(t)
	if err := os.MkdirAll(filepath.Dir(masterKeyPath(cfg)), 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := ReadMasterKey(cfg)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-C100 could not read the master key (did you run sqsolve init?): ") {
		t.Fatalf("missing master key: %v", err)
	}
	if strings.Contains(err.Error(), "SQS-C010") {
		t.Fatalf("SQS-C010 is not the code of a missing master key: %v", err)
	}

	if err := os.WriteFile(masterKeyPath(cfg), []byte("QUJD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ReadMasterKey(cfg)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-C100 could not read the master key") || !strings.Contains(err.Error(), "SQS-C010 permissions too open on a secrets file") {
		t.Fatalf("master key with open permissions: %v", err)
	}

	// WriteFile keeps the mode of an existing file, so tighten it explicitly.
	if err := os.WriteFile(masterKeyPath(cfg), []byte("not base64!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(masterKeyPath(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadMasterKey(cfg); err == nil || !strings.HasPrefix(err.Error(), "SQS-C011 the master key is not valid base64: ") {
		t.Fatalf("master key that is not base64: %v", err)
	}

	if err := os.WriteFile(masterKeyPath(cfg), []byte("QUJD\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := ReadMasterKey(cfg)
	if err != nil || string(key) != "ABC" {
		t.Fatalf("valid master key: %q %v", key, err)
	}
}

// Audit AUD-003: the master key and the sqsolve_admin password are private
// (0400). Startup accepted them at 0440 while docs 08 and doctor did not;
// the PostgreSQL superuser password is the one shared with its group.
func TestPrivateSecretsRefuseGroupRead(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	if err := Init(cfg, io.Discard); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		masterKeyPath(cfg): "SQS-C100 could not read the master key (did you run sqsolve init?): SQS-C010 permissions too open on a secrets file: " +
			masterKeyPath(cfg) + " has mode 440 and may have at most 600",
		adminPassPath(cfg): "SQS-C013 sqsolve_admin password: SQS-C010 permissions too open on a secrets file: " +
			adminPassPath(cfg) + " has mode 440 and may have at most 600",
	} {
		os.Chmod(path, 0o440)
		a, err := Open(ctx, cfg, quietLog())
		if err == nil {
			a.Close()
		}
		if err == nil || err.Error() != want {
			t.Fatalf("%s at 0440: want %q, got %v", filepath.Base(path), want, err)
		}
		os.Chmod(path, 0o400)
	}
	if st, _ := os.Stat(cfg.SuperuserPasswordFile()); st.Mode().Perm() != 0o440 {
		t.Fatalf("superuser password at %o", st.Mode().Perm())
	}
	a, err := Open(ctx, cfg, quietLog())
	if err != nil {
		t.Fatalf("the shared superuser password at 0440 was refused: %v", err)
	}
	defer a.Close()
	// doctor applies the same rule.
	os.Chmod(masterKeyPath(cfg), 0o440)
	defer os.Chmod(masterKeyPath(cfg), 0o400)
	if ch := checkMap(Doctor(ctx, a))["Secret master.key"]; ch.Status != "fail" || !strings.Contains(ch.Detail, "SQS-C010") {
		t.Fatalf("doctor: %+v", ch)
	}
}
