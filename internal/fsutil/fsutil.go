// Package fsutil provides the only write path for the managed files: atomic
// replacement through a 0600 temp file, a one-generation .bak copy, symlink
// following and a process lock.
package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileMode is the permission every managed file, temp file and backup gets.
const FileMode fs.FileMode = 0o600

// BackupSuffix is appended to the real file path to form the backup path.
const BackupSuffix = ".bak"

const maxSymlinks = 40

// Resolve follows symlinks in path until it reaches a regular file or a path
// that does not exist yet, so a dangling dotfiles link is written through.
func Resolve(path string) (string, error) {
	p := path
	for range maxSymlinks {
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return p, nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			return p, nil
		}
		dst, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dst) {
			dst = filepath.Join(filepath.Dir(p), dst)
		}
		p = dst
	}
	return "", fmt.Errorf("fsutil: too many levels of symbolic links: %s", path)
}

// Pending is a fully written and synced temp file waiting to replace its target.
type Pending struct {
	target string
	tmp    string
	done   bool
}

// Prepare writes data to a 0600 temp file next to the real file behind path
// and syncs it. The target is untouched until Commit.
func Prepare(path string, data []byte) (*Pending, error) {
	target, err := Resolve(path)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return nil, err
	}
	p := &Pending{target: target, tmp: f.Name()}
	err = f.Chmod(FileMode)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = p.Abort()
		return nil, fmt.Errorf("fsutil: write temp file for %s: %w", target, err)
	}
	return p, nil
}

// Target returns the real file the temp file will replace.
func (p *Pending) Target() string { return p.target }

// Commit renames the temp file over the target and syncs the directory. On
// failure the temp file is removed and the target is left as it was.
func (p *Pending) Commit() error {
	if p.done {
		return errors.New("fsutil: pending write already finished")
	}
	p.done = true
	if err := os.Rename(p.tmp, p.target); err != nil {
		_ = os.Remove(p.tmp)
		return fmt.Errorf("fsutil: replace %s: %w", p.target, err)
	}
	syncDir(filepath.Dir(p.target))
	return nil
}

// Abort removes the temp file. It is safe to call after Commit.
func (p *Pending) Abort() error {
	if p.done {
		return nil
	}
	p.done = true
	if err := os.Remove(p.tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// WriteFile atomically replaces the real file behind path with data after
// copying the current content to <file>.bak. The result is always 0600.
func WriteFile(path string, data []byte) error {
	p, err := Prepare(path, data)
	if err != nil {
		return err
	}
	if err := Backup(p.Target()); err != nil {
		_ = p.Abort()
		return err
	}
	return p.Commit()
}

// BackupPath returns the backup location for the real file behind path.
func BackupPath(path string) (string, error) {
	target, err := Resolve(path)
	if err != nil {
		return "", err
	}
	return target + BackupSuffix, nil
}

// Backup copies the real file behind path to <file>.bak (0600), replacing the
// previous backup. A missing file is not an error and leaves no backup.
func Backup(path string) error {
	target, err := Resolve(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fsutil: read %s for backup: %w", target, err)
	}
	p, err := Prepare(target+BackupSuffix, data)
	if err != nil {
		return err
	}
	return p.Commit()
}

// Restore atomically puts <file>.bak back in place of the real file behind
// path. The backup itself is kept.
func Restore(path string) error {
	target, err := Resolve(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(target + BackupSuffix)
	if err != nil {
		return fmt.Errorf("fsutil: read backup of %s: %w", target, err)
	}
	p, err := Prepare(target, data)
	if err != nil {
		return err
	}
	return p.Commit()
}

// FixPerm sets the permission of the file behind path to FileMode. It
// returns the previous permission and whether it had to change, so callers
// can warn. A missing file is reported as unchanged.
func FixPerm(path string) (old fs.FileMode, changed bool, err error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	old = fi.Mode().Perm()
	if old == FileMode {
		return old, false, nil
	}
	if err := os.Chmod(path, FileMode); err != nil {
		return old, false, err
	}
	return old, true, nil
}
