// SPDX-License-Identifier: AGPL-3.0-only

// Package fsx has crash-safe file helpers: atomic writes, directory sync,
// volume markers and free space.
package fsx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	if err := writePartial(tmp, data, perm); err != nil {
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

// writePartial writes and fsyncs tmp with the given mode. The mode is set
// before the fsync so it is as durable as the content.
func writePartial(tmp string, data []byte, perm os.FileMode) error {
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
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(tmp)
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
	note("create", tmp, "")
	return nil
}

// WritePair writes two files that are only valid together, such as a key
// and its certificate. Both go to .partial files first and are renamed
// first, then second; a crash in between leaves only second's .partial file,
// which RecoverPair renames into place.
//
// Each step is made durable (a sync of its folder) before the next one, so
// that after a power loss second's .partial file without first's always
// means first is already in place: a folder that was not synced may keep any
// subset of its recent changes (audit AUD-002, powerloss_test.go).
func WritePair(first string, firstData []byte, firstPerm os.FileMode, second string, secondData []byte, secondPerm os.FileMode) error {
	fp, sp := first+".partial", second+".partial"
	// An earlier write of this pair that stopped between its renames is
	// finished first: this one reuses its .partial names.
	if _, err := RecoverPair(first, second); err != nil {
		return err
	}
	if err := writePartial(fp, firstData, firstPerm); err != nil {
		return err
	}
	if err := SyncDir(filepath.Dir(fp)); err != nil {
		discard(fp, sp)
		return err
	}
	if err := writePartial(sp, secondData, secondPerm); err != nil {
		discard(fp, sp)
		return err
	}
	if err := SyncDir(filepath.Dir(sp)); err != nil {
		discard(fp, sp)
		return err
	}
	if err := fail("pair-first"); err != nil {
		return err
	}
	if err := rename(fp, first); err != nil {
		discard(fp, sp)
		return err
	}
	// From here RecoverPair completes the pair after a crash.
	if err := SyncDir(filepath.Dir(first)); err != nil {
		return err
	}
	if err := fail("pair-second"); err != nil {
		return err
	}
	if err := rename(sp, second); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(second))
}

// discard removes the .partial files of a pair that was not renamed, second's
// first and durably, so a power loss never leaves it alone.
func discard(fp, sp string) error {
	if err := remove(sp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := SyncDir(filepath.Dir(sp)); err != nil {
		return err
	}
	if err := remove(fp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return SyncDir(filepath.Dir(fp))
}

// RecoverPair finishes a WritePair that stopped between its two renames and
// discards one that stopped before them, so first and second always belong
// together. It reports whether it changed anything.
func RecoverPair(first, second string) (bool, error) {
	fp, sp := first+".partial", second+".partial"
	_, ferr := os.Lstat(fp)
	_, serr := os.Lstat(sp)
	for _, err := range []error{ferr, serr} {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	switch {
	case ferr == nil:
		// Stopped before the renames: the old pair is intact.
		return true, discard(fp, sp)
	case serr == nil:
		// first is already the new one: complete the pair.
		if err := rename(sp, second); err != nil {
			return false, err
		}
		return true, SyncDir(filepath.Dir(second))
	}
	return false, nil
}

// RemovePartials deletes the regular *.partial files left in dir by an
// interrupted write. A missing dir is not an error.
func RemovePartials(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.Type().IsRegular() || filepath.Ext(e.Name()) != ".partial" {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return n, err
		}
		n++
	}
	if n > 0 {
		return n, SyncDir(dir)
	}
	return 0, nil
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
	note("sync", dir, "")
	return nil
}

// MarkerName is the file that identifies a volume.
const MarkerName = ".sqsolve-volume"

// Volume names in the markers (ADR 0018, ADR 0023).
const (
	VolumeDB      = "db"
	VolumeApp     = "app"
	VolumeBackups = "backups"
)

// Marker is the content of a volume marker.
type Marker struct {
	InstanceID string    `json:"instance_id"`
	Volume     string    `json:"volume"`
	CreatedAt  time.Time `json:"created_at"`
}

// MsgUnreadableMarker is the text of a marker that cannot be read or parsed
// (directory, cause).
const MsgUnreadableMarker = "SQS-S002 unreadable marker in %s: %w"

// ErrNoMarker means the directory has no marker.
var ErrNoMarker = errors.New("SQS-S001 the volume has no sqsolve marker")

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
		return nil, fmt.Errorf(MsgUnreadableMarker, dir, err)
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

// CheckMarker verifies that dir is the backups volume of instanceID, the
// check before every write of a backup.
func CheckMarker(dir, instanceID string) error {
	return CheckVolume(dir, instanceID, VolumeBackups)
}

// CheckVolume verifies that dir carries the marker of the given volume of
// instanceID. A marker of this installation for another volume means the
// host paths of the volumes are swapped.
func CheckVolume(dir, instanceID, volume string) error {
	m, err := ReadMarker(dir)
	if err != nil {
		return err
	}
	if m.InstanceID != instanceID {
		return fmt.Errorf("SQS-S003 volume %s belongs to another installation (%s)", dir, m.InstanceID)
	}
	if m.Volume != volume {
		return SwappedError(dir, volume, m)
	}
	return nil
}

// SwappedError describes a marker of the installation found on the path of
// another of its volumes.
func SwappedError(dir, want string, m *Marker) error {
	return fmt.Errorf("SQS-S019 %s holds the %s volume of installation %s, not its %s volume: the host paths of the volumes are swapped. "+
		"Fix the volume paths in the compose file", dir, m.Volume, m.InstanceID, want)
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

// Confined reports whether path lies strictly below dir once every symlink
// of its existing part is resolved; the part that does not exist yet cannot
// hold a symlink. A dangling symlink, or one that loops, is not confined.
func Confined(dir, path string) (bool, error) {
	rd, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false, err
	}
	p, rest := filepath.Clean(path), ""
	for {
		if _, err := os.Lstat(p); err == nil {
			rp, err := filepath.EvalSymlinks(p)
			if err != nil {
				return false, nil
			}
			rel, err := filepath.Rel(rd, filepath.Join(rp, rest))
			if err != nil {
				return false, nil
			}
			return rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, "../"), nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false, nil
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// trace, when set, sees each change made to a folder and each folder sync,
// in order (tests model what a power loss keeps).
var trace func(op, path, to string)

func note(op, path, to string) {
	if trace != nil {
		trace(op, path, to)
	}
}

func rename(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	note("rename", from, to)
	return nil
}

func remove(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	note("remove", path, "")
	return nil
}
