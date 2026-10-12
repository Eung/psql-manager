//go:build linux || darwin

package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLocked means another psql-manager process holds the lock.
var ErrLocked = errors.New("fsutil: another psql-manager process is running")

// Lock is an exclusive flock held on a lock file.
type Lock struct{ f *os.File }

// DefaultLockPath returns ~/.psql-manager.lock.
func DefaultLockPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("fsutil: locate home directory: %w", err)
	}
	return filepath.Join(home, ".psql-manager.lock"), nil
}

// Acquire takes an exclusive lock on path without waiting. It returns
// ErrLocked when the lock is held elsewhere. The lock file is never removed.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, FileMode)
	if err != nil {
		return nil, fmt.Errorf("fsutil: open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("fsutil: lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. It is safe to call more than once.
func (l *Lock) Release() error {
	if l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}
