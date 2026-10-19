// SPDX-License-Identifier: AGPL-3.0-only

// Package fsx has crash-safe file helpers: atomic writes, directory sync,
// volume markers and free space.
package fsx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// failpoint lets tests simulate a crash between steps.
var failpoint func(step string) error

func fail(step string) error {
	if failpoint != nil {
		return failpoint(step)
	}
	return nil
}

// WriteFileAtomic writes data to path through a .partial file, fsync and
// rename, then syncs the directory.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := fail("write"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := fail("rename"); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return SyncDir(filepath.Dir(path))
}

// SealFile fsyncs a file written by another process and renames it from
// src to dst, then syncs the directory.
func SealFile(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(dst))
}

// SyncDir fsyncs a directory so a rename survives a power cut.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}

// MarkerName is the file that identifies a volume.
const MarkerName = ".sqsolve-volume"

// Marker is the content of a volume marker.
type Marker struct {
	InstanceID string    `json:"instance_id"`
	Volume     string    `json:"volume"`
	CreatedAt  time.Time `json:"created_at"`
}

// ErrNoMarker means the directory has no marker.
var ErrNoMarker = errors.New("SQS-S001 el volumen no tiene marcador de sqsolve")

// ReadMarker reads the marker of dir.
func ReadMarker(dir string) (*Marker, error) {
	b, err := os.ReadFile(filepath.Join(dir, MarkerName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoMarker
	}
	if err != nil {
		return nil, err
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("SQS-S002 marcador ilegible en %s: %w", dir, err)
	}
	return &m, nil
}

// WriteMarker writes a marker.
func WriteMarker(dir string, m Marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(dir, MarkerName), append(b, '\n'), 0o600)
}

// CheckMarker verifies that dir carries the marker of instanceID.
func CheckMarker(dir, instanceID string) error {
	m, err := ReadMarker(dir)
	if err != nil {
		return err
	}
	if m.InstanceID != instanceID {
		return fmt.Errorf("SQS-S003 el volumen %s pertenece a otra instalación (%s)", dir, m.InstanceID)
	}
	return nil
}

// Space is the capacity of a filesystem.
type Space struct {
	Total uint64
	Free  uint64
}

// UsedPercent returns the used share in percent.
func (s Space) UsedPercent() float64 {
	if s.Total == 0 {
		return 0
	}
	return 100 * float64(s.Total-s.Free) / float64(s.Total)
}

// Statfs returns the capacity of the filesystem that holds dir.
func Statfs(dir string) (Space, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return Space{}, err
	}
	bs := uint64(st.Bsize)
	return Space{Total: st.Blocks * bs, Free: st.Bavail * bs}, nil
}

// IsEmptyDir reports whether dir has no entries.
func IsEmptyDir(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if len(names) == 0 && (err == nil || errors.Is(err, io.EOF)) {
		return true, nil
	}
	return false, nil
}

// Within reports whether path is inside dir after resolving symlinks.
func Within(dir, path string) (bool, error) {
	rd, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false, err
	}
	rp, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(rd, rp)
	if err != nil {
		return false, nil
	}
	return rel != ".." && !filepath.IsAbs(rel) && (len(rel) < 3 || rel[:3] != "../"), nil
}
