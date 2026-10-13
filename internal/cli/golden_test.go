package cli

import (
	"bytes"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Eung/psql-manager/internal/store"
)

// -update를 주면 실제 결과로 기대 파일을 다시 쓴다. 다시 쓴 뒤에는 반드시 내용을 검토한다.
var update = flag.Bool("update", false, "rewrite golden files")

// 골든 파일 형식(testdata/cli/<case>/):
//
//	args                 한 줄에 인자 하나
//	stdin                표준 입력(선택)
//	env                  KEY=VALUE 한 줄씩(선택), 값의 $TMP는 임시 디렉터리
//	service.conf, pgpass 실행 전 파일(없으면 파일 없음)
//	want.service.conf    실행 후 기대 내용(없으면 실행 전과 같아야 함)
//	want.pgpass          실행 후 기대 내용(없으면 실행 전과 같아야 함)
//	want.stdout, want.stderr, want.exit
//
// 비밀번호는 파일에 평문으로 두지 않고 {{pw:이름}} 토큰으로 쓴다. 테스트는 토큰을
// 더미 값으로 바꿔 실행하고, 출력에 더미 값이 나오면 실패로 본다.
var (
	pwToken = regexp.MustCompile(`\{\{pw:([a-z0-9-]+)\}\}`)
	pwValue = regexp.MustCompile(`pw-([a-z0-9-]+)-dummy`)
)

func expandPw(s string) string   { return pwToken.ReplaceAllString(s, "pw-$1-dummy") }
func tokenizePw(s string) string { return pwValue.ReplaceAllString(s, "{{pw:$1}}") }

func readOpt(t *testing.T, path string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func TestGolden(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "..", "testdata", "cli", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no golden cases found")
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) { runGolden(t, dir) })
	}
}

func runGolden(t *testing.T, dir string) {
	tmp := t.TempDir()
	svcPath := filepath.Join(tmp, "pg_service.conf")
	ppPath := filepath.Join(tmp, "pgpass")

	// 실제 홈과 접속 환경변수의 영향을 받지 않도록 격리한다.
	for k, v := range map[string]string{
		"HOME": tmp, "PGSERVICEFILE": svcPath, "PGPASSFILE": ppPath, "PATH": "",
		"PGHOST": "", "PGPORT": "", "PGDATABASE": "", "PGUSER": "", "PSQL_MANAGER_PSQL": "",
	} {
		t.Setenv(k, v)
	}
	if envText, ok := readOpt(t, filepath.Join(dir, "env")); ok {
		for _, line := range strings.Split(strings.TrimSpace(envText), "\n") {
			k, v, _ := strings.Cut(line, "=")
			t.Setenv(k, strings.ReplaceAll(v, "$TMP", tmp))
		}
	}
	fakePsql := filepath.Join(tmp, "bin", "psql")
	if err := os.MkdirAll(filepath.Dir(fakePsql), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakePsql, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	inputs := map[string]string{"service.conf": svcPath, "pgpass": ppPath}
	before := map[string]*string{}
	for name, path := range inputs {
		content, ok := readOpt(t, filepath.Join(dir, name))
		if !ok {
			before[name] = nil
			continue
		}
		content = expandPw(content)
		before[name] = &content
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	argsText, ok := readOpt(t, filepath.Join(dir, "args"))
	if !ok {
		t.Fatal("missing args file")
	}
	var args []string
	if argsText != "" {
		args = strings.Split(strings.TrimSuffix(argsText, "\n"), "\n")
	}
	stdin, _ := readOpt(t, filepath.Join(dir, "stdin"))

	var stdout, stderr bytes.Buffer
	a := New("test")
	a.Stdin = strings.NewReader(expandPw(stdin))
	a.Stdout, a.Stderr = &stdout, &stderr
	a.IsTerminal = func() bool { return false }
	a.ReadPassword = func() (string, error) { return "", errors.New("no terminal in golden tests") }
	open := a.OpenStore
	a.OpenStore = func() (*store.Store, error) {
		st, err := open()
		if err == nil {
			st.Username = func() (string, error) { return "tester", nil }
		}
		return st, err
	}
	code := a.Run(args)

	// 어떤 출력에도 비밀번호(더미 값)가 나오면 안 된다.
	for name, out := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		if pwValue.MatchString(out) {
			t.Errorf("%s contains a password", name)
		}
	}

	norm := func(s string) string { return tokenizePw(strings.ReplaceAll(s, tmp, "$TMP")) }
	check := func(wantFile, got string, fallback *string) {
		t.Helper()
		path := filepath.Join(dir, wantFile)
		want, ok := readOpt(t, path)
		if *update {
			// 기대 파일이 없고 결과가 기본값과 같으면 파일을 만들지 않는다.
			if !ok && ((fallback == nil && got == "") || (fallback != nil && got == norm(*fallback))) {
				return
			}
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		if !ok {
			if fallback != nil {
				want = norm(*fallback)
			}
		}
		if got != want {
			t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", wantFile, got, want)
		}
	}
	check("want.stdout", norm(stdout.String()), nil)
	check("want.stderr", norm(stderr.String()), nil)
	exitText := strconv.Itoa(code) + "\n"
	zero := "0\n"
	check("want.exit", exitText, &zero)

	for name, path := range inputs {
		got, exists := readOpt(t, path)
		if !exists {
			got = "<absent>\n"
		}
		fallback := before[name]
		if fallback == nil {
			absent := "<absent>\n"
			fallback = &absent
		}
		check("want."+name, norm(got), fallback)
	}
}
