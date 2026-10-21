// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
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
