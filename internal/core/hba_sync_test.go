// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"os"
	"strings"
	"testing"
)

// A pg_hba.conf that cannot be written stops the sync with its code, before
// PostgreSQL is asked to validate or reload anything (this service has no
// PostgreSQL client at all).
func TestSyncHBAReportsAFileItCannotWrite(t *testing.T) {
	e := newBkEnv(t)
	e.library("hba_lib", StateActive)
	// A file sits where the configuration folder goes.
	if err := os.WriteFile(e.svc.Cfg.PGConfDir(), []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := e.svc.SyncHBA(e.ctx)
	if err == nil || !strings.HasPrefix(err.Error(), "SQS-P041 could not write pg_hba.conf: ") {
		t.Fatalf("sync with a configuration folder that cannot be written: %v", err)
	}
}
