package history

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func clock(start int64) func() time.Time {
	t := start
	return func() time.Time {
		t++
		return time.Unix(t, 0)
	}
}

func TestRecordAndLoad(t *testing.T) {
	h := &History{Path: filepath.Join(t.TempDir(), "state", "psql-manager", "history"), Now: clock(100)}
	if es, err := h.Load(); err != nil || es != nil {
		t.Fatalf("Load of missing file = %v, %v", es, err)
	}
	for _, n := range []string{"a", "b", "a"} {
		if err := h.Record(n); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(h.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "101\ta\n102\tb\n103\ta\n" {
		t.Errorf("file = %q", got)
	}
	fi, _ := os.Stat(h.Path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o", fi.Mode().Perm())
	}
	if err := h.Record("bad\tname"); err == nil {
		t.Error("name with tab accepted")
	}
}

func TestMaxLines(t *testing.T) {
	// 이미 1000줄인 파일에 5번 더 기록하면 가장 오래된 5줄이 빠진다.
	h := &History{Path: filepath.Join(t.TempDir(), "history"), Now: clock(DefaultMax)}
	var seed strings.Builder
	for i := 1; i <= DefaultMax; i++ {
		fmt.Fprintf(&seed, "%d\tsvc\n", i)
	}
	if err := os.WriteFile(h.Path, []byte(seed.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := h.Record("svc"); err != nil {
			t.Fatal(err)
		}
	}
	es, err := h.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != DefaultMax || es[0].Time != 6 || es[len(es)-1].Time != DefaultMax+5 {
		t.Errorf("kept %d lines, first %d, last %d", len(es), es[0].Time, es[len(es)-1].Time)
	}
	small := &History{Path: filepath.Join(t.TempDir(), "h"), Max: 2, Now: clock(0)}
	for _, n := range []string{"a", "b", "c"} {
		small.Record(n)
	}
	if es, _ := small.Load(); len(es) != 2 || es[0].Name != "b" {
		t.Errorf("Max=2 kept %v", es)
	}
}

func TestLoadSkipsMalformedLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "history")
	os.WriteFile(p, []byte("1\ta\ngarbage\nx\tb\n2\t\n3\tc\r\n"), 0o600)
	es, err := (&History{Path: p}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 || es[0] != (Entry{1, "a"}) || es[1] != (Entry{3, "c"}) {
		t.Errorf("entries = %v", es)
	}
}

func TestWriteFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, nil, 0o600)
	// 부모가 일반 파일이라 디렉터리를 만들 수 없다.
	if err := (&History{Path: filepath.Join(blocker, "history")}).Record("a"); err == nil {
		t.Error("Record under a regular file succeeded")
	}
	// 이력 경로가 디렉터리면 읽을 수 없다.
	if err := (&History{Path: dir}).Record("a"); err == nil {
		t.Error("Record onto a directory succeeded")
	}
}

func TestSort(t *testing.T) {
	es := []Entry{{10, "old"}, {30, "new"}, {20, "mid"}, {30, "tie-later"}}
	names := []string{"zeta", "mid", "alpha", "old", "new", "tie-later"}
	Sort(names, LastUsed(es))
	if want := []string{"tie-later", "new", "mid", "old", "alpha", "zeta"}; !slices.Equal(names, want) {
		t.Errorf("order = %v, want %v", names, want)
	}
}

func TestDefaultPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if p, _ := DefaultPath(env(map[string]string{"XDG_STATE_HOME": "/state", "HOME": "/home/u"})); p != "/state/psql-manager/history" {
		t.Errorf("XDG path = %q", p)
	}
	if p, _ := DefaultPath(env(map[string]string{"XDG_STATE_HOME": "relative", "HOME": "/home/u"})); p != "/home/u/.local/state/psql-manager/history" {
		t.Errorf("relative XDG path = %q", p)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if p, err := DefaultPath(env(nil)); err != nil || !strings.HasPrefix(p, home) {
		t.Errorf("fallback path = %q, %v", p, err)
	}
}
