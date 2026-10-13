package connect

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakePsql은 받은 인자와 PGCONNECT_TIMEOUT을 기록하고 body를 실행하는 가짜 psql이다.
func fakePsql(t *testing.T, body string) (psql, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	psql = filepath.Join(dir, "psql")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\necho \"PGCONNECT_TIMEOUT=$PGCONNECT_TIMEOUT\" >> '" + argsFile + "'\n" + body + "\n"
	if err := os.WriteFile(psql, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return psql, argsFile
}

func TestArgvKeepsExtraArgs(t *testing.T) {
	got := Argv("/bin/psql", "svc", []string{"-c", "select 2"})
	if want := []string{"/bin/psql", "service=svc", "-c", "select 2"}; !slices.Equal(got, want) {
		t.Errorf("Argv = %q", got)
	}
}

func TestTestSuccessAndArgs(t *testing.T) {
	psql, argsFile := fakePsql(t, "echo 1")
	res := Test(context.Background(), psql, "svc")
	if !res.OK {
		t.Fatalf("result = %+v", res)
	}
	b, _ := os.ReadFile(argsFile)
	want := "service=svc connect_timeout=3\n-w\n-X\n-A\n-t\n-c\nselect 1\nPGCONNECT_TIMEOUT=3\n"
	if string(b) != want {
		t.Errorf("psql got\n%s\nwant\n%s", b, want)
	}
}

func TestTestFailures(t *testing.T) {
	cases := map[string]struct{ body, msg string }{
		"error":         {"echo 'psql: error: connection refused' >&2; echo detail >&2; exit 2", "psql: error: connection refused"},
		"silent exit":   {"exit 3", "exit status 3"},
		"wrong output":  {"echo 2", "unexpected output from psql"},
		"zero and text": {"echo 1; exit 1", "exit status 1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			psql, _ := fakePsql(t, c.body)
			res := Test(context.Background(), psql, "svc")
			if res.OK || res.Message != c.msg {
				t.Errorf("result = %+v, want message %q", res, c.msg)
			}
		})
	}
}

func TestTestTimeout(t *testing.T) {
	// 백그라운드 자식이 출력 파이프를 쥐고 있어도 제한 시간 안에 돌아와야 한다.
	psql, _ := fakePsql(t, "sleep 5 & sleep 5")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res := Test(ctx, psql, "svc")
	if res.Duration > 3*time.Second {
		t.Errorf("Test took %v", res.Duration)
	}
	if res.OK || res.Message != "timed out" {
		t.Errorf("result = %+v", res)
	}
}

func TestTestMissingBinary(t *testing.T) {
	res := Test(context.Background(), filepath.Join(t.TempDir(), "nope"), "svc")
	if res.OK || !strings.Contains(res.Message, "no such file") {
		t.Errorf("result = %+v", res)
	}
}
