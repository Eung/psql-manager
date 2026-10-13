package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eung/psql-manager/internal/store"
)

// ttyApp은 터미널에서 실행한 것처럼 동작하는 App과 임시 파일 경로를 만든다.
// passwords는 ReadPassword가 차례로 돌려줄 값이다.
type ttyApp struct {
	*App
	stdout, stderr  bytes.Buffer
	svcPath, ppPath string
	dir             string
	env             map[string]string // Getenv가 돌려줄 값. HOME과 XDG_STATE_HOME은 임시 경로다.
}

func newTTYApp(t *testing.T, stdin string, passwords ...string) *ttyApp {
	t.Helper()
	dir := t.TempDir()
	ta := &ttyApp{svcPath: filepath.Join(dir, "pg_service.conf"), ppPath: filepath.Join(dir, "pgpass"), dir: dir,
		env: map[string]string{"HOME": dir, "XDG_STATE_HOME": filepath.Join(dir, "state")}}
	a := New("test")
	a.Stdin = strings.NewReader(stdin)
	a.Stdout, a.Stderr = &ta.stdout, &ta.stderr
	a.Getenv = func(k string) string { return ta.env[k] }
	a.Exec = func(string, []string, []string) error { return errors.New("exec not expected in this test") }
	a.IsTerminal = func() bool { return true }
	a.ReadPassword = func() (string, error) {
		if len(passwords) == 0 {
			return "", errors.New("no more passwords")
		}
		pw := passwords[0]
		passwords = passwords[1:]
		return pw, nil
	}
	a.OpenStore = func() (*store.Store, error) {
		st := store.New(ta.svcPath, ta.ppPath, filepath.Join(dir, "lock"))
		st.Getenv = a.Getenv
		st.Username = func() (string, error) { return "tester", nil }
		return st, nil
	}
	ta.App = a
	return ta
}

func (ta *ttyApp) file(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "<absent>"
	}
	return string(b)
}

const testPw = "tty-dummy-pw"

func TestAddInteractive(t *testing.T) {
	ta := newTTYApp(t, "db.example.com\n\nappdb\napp\n설명\na, b\n", testPw, testPw)
	if code := ta.Run([]string{"add", "svc"}); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, ta.stderr.String())
	}
	if got, want := ta.file(t, ta.svcPath), "# desc: 설명\n# tags: a, b\n[svc]\nhost=db.example.com\nport=5432\ndbname=appdb\nuser=app\n"; got != want {
		t.Errorf("service file\n got %q\nwant %q", got, want)
	}
	if got := ta.file(t, ta.ppPath); got != "# psql-manager:svc\ndb.example.com:5432:appdb:app:"+testPw+"\n" {
		t.Error("pgpass does not hold the prompted password")
	}
	for _, q := range []string{"host (empty for local socket): ", "port [5432]: ", "Password (empty for none): ", "Retype password: "} {
		if !strings.Contains(ta.stderr.String(), q) {
			t.Errorf("prompt %q missing", q)
		}
	}
	if strings.Contains(ta.stdout.String()+ta.stderr.String(), testPw) {
		t.Error("password echoed to output")
	}
}

func TestAddInteractiveWithFlagsPromptsOnlyPassword(t *testing.T) {
	ta := newTTYApp(t, "", "")
	if code := ta.Run([]string{"add", "svc", "--db", "d"}); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, ta.stderr.String())
	}
	if strings.Contains(ta.stderr.String(), "host (") {
		t.Error("asked for host although options were given")
	}
	if ta.file(t, ta.ppPath) != "<absent>" {
		t.Error("pgpass written for an empty password")
	}
}

func TestAddPasswordMismatch(t *testing.T) {
	ta := newTTYApp(t, "", "one-dummy", "two-dummy")
	if code := ta.Run([]string{"add", "svc", "--port", "1", "--db", "d", "--user", "u"}); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(ta.stderr.String(), "passwords do not match") {
		t.Errorf("stderr = %q", ta.stderr.String())
	}
	if ta.file(t, ta.svcPath) != "<absent>" {
		t.Error("service file written after a failed prompt")
	}
}

func TestAddPromptInputEnds(t *testing.T) {
	ta := newTTYApp(t, "only-host\n")
	if code := ta.Run([]string{"add", "svc"}); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(ta.stderr.String(), "input ended") {
		t.Errorf("stderr = %q", ta.stderr.String())
	}
}

func TestEditChangePassword(t *testing.T) {
	ta := newTTYApp(t, "old-dummy\n", testPw, testPw)
	if code := ta.Run([]string{"add", "svc2", "--port", "2", "--db", "d", "--user", "u", "--password-stdin"}); code != 0 {
		t.Fatalf("setup exit %d: %s", code, ta.stderr.String())
	}
	if code := ta.Run([]string{"edit", "svc2", "--change-password"}); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, ta.stderr.String())
	}
	if !strings.Contains(ta.file(t, ta.ppPath), ":2:d:u:"+testPw+"\n") {
		t.Error("password was not changed")
	}
}

func TestEditPasswordOptions(t *testing.T) {
	cases := []struct {
		name string
		tty  bool
		args []string
		want string
	}{
		{"change without tty", false, []string{"edit", "svc", "--change-password"}, "needs a terminal"},
		{"two password options", true, []string{"edit", "svc", "--remove-password", "--password-stdin"}, "only one of"},
		{"empty new password", true, []string{"edit", "svc", "--change-password"}, "empty password"},
		{"bad rename", true, []string{"edit", "svc", "--rename", "a b"}, "invalid service name"},
		{"bad option", true, []string{"edit", "svc", "--option", "novalue"}, "key=value"},
		{"option for conn key", true, []string{"edit", "svc", "--option", "host=x"}, "use --host"},
		{"duplicate option", true, []string{"edit", "svc", "--option", "a=1", "--option", "a=2"}, "more than once"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ta := newTTYApp(t, "", "")
			ta.IsTerminal = func() bool { return c.tty }
			if code := ta.Run(c.args); code != 2 {
				t.Fatalf("exit %d, want 2 (stderr %q)", code, ta.stderr.String())
			}
			if !strings.Contains(ta.stderr.String(), c.want) {
				t.Errorf("stderr = %q, want %q", ta.stderr.String(), c.want)
			}
		})
	}
}

func TestRmConfirmation(t *testing.T) {
	for _, c := range []struct {
		answer  string
		removed bool
	}{{"y\n", true}, {"YES\n", true}, {"n\n", false}, {"\n", false}} {
		t.Run(strings.TrimSpace(c.answer), func(t *testing.T) {
			ta := newTTYApp(t, "")
			ta.IsTerminal = func() bool { return false }
			ta.Run([]string{"add", "svc", "--db", "d"})
			ta.IsTerminal = func() bool { return true }
			ta.Stdin = strings.NewReader(c.answer)
			if code := ta.Run([]string{"rm", "svc"}); code != 0 {
				t.Fatalf("exit %d: %s", code, ta.stderr.String())
			}
			if got := strings.Contains(ta.file(t, ta.svcPath), "[svc]"); got == c.removed {
				t.Errorf("removed = %v, want %v", !got, c.removed)
			}
			if !strings.Contains(ta.stderr.String(), `Remove service "svc"`) {
				t.Errorf("no confirmation prompt: %q", ta.stderr.String())
			}
		})
	}
}

func TestRmMissingAsksNothing(t *testing.T) {
	ta := newTTYApp(t, "y\n")
	if code := ta.Run([]string{"rm", "missing"}); code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	if strings.Contains(ta.stderr.String(), "Remove service") {
		t.Error("asked for confirmation of a missing service")
	}
}

func TestShowRevealPassword(t *testing.T) {
	ta := newTTYApp(t, testPw+"\n")
	ta.IsTerminal = func() bool { return false }
	if code := ta.Run([]string{"add", "svc", "--port", "1", "--db", "d", "--user", "u", "--password-stdin"}); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.stderr.String())
	}
	ta.stdout.Reset()
	ta.Run([]string{"show", "svc"})
	if strings.Contains(ta.stdout.String(), testPw) || !strings.Contains(ta.stdout.String(), passwordMask) {
		t.Errorf("show without reveal leaked or did not mask:\n%s", strings.ReplaceAll(ta.stdout.String(), testPw, "<LEAK>"))
	}
	ta.stdout.Reset()
	if code := ta.Run([]string{"show", "svc", "--reveal-password"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(ta.stdout.String(), testPw) {
		t.Error("--reveal-password did not print the password")
	}
}

func TestShowSharedPassword(t *testing.T) {
	ta := newTTYApp(t, testPw+"\n")
	ta.IsTerminal = func() bool { return false }
	ta.Run([]string{"add", "owner", "--host", "h", "--port", "1", "--db", "d", "--user", "u", "--password-stdin"})
	ta.Run([]string{"add", "follower", "--host", "h", "--port", "1", "--db", "d", "--user", "u"})
	ta.stdout.Reset()
	ta.Run([]string{"show", "follower"})
	if !strings.Contains(ta.stdout.String(), `(shared with "owner", `) {
		t.Errorf("show = %s", strings.ReplaceAll(ta.stdout.String(), testPw, "<LEAK>"))
	}
}

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		{"add"}, {"add", "a", "b"}, {"add", "--bogus"}, {"list", "extra"}, {"search"},
		{"show"}, {"rm"}, {"path", "extra"}, {"help", "nope"},
	}
	for _, args := range cases {
		ta := newTTYApp(t, "")
		if code := ta.Run(args); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestHelpForEveryCommand(t *testing.T) {
	for _, c := range commands {
		ta := newTTYApp(t, "")
		if code := ta.Run([]string{"help", c.name}); code != 0 || !strings.Contains(ta.stdout.String(), "psql-manager "+c.name) {
			t.Errorf("help %s: exit %d, stdout %q", c.name, code, ta.stdout.String())
		}
		ta = newTTYApp(t, "")
		if code := ta.Run([]string{c.name, "-h"}); code != 0 || ta.stdout.Len() == 0 {
			t.Errorf("%s -h: exit %d", c.name, code)
		}
	}
}

func TestParseInterspersedAndDoubleDash(t *testing.T) {
	fs := newFlagSet("x")
	v := fs.String("v", "", "")
	pos, err := parse(fs, []string{"a", "-v", "1", "b", "--", "-v", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if *v != "1" || strings.Join(pos, " ") != "a b -v c" {
		t.Errorf("v=%q pos=%q", *v, pos)
	}
}

func TestMessageStripsOnlyPackagePrefixes(t *testing.T) {
	cases := map[string]string{
		"store: invalid input: svcfile: value must not contain line breaks": "invalid input: value must not contain line breaks",
		"store: svcfile: nested":                                     "nested",
		"cannot write history: mkdir /x/history: not a directory":    "cannot write history: mkdir /x/history: not a directory",
		"read /home/u/.local/state/psql-manager/history: is a dir":   "read /home/u/.local/state/psql-manager/history: is a dir",
		"a\npgpass: line 2: bad":                                     "a\nline 2: bad",
		"x: /p/pg_service.conf: svcfile: line 2: expected key=value": "x: /p/pg_service.conf: line 2: expected key=value",
	}
	for in, want := range cases {
		if got := stripPrefixes(in); got != want {
			t.Errorf("stripPrefixes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHints(t *testing.T) {
	for _, err := range []error{store.ErrMissingField, store.ErrKeyConflict, store.ErrShadowed,
		store.ErrInvalidServiceFile, store.ErrInvalidPgpass, store.ErrLocked, os.ErrPermission} {
		if hint(err) == "" {
			t.Errorf("no hint for %v", err)
		}
	}
	if hint(store.ErrNotFound) != "" {
		t.Error("unexpected hint for ErrNotFound")
	}
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{usagef("x"), exitUsage},
		{store.ErrNotFound, exitNotFound},
		{notFound("a", store.ErrNotFound), exitNotFound},
		{notFound("a", store.ErrExists), exitUsage},
		{store.ErrMissingField, exitUsage},
		{fmt.Errorf("%w: x", store.ErrInvalidInput), exitUsage},
		{store.ErrShadowed, exitFile},
		{store.ErrInvalidPgpass, exitFile},
		{os.ErrPermission, exitFile},
	}
	for _, c := range cases {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("exitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
	if notFound("a", os.ErrPermission) != os.ErrPermission {
		t.Error("notFound changed an unrelated error")
	}
}

func TestOpenStoreError(t *testing.T) {
	ta := newTTYApp(t, "")
	ta.OpenStore = func() (*store.Store, error) { return nil, os.ErrPermission }
	for _, args := range [][]string{{"list"}, {"show", "a"}, {"rm", "-y", "a"}, {"search", "a"}, {"path"},
		{"add", "a", "--db", "d"}, {"edit", "a", "--db", "d"}} {
		if code := ta.Run(args); code != exitFile {
			t.Errorf("%v: exit %d, want %d", args, code, exitFile)
		}
	}
}

func TestDefaultApp(t *testing.T) {
	a := New("v1")
	if a.Stdout != os.Stdout || a.Getenv == nil || a.IsTerminal == nil || a.ReadPassword == nil || a.OpenStore == nil {
		t.Error("New left defaults unset")
	}
	// 실제 홈을 건드리지 않도록 경로를 임시 디렉터리로 돌린다.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("PGSERVICEFILE", filepath.Join(dir, "svc"))
	t.Setenv("PGPASSFILE", filepath.Join(dir, "pgpass"))
	st, err := a.OpenStore()
	if err != nil || st.ServicePath != filepath.Join(dir, "svc") {
		t.Errorf("OpenStore = %v, %v", st, err)
	}
}
