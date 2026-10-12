package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func perm(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteFileCreatesWith0600(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pgpass")
	if err := WriteFile(p, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); got != "a" {
		t.Errorf("content = %q", got)
	}
	if m := perm(t, p); m != 0o600 {
		t.Errorf("perm = %o, want 600", m)
	}
	// 원본이 없었으면 백업도 없다.
	if names := dirNames(t, dir); !slices.Equal(names, []string{"pgpass"}) {
		t.Errorf("dir = %v", names)
	}
}

func TestWriteFileReplacesLoosePermAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "svc.conf")
	if err := os.WriteFile(p, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("v3")); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); got != "v3" {
		t.Errorf("content = %q", got)
	}
	// 직전 한 세대만 남는다.
	if got := readFile(t, p+".bak"); got != "v2" {
		t.Errorf("backup = %q, want v2", got)
	}
	if m := perm(t, p); m != 0o600 {
		t.Errorf("perm = %o", m)
	}
	if m := perm(t, p+".bak"); m != 0o600 {
		t.Errorf("backup perm = %o", m)
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{"svc.conf", "svc.conf.bak"}) {
		t.Errorf("temp files left behind: %v", names)
	}
}

func TestTempFileRemovedOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "is-a-dir")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	// 백업 단계(디렉터리 읽기)에서 실패한다.
	if err := WriteFile(target, []byte("x")); err == nil {
		t.Fatal("WriteFile over a directory succeeded")
	}
	// rename 단계에서 실패한다.
	p, err := Prepare(target, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Commit(); err == nil {
		t.Fatal("Commit over a directory succeeded")
	}
	if err := p.Commit(); err == nil {
		t.Error("second Commit succeeded")
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{"is-a-dir"}) {
		t.Errorf("temp files left behind: %v", names)
	}
	// 디렉터리가 없으면 임시 파일도 못 만든다.
	if _, err := Prepare(filepath.Join(dir, "missing", "f"), nil); err == nil {
		t.Error("Prepare in missing directory succeeded")
	}
}

func TestPrepareAbortLeavesTargetUntouched(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("old-a"), 0o600); err != nil {
		t.Fatal(err)
	}
	pa, err := Prepare(a, []byte("new-a"))
	if err != nil {
		t.Fatal(err)
	}
	pb, err := Prepare(b, []byte("new-b"))
	if err != nil {
		t.Fatal(err)
	}
	if pa.Target() != a {
		t.Errorf("Target() = %q", pa.Target())
	}
	if got := readFile(t, a); got != "old-a" {
		t.Errorf("Prepare touched the target: %q", got)
	}
	if err := pa.Abort(); err != nil {
		t.Fatal(err)
	}
	if err := pa.Abort(); err != nil {
		t.Errorf("second Abort: %v", err)
	}
	if err := pb.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := pb.Abort(); err != nil {
		t.Errorf("Abort after Commit: %v", err)
	}
	if got := readFile(t, a); got != "old-a" {
		t.Errorf("aborted write changed target: %q", got)
	}
	if got := readFile(t, b); got != "new-b" {
		t.Errorf("committed content = %q", got)
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{"a", "b"}) {
		t.Errorf("temp files left behind: %v", names)
	}
}

func TestWriteThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "pgpass")
	if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".pgpass")
	if err := os.Symlink("dotfiles/pgpass", link); err != nil { // 상대 경로 링크
		t.Fatal(err)
	}
	if err := WriteFile(link, []byte("new")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		t.Error("symlink was replaced by a regular file")
	}
	if got := readFile(t, real); got != "new" {
		t.Errorf("real file = %q", got)
	}
	if got := readFile(t, real+".bak"); got != "old" {
		t.Errorf("backup next to real file = %q", got)
	}
	if bp, err := BackupPath(link); err != nil || bp != real+".bak" {
		t.Errorf("BackupPath = %q, %v", bp, err)
	}
	if err := Restore(link); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, real); got != "old" {
		t.Errorf("after Restore = %q", got)
	}
}

func TestWriteThroughDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(link, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, real); got != "x" {
		t.Errorf("target = %q", got)
	}
	if m := perm(t, real); m != 0o600 {
		t.Errorf("perm = %o", m)
	}
}

func TestSymlinkLoop(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(a, nil); err == nil {
		t.Error("WriteFile through a symlink loop succeeded")
	}
	for _, f := range []func(string) error{Backup, Restore} {
		if err := f(a); err == nil {
			t.Error("symlink loop accepted")
		}
	}
	if _, err := BackupPath(a); err == nil {
		t.Error("BackupPath accepted a symlink loop")
	}
}

func TestRestoreWithoutBackup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := Restore(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Restore without backup: %v", err)
	}
}

func TestFixPerm(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pgpass")
	if _, changed, err := FixPerm(p); err != nil || changed {
		t.Errorf("missing file: changed=%v err=%v", changed, err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old, changed, err := FixPerm(p)
	if err != nil || !changed || old != 0o644 {
		t.Errorf("FixPerm = %o, %v, %v", old, changed, err)
	}
	if m := perm(t, p); m != 0o600 {
		t.Errorf("perm = %o", m)
	}
	if _, changed, _ := FixPerm(p); changed {
		t.Error("FixPerm changed an already correct file")
	}
}

func TestLockConflict(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".psql-manager.lock")
	l1, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(p); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire: err = %v, want ErrLocked", err)
	}
	if err := l1.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l1.Release(); err != nil {
		t.Errorf("second Release: %v", err)
	}
	l2, err := Acquire(p)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	defer l2.Release()
	if m := perm(t, p); m != 0o600 {
		t.Errorf("lock file perm = %o", m)
	}
	if _, err := Acquire(filepath.Join(t.TempDir(), "missing", "lock")); err == nil {
		t.Error("Acquire in missing directory succeeded")
	}
}

func TestDefaultLockPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if p, err := DefaultLockPath(); err != nil || p != filepath.Join(home, ".psql-manager.lock") {
		t.Errorf("DefaultLockPath() = %q, %v", p, err)
	}
}
