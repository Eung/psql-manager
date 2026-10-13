package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// binary는 TestMain에서 빌드한 psql-manager 경로다.
var binary string

// TestMain은 실제 바이너리를 임시 디렉터리에 빌드한다. connect는 syscall.Exec로
// 프로세스를 교체하므로 테스트 프로세스 안에서는 시험할 수 없다.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "psql-manager-bin-")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "psql-manager")
	out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput()
	if err != nil {
		os.RemoveAll(dir)
		panic("go build failed: " + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakePsql은 받은 인자와 환경을 로그에 남기는 가짜 psql이다. 접속 테스트
// (connect_timeout이 들어간 접속 문자열)면 service=bad...만 실패시키고, 그 밖의
// 호출(connect)은 종료 코드 7로 끝내 프로세스 교체 여부를 확인할 수 있게 한다.
const fakePsql = `#!/bin/sh
{
  echo "argv:"
  for a in "$@"; do echo "  $a"; done
  echo "PGSERVICE=${PGSERVICE-<unset>}"
  echo "PGCONNECT_TIMEOUT=${PGCONNECT_TIMEOUT-<unset>}"
} >> "$FAKE_LOG"
case "$1" in
  *connect_timeout=*)
    case "$1" in service=bad*) echo "psql: error: connection refused" >&2; exit 2;; esac
    echo 1
    exit 0;;
esac
exit 7
`

type sandbox struct {
	dir string
	env []string
	log string
}

// newSandbox는 실제 홈과 환경변수를 쓰지 않는 실행 환경을 만든다.
func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	dir := t.TempDir()
	psql := filepath.Join(dir, "bin", "psql")
	if err := os.MkdirAll(filepath.Dir(psql), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(psql, []byte(fakePsql), 0o755); err != nil {
		t.Fatal(err)
	}
	sb := &sandbox{dir: dir, log: filepath.Join(dir, "psql.log")}
	sb.env = []string{
		"HOME=" + dir,
		"PATH=/usr/bin:/bin",
		"PGSERVICEFILE=" + filepath.Join(dir, "pg_service.conf"),
		"PGPASSFILE=" + filepath.Join(dir, "pgpass"),
		"XDG_STATE_HOME=" + filepath.Join(dir, "state"),
		"PSQL_MANAGER_PSQL=" + psql,
		"FAKE_LOG=" + sb.log,
	}
	return sb
}

// run은 바이너리를 실행하고 종료 코드와 출력을 돌려준다.
func (sb *sandbox) run(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = sb.env
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, stdout.String(), stderr.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), stdout.String(), stderr.String()
	}
	t.Fatal(err)
	return 0, "", ""
}

func (sb *sandbox) takeLog(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(sb.log)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(sb.log)
	return string(b)
}

func TestConnectReplacesProcess(t *testing.T) {
	sb := newSandbox(t)
	if code, _, stderr := sb.run(t, "dummy-pw\n", "add", "svc", "--host", "h", "--port", "5432",
		"--db", "d", "--user", "u", "--password-stdin"); code != 0 {
		t.Fatalf("add: exit %d: %s", code, stderr)
	}
	code, _, stderr := sb.run(t, "", "connect", "svc", "--", "-c", "select 2", "--no-psqlrc")
	// 가짜 psql의 종료 코드가 그대로 나오면 프로세스가 psql로 교체된 것이다.
	if code != 7 {
		t.Fatalf("exit %d, want 7 from the fake psql: %s", code, stderr)
	}
	want := "argv:\n  service=svc\n  -c\n  select 2\n  --no-psqlrc\nPGSERVICE=<unset>\nPGCONNECT_TIMEOUT=<unset>\n"
	if got := sb.takeLog(t); got != want {
		t.Errorf("psql got\n%s\nwant\n%s", got, want)
	}
	hist, err := os.ReadFile(filepath.Join(sb.dir, "state", "psql-manager", "history"))
	if err != nil || !strings.HasSuffix(string(hist), "\tsvc\n") {
		t.Errorf("history = %q, %v", hist, err)
	}
}

func TestConnectMissingService(t *testing.T) {
	sb := newSandbox(t)
	if code, _, _ := sb.run(t, "", "connect", "nope"); code != 3 {
		t.Errorf("exit %d, want 3", code)
	}
	if _, err := os.Stat(sb.log); !errors.Is(err, os.ErrNotExist) {
		t.Error("psql was started for a missing service")
	}
}

func TestTestCommand(t *testing.T) {
	sb := newSandbox(t)
	for _, name := range []string{"good", "bad"} {
		if code, _, stderr := sb.run(t, "", "add", name, "--db", "d"); code != 0 {
			t.Fatalf("add %s: %s", name, stderr)
		}
	}
	code, stdout, stderr := sb.run(t, "", "test", "good")
	if code != 0 || stdout != "ok  good\n" {
		t.Fatalf("test good: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	want := "argv:\n  service=good connect_timeout=3\n  -w\n  -X\n  -A\n  -t\n  -c\n  select 1\nPGSERVICE=<unset>\nPGCONNECT_TIMEOUT=3\n"
	if got := sb.takeLog(t); got != want {
		t.Errorf("psql got\n%s\nwant\n%s", got, want)
	}

	code, stdout, _ = sb.run(t, "", "test", "--all")
	if code != 5 {
		t.Errorf("test --all: exit %d, want 5", code)
	}
	if !strings.Contains(stdout, "FAIL  bad  psql: error: connection refused\n") || !strings.Contains(stdout, "ok    good") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestPsqlNotFound(t *testing.T) {
	sb := newSandbox(t)
	sb.run(t, "", "add", "svc", "--db", "d")
	sb.env = append(sb.env, "PSQL_MANAGER_PSQL=") // 나중 값이 우선한다
	sb.env = append(sb.env, "PATH=/nonexistent")
	code, _, stderr := sb.run(t, "", "connect", "svc")
	if code != 4 || !strings.Contains(stderr, "install the PostgreSQL client") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func TestVersion(t *testing.T) {
	sb := newSandbox(t)
	if code, stdout, _ := sb.run(t, "", "--version"); code != 0 || stdout != "dev\n" {
		t.Errorf("exit %d, stdout %q", code, stdout)
	}
}
