package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Eung/psql-manager/internal/history"
)

// execCall은 가짜 Exec가 받은 인자다.
type execCall struct {
	path string
	argv []string
}

// withServices는 서비스 셋(a, b, c)과 가짜 psql, 가짜 Exec를 준비한다.
func withServices(t *testing.T, stdin string) (*ttyApp, *[]execCall) {
	t.Helper()
	ta := newTTYApp(t, "")
	ta.IsTerminal = func() bool { return false }
	for _, n := range []string{"c", "a", "b"} {
		if code := ta.Run([]string{"add", n, "--db", n + "db", "--desc", "desc of " + n}); code != 0 {
			t.Fatalf("add %s: %s", n, ta.stderr.String())
		}
	}
	psql := filepath.Join(ta.dir, "bin", "psql")
	os.MkdirAll(filepath.Dir(psql), 0o700)
	os.WriteFile(psql, []byte("#!/bin/sh\n"), 0o755)
	ta.env["PSQL_MANAGER_PSQL"] = psql
	calls := &[]execCall{}
	ta.Exec = func(path string, argv, env []string) error {
		*calls = append(*calls, execCall{path, argv})
		return nil
	}
	ta.stdout.Reset()
	ta.stderr.Reset()
	ta.Stdin = strings.NewReader(stdin)
	return ta, calls
}

func (ta *ttyApp) historyNames(t *testing.T) []string {
	t.Helper()
	es, err := (&history.History{Path: filepath.Join(ta.dir, "state", "psql-manager", "history")}).Load()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

func TestConnectByName(t *testing.T) {
	ta, calls := withServices(t, "")
	if code := ta.Run([]string{"connect", "b", "--", "-c", "select 2", "--echo-all"}); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr.String())
	}
	psql := ta.env["PSQL_MANAGER_PSQL"]
	want := []execCall{{psql, []string{psql, "service=b", "-c", "select 2", "--echo-all"}}}
	if len(*calls) != 1 || (*calls)[0].path != want[0].path || !slices.Equal((*calls)[0].argv, want[0].argv) {
		t.Errorf("exec calls = %+v, want %+v", *calls, want)
	}
	if got := ta.historyNames(t); !slices.Equal(got, []string{"b"}) {
		t.Errorf("history = %v", got)
	}
}

func TestConnectHistoryFailureDoesNotBlock(t *testing.T) {
	ta, calls := withServices(t, "")
	blocker := filepath.Join(ta.dir, "blocker")
	os.WriteFile(blocker, nil, 0o600)
	ta.env["XDG_STATE_HOME"] = blocker // 일반 파일 아래에는 디렉터리를 만들 수 없다
	if code := ta.Run([]string{"connect", "a"}); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr.String())
	}
	if len(*calls) != 1 {
		t.Error("connect was blocked by a history failure")
	}
	if !strings.Contains(ta.stderr.String(), "cannot write history") {
		t.Errorf("stderr = %q", ta.stderr.String())
	}
}

func TestConnectExecFailure(t *testing.T) {
	ta, _ := withServices(t, "")
	ta.Exec = func(string, []string, []string) error { return errors.New("boom") }
	if code := ta.Run([]string{"connect", "a"}); code != exitFile {
		t.Errorf("exit %d, want %d", code, exitFile)
	}
}

func TestConnectUsage(t *testing.T) {
	ta, calls := withServices(t, "")
	if code := ta.Run([]string{"connect", "a", "b"}); code != exitUsage {
		t.Errorf("exit %d", code)
	}
	if len(*calls) != 0 {
		t.Error("exec called")
	}
}

func TestPickerUsesHistoryOrder(t *testing.T) {
	ta, calls := withServices(t, "")
	ta.IsTerminal = func() bool { return true }
	// b에 먼저 접속해 두면 목록 맨 위에 온다.
	ta.Run([]string{"connect", "b"})
	ta.stderr.Reset()
	ta.Stdin = strings.NewReader("2\n")
	if code := ta.Run(nil); code != 0 { // 인자 없이 실행하면 번호 선택 화면
		t.Fatalf("exit %d: %s", code, ta.stderr.String())
	}
	out := ta.stderr.String()
	if !(strings.Index(out, "1  b") < strings.Index(out, "2  a") && strings.Index(out, "2  a") < strings.Index(out, "3  c")) {
		t.Errorf("picker order:\n%s", out)
	}
	if !strings.Contains(out, "desc of a") || !strings.Contains(out, "Select a service [1-3]") {
		t.Errorf("picker output:\n%s", out)
	}
	if last := (*calls)[len(*calls)-1].argv[1]; last != "service=a" {
		t.Errorf("picked %s, want service=a", last)
	}
}

func TestPickerCancelAndInvalid(t *testing.T) {
	cases := []struct {
		input string
		code  int
	}{{"\n", 0}, {"9\n", exitUsage}, {"x\n", exitUsage}, {"", exitUsage}}
	for _, c := range cases {
		ta, calls := withServices(t, c.input)
		ta.IsTerminal = func() bool { return true }
		if code := ta.Run([]string{"connect"}); code != c.code {
			t.Errorf("input %q: exit %d, want %d (%s)", c.input, code, c.code, ta.stderr.String())
		}
		if len(*calls) != 0 {
			t.Errorf("input %q: exec called", c.input)
		}
	}
}

func TestPickerNoServices(t *testing.T) {
	ta := newTTYApp(t, "1\n")
	if code := ta.Run(nil); code != exitUsage || !strings.Contains(ta.stderr.String(), "no services") {
		t.Errorf("exit %d: %s", code, ta.stderr.String())
	}
}

// fakeTestPsql은 service=bad...로 시작하는 접속만 실패하는 가짜 psql이다.
func fakeTestPsql(t *testing.T, ta *ttyApp) {
	t.Helper()
	psql := filepath.Join(ta.dir, "bin", "psql")
	script := "#!/bin/sh\ncase \"$1\" in service=bad*) echo 'psql: error: connection refused' >&2; exit 2;; esac\necho 1\n"
	if err := os.WriteFile(psql, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestTestCommand(t *testing.T) {
	ta, _ := withServices(t, "")
	fakeTestPsql(t, ta)
	if code := ta.Run([]string{"test", "a"}); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr.String())
	}
	if got := ta.stdout.String(); got != "ok  a\n" {
		t.Errorf("stdout = %q", got)
	}

	ta.Run([]string{"add", "bad", "--db", "x"})
	ta.stdout.Reset()
	ta.stderr.Reset()
	if code := ta.Run([]string{"test", "--all"}); code != exitConnect {
		t.Fatalf("exit %d, want %d: %s", code, exitConnect, ta.stderr.String())
	}
	want := "ok    a\nok    b\nFAIL  bad  psql: error: connection refused\nok    c\n"
	if got := ta.stdout.String(); got != want {
		t.Errorf("stdout =\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(ta.stderr.String(), "connection failed: 1 of 4") {
		t.Errorf("stderr = %q", ta.stderr.String())
	}
}

func TestTestCommandErrors(t *testing.T) {
	ta, _ := withServices(t, "")
	for _, c := range []struct {
		args []string
		code int
	}{
		{[]string{"test"}, exitUsage},
		{[]string{"test", "missing"}, exitNotFound},
	} {
		if code := ta.Run(c.args); code != c.code {
			t.Errorf("%v: exit %d, want %d", c.args, code, c.code)
		}
	}
	empty := newTTYApp(t, "")
	if code := empty.Run([]string{"test", "--all"}); code != 0 || !strings.Contains(empty.stderr.String(), "no services") {
		t.Errorf("empty --all: exit %d, %q", code, empty.stderr.String())
	}
}

func TestDoctorUsage(t *testing.T) {
	ta := newTTYApp(t, "")
	if code := ta.Run([]string{"doctor", "extra"}); code != exitUsage {
		t.Errorf("exit %d", code)
	}
}

func TestBrokenHistoryWarns(t *testing.T) {
	ta, _ := withServices(t, "")
	hp := filepath.Join(ta.dir, "state", "psql-manager", "history")
	os.MkdirAll(hp, 0o700) // 이력 경로가 디렉터리라 읽을 수 없다
	if code := ta.Run([]string{"list"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(ta.stderr.String(), "cannot read history") {
		t.Errorf("stderr = %q", ta.stderr.String())
	}
	// 이력 없이 이름순으로 출력된다.
	if out := ta.stdout.String(); !(strings.Index(out, "\na ") < strings.Index(out, "\nb ") && strings.Index(out, "\nb ") < strings.Index(out, "\nc ")) {
		t.Errorf("list order:\n%s", out)
	}
}
