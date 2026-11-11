// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"errors"
	"os"
	"testing"
)

// docs 11 §5.1: while the backups volume carries its marker, a manifest that
// is not valid JSON, or not a valid version 1 manifest (SQS-B042), is a
// verdict on the backup. The second one used to be a check that "could not
// run": the backup stayed verified, and counted as the latest verified one,
// while no restore could read it.
func TestVerifyL1InvalidManifestIsAVerdict(t *testing.T) {
	e := newBkEnv(t)
	l := e.library("client_x", StateActive)
	for _, c := range []struct{ name, manifest, want string }{
		{"not JSON", "{nope", "SQS-B020 verification failed: the manifest is unreadable"},
		{"not a version 1 manifest", `{"manifest_version": 2}`,
			"SQS-B020 verification failed: SQS-B042 the manifest is not a valid version 1 manifest: unsupported manifest_version 2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := e.backup(l, bkOpt{})
			if err := os.WriteFile(manifestPath(mustAbs(e.svc, b)), []byte(c.manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			err := e.svc.verifyL1(e.ctx, b.ID)
			var vf *verifyFailedError
			if !errors.As(err, &vf) || err.Error() != c.want {
				t.Fatalf("got %v, want %s", err, c.want)
			}
			got, err := e.svc.Backup(e.ctx, b.ID)
			if err != nil || got.VerifyStatus != VerifyFailed || got.Usable() {
				t.Fatalf("backup %+v %v, want it marked failed", got, err)
			}
			if latest, err := e.svc.LatestVerified(e.ctx, l.ID); err != nil || latest != nil && latest.ID == b.ID {
				t.Fatalf("latest verified %+v %v, want another backup or none", latest, err)
			}
			if evs := e.events(EventVerifySkipped); len(evs) != 0 {
				t.Fatalf("a verdict was reported as a verification that could not run: %+v", evs)
			}
		})
	}
}
