//go:build linux || darwin

package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLocked는 다른 psql-manager 프로세스가 잠금을 잡고 있다는 뜻이다.
var ErrLocked = errors.New("fsutil: another psql-manager process is running")

// Lock은 잠금 파일에 건 배타적 flock이다.
type Lock struct{ f *os.File }

// DefaultLockPath는 ~/.psql-manager.lock 경로를 돌려준다.
func DefaultLockPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("fsutil: locate home directory: %w", err)
	}
	return filepath.Join(home, ".psql-manager.lock"), nil
}

// Acquire는 path에 대기 없이 배타적 잠금을 건다. 다른 곳에서 잠금을 잡고
// 있으면 ErrLocked를 돌려준다. 잠금 파일은 지우지 않는다.
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

// Release는 잠금을 푼다. 여러 번 호출해도 안전하다.
func (l *Lock) Release() error {
	if l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}
