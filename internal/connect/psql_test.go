package connect

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeExec(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func TestFindPsql(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "b", "psql")
	writeExec(t, good, 0o755)
	writeExec(t, filepath.Join(dir, "a", "psql"), 0o644) // 실행 권한 없음
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	path := "relative" + string(filepath.ListSeparator) + filepath.Join(dir, "a") +
		string(filepath.ListSeparator) + filepath.Join(dir, "b")
	if p, src, err := FindPsql(env(map[string]string{"PATH": path})); err != nil || p != good || src != SourcePath {
		t.Errorf("PATH lookup = %q, %q, %v", p, src, err)
	}
	if p, src, err := FindPsql(env(map[string]string{"PATH": path, SourceEnv: good})); err != nil || p != good || src != SourceEnv {
		t.Errorf("env lookup = %q, %q, %v", p, src, err)
	}
	if _, src, err := FindPsql(env(map[string]string{SourceEnv: filepath.Join(dir, "a", "psql")})); !errors.Is(err, ErrPsqlNotFound) || src != SourceEnv {
		t.Errorf("non-executable env path: %q, %v", src, err)
	}
	if _, _, err := FindPsql(env(map[string]string{SourceEnv: dir})); !errors.Is(err, ErrPsqlNotFound) {
		t.Errorf("directory accepted: %v", err)
	}
	if _, _, err := FindPsql(env(nil)); !errors.Is(err, ErrPsqlNotFound) {
		t.Errorf("empty PATH: %v", err)
	}
}
