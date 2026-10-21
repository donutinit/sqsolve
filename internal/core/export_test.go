// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"time"
)

// Test-only access for the integration tests of package core_test.

// VerifyL1 runs the level 1 verification of a backup.
func (s *Service) VerifyL1(ctx context.Context, id string) error { return s.verifyL1(ctx, id) }

// SetMarkAndSnapshotHook runs f between reading the activity mark of a
// backup and exporting its snapshot. The returned function removes it.
func SetMarkAndSnapshotHook(f func()) (restore func()) {
	hookMarkAndSnapshot.Store(&f)
	return func() { hookMarkAndSnapshot.Store(nil) }
}

// SetImportStagedHook runs f when an imported file sits as .partial in the
// library folder; an error stops the import there, as a crash would.
func SetImportStagedHook(f func(partial string) error) (restore func()) {
	hookImportStaged.Store(&f)
	return func() { hookImportStaged.Store(nil) }
}

// SetExportEnqueuedHook runs f right after the job of an export from a
// backup is enqueued.
func SetExportEnqueuedHook(f func(jobID string)) (restore func()) {
	hookExportEnqueued.Store(&f)
	return func() { hookExportEnqueued.Store(nil) }
}

// SetAdoptionCheckHook runs f when reconciliation has opened the database of
// a missing library for its adoption check. The returned function removes it.
func SetAdoptionCheckHook(f func(db string)) (restore func()) {
	hookAdoptionCheck.Store(&f)
	return func() { hookAdoptionCheck.Store(nil) }
}

// SetDBVolumeFree makes the restore space check see free bytes on the
// database volume.
func SetDBVolumeFree(free uint64) (restore func()) {
	f := func() uint64 { return free }
	hookDBVolumeFree.Store(&f)
	return func() { hookDBVolumeFree.Store(nil) }
}

// SetImportSettle changes how long a file in _import must stay unchanged.
func SetImportSettle(d time.Duration) (restore func()) {
	old := importSettle.Swap(int64(d))
	return func() { importSettle.Store(old) }
}
