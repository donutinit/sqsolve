// SPDX-License-Identifier: AGPL-3.0-only

package fsx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A written marker reads back with every field, and it is private.
func TestMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 25, 8, 30, 0, 0, time.UTC)
	if err := WriteMarker(dir, Marker{InstanceID: "INST", Volume: VolumeApp, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMarker(dir)
	if err != nil || m.InstanceID != "INST" || m.Volume != VolumeApp || !m.CreatedAt.Equal(at) {
		t.Fatalf("%+v %v", m, err)
	}
	if st, _ := os.Stat(filepath.Join(dir, MarkerName)); st.Mode().Perm() != 0o600 {
		t.Fatalf("marker %04o, want 0600", st.Mode().Perm())
	}
}

// A marker that is not JSON is SQS-S002 with the folder and the cause; one
// that cannot be read at all returns the read error, not "no marker".
func TestUnreadableMarker(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, MarkerName), []byte("instance=INST"), 0o600)
	_, err := ReadMarker(dir)
	want := fmt.Sprintf("SQS-S002 unreadable marker in %s: invalid character 'i' looking for beginning of value", dir)
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %s", err, want)
	}
	if err := CheckMarker(dir, "INST"); err == nil || err.Error() != want {
		t.Fatalf("check: %v", err)
	}
	other := t.TempDir()
	os.Mkdir(filepath.Join(other, MarkerName), 0o700)
	if _, err := ReadMarker(other); err == nil || errors.Is(err, ErrNoMarker) || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("a marker that is a folder: %v", err)
	}
}

// The errors of CheckVolume say what is wrong: no marker (SQS-S001), another
// installation (SQS-S003) or the volume of another path (SQS-S019).
func TestCheckVolumeMessages(t *testing.T) {
	dir := t.TempDir()
	if err := CheckVolume(dir, "A", VolumeDB); err == nil || err.Error() != "SQS-S001 the volume has no sqsolve marker" {
		t.Fatalf("no marker: %v", err)
	}
	WriteMarker(dir, Marker{InstanceID: "A", Volume: VolumeApp, CreatedAt: time.Now()})
	if err := CheckVolume(dir, "B", VolumeApp); err == nil || err.Error() != fmt.Sprintf("SQS-S003 volume %s belongs to another installation (A)", dir) {
		t.Fatalf("another installation: %v", err)
	}
	want := fmt.Sprintf("SQS-S019 %s holds the app volume of installation A, not its db volume: the host paths of the volumes are swapped. Fix the volume paths in the compose file", dir)
	if err := CheckVolume(dir, "A", VolumeDB); err == nil || err.Error() != want {
		t.Fatalf("swapped: %v", err)
	}
	if err := CheckVolume(dir, "A", VolumeApp); err != nil {
		t.Fatal(err)
	}
}

// A marker cannot be written to a folder that does not exist.
func TestWriteMarkerMissingFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if err := WriteMarker(dir, Marker{InstanceID: "A", Volume: VolumeBackups}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
}
