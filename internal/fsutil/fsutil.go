// Package fsutil은 관리 대상 파일을 쓰는 유일한 경로다. 0600 임시 파일을 거친
// 원자적 교체, 한 세대 .bak 백업, 심볼릭 링크 따라가기, 프로세스 잠금을 제공한다.
package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileMode는 관리 파일, 임시 파일, 백업 파일에 모두 적용하는 권한이다.
const FileMode fs.FileMode = 0o600

// BackupSuffix는 실제 파일 경로 뒤에 붙여 백업 경로를 만든다.
const BackupSuffix = ".bak"

const maxSymlinks = 40

// Resolve는 일반 파일이나 아직 없는 경로에 닿을 때까지 심볼릭 링크를 따라간다.
// 그래서 대상이 아직 없는 dotfiles 링크도 링크를 통해 실제 위치에 쓴다.
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

// Pending은 내용을 다 쓰고 fsync까지 마친 채 대상 파일 교체를 기다리는 임시 파일이다.
type Pending struct {
	target string
	tmp    string
	done   bool
}

// Prepare는 path가 가리키는 실제 파일 옆에 0600 임시 파일을 만들어 data를 쓰고
// fsync한다. Commit 전까지 대상 파일은 건드리지 않는다.
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

// Target은 임시 파일이 교체할 실제 파일 경로를 돌려준다.
func (p *Pending) Target() string { return p.target }

// Commit은 임시 파일을 대상 위치로 rename하고 디렉터리를 fsync한다. 실패하면
// 임시 파일을 지우고 대상 파일은 원래대로 둔다.
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

// Abort는 임시 파일을 지운다. Commit 뒤에 호출해도 안전하다.
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

// WriteFile은 현재 내용을 <파일>.bak으로 복사한 뒤 path가 가리키는 실제 파일을
// data로 원자적으로 교체한다. 결과 파일 권한은 항상 0600이다.
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

// BackupPath는 path가 가리키는 실제 파일의 백업 경로를 돌려준다.
func BackupPath(path string) (string, error) {
	target, err := Resolve(path)
	if err != nil {
		return "", err
	}
	return target + BackupSuffix, nil
}

// Backup은 path가 가리키는 실제 파일을 <파일>.bak(0600)으로 복사해 이전 백업을
// 대체한다. 원본이 없으면 오류 없이 백업도 만들지 않는다.
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

// Restore는 <파일>.bak을 path가 가리키는 실제 파일 자리에 원자적으로 되돌린다.
// 백업 파일은 그대로 남긴다.
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

// FixPerm은 path가 가리키는 파일의 권한을 FileMode로 맞춘다. 호출하는 쪽에서
// 경고할 수 있도록 이전 권한과 변경 여부를 돌려준다. 파일이 없으면 변경 없음으로 본다.
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
