package store

import (
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// kinds는 문제 목록을 "종류@줄" 형태로 바꿔 비교하기 쉽게 만든다.
func kinds(ps []Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Kind+"@"+strconv.Itoa(p.Line))
	}
	return out
}

func diagnose(t *testing.T, e *env) []Problem {
	t.Helper()
	ps, err := e.st.Diagnose()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if strings.Contains(p.Message, "dummy") {
			t.Errorf("message leaks a password: %q", p.Message)
		}
	}
	return ps
}

func TestDiagnoseClean(t *testing.T) {
	svc := "[a]\nhost=h\nport=1\ndbname=d\nuser=u\n"
	pp := "# psql-manager:a\nh:1:d:u:a-dummy\n"
	e := newEnv(t, svc, pp)
	if ps := diagnose(t, e); len(ps) != 0 {
		t.Errorf("problems = %+v", ps)
	}
}

func TestDiagnoseEachProblem(t *testing.T) {
	cases := []struct {
		name    string
		svc, pp string
		perm    os.FileMode
		want    []string // 종류@줄
		fixable []bool
	}{
		{
			name: "orphan entry",
			svc:  "[a]\nport=1\n",
			pp:   "# psql-manager:gone\nh:1:d:u:x-dummy\n",
			want: []string{"orphan-entry@1", "no-password@1"},
		},
		{
			name: "marker without entry",
			svc:  "[a]\nhost=h\nport=1\ndbname=d\nuser=u\n",
			pp:   "# psql-manager:a\n\n",
			want: []string{"marker-without-entry@1", "no-password@1"},
		},
		{
			name: "duplicate section",
			svc:  "[a]\nport=1\n\n[a]\nport=2\n",
			pp:   "",
			want: []string{"duplicate-service@4", "no-password@1"},
		},
		{
			name: "permission",
			svc:  "[a]\nhost=h\nport=1\ndbname=d\nuser=u\n",
			pp:   "# psql-manager:a\nh:1:d:u:a-dummy\n",
			perm: 0o644,
			want: []string{"pgpass-permission@0"},
		},
		{
			name: "out of sync",
			svc:  "[a]\nhost=h2\nport=1\ndbname=d\nuser=u\n",
			pp:   "# psql-manager:a\nh:1:d:u:a-dummy\n",
			want: []string{"entry-out-of-sync@2"},
		},
		{
			name: "out of sync, fields missing",
			svc:  "[a]\nhost=h\ndbname=d\nuser=u\n",
			pp:   "# psql-manager:a\nh:1:d:u:a-dummy\n",
			want: []string{"entry-out-of-sync@2"},
		},
		{
			name: "managed entry shadowed by wildcard",
			svc:  "[a]\nhost=h\nport=1\ndbname=d\nuser=u\n",
			pp:   "*:*:*:*:any-dummy\n# psql-manager:a\nh:1:d:u:a-dummy\n",
			want: []string{"entry-shadowed@3"},
		},
		{
			name: "unmanaged line shadowed by wildcard",
			svc:  "",
			pp:   "h:*:*:*:any-dummy\nh:1:d:u:x-dummy\nh:1:d:u:y-dummy\n",
			want: []string{"line-shadowed@2", "line-shadowed@3"},
		},
		{
			name: "syntax errors",
			svc:  "[a]\nbroken\n",
			pp:   "only:three:fields\n",
			want: []string{"service-syntax@2", "pgpass-syntax@1", "no-password@1"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, c.svc, c.pp)
			if c.perm != 0 {
				if err := os.Chmod(e.ppPath, c.perm); err != nil {
					t.Fatal(err)
				}
			}
			if got := kinds(diagnose(t, e)); !slices.Equal(got, c.want) {
				t.Errorf("problems = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDiagnoseMessages(t *testing.T) {
	e := newEnv(t, "[a]\nhost=h2\nport=1\ndbname=d\nuser=u\n", "h:*:*:*:any-dummy\nh:1:d:u:x-dummy\n# psql-manager:a\nh:1:d:u:a-dummy\n")
	ps := diagnose(t, e)
	var msgs []string
	for _, p := range ps {
		msgs = append(msgs, p.Severity.String()+" "+p.Message)
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{
		"error entry is for h:1:d:u but the service uses h2:1:d:u",
		"warning never used: wildcard line 1 above it matches first",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if SeverityInfo.String() != "info" {
		t.Error("Severity.String")
	}
}

func TestFix(t *testing.T) {
	svc := "[a]\nhost=h2\nport=1\ndbname=d\nuser=u\n\n[b]\nport=1\n"
	pp := "# 손으로 쓴 줄\nother:1:d:u:keep-dummy\n# psql-manager:gone\nx:1:d:u:x-dummy\n# psql-manager:a\nh:1:d:u:a-dummy\n# psql-manager:b\n"
	e := newEnv(t, svc, pp)
	if err := os.Chmod(e.ppPath, 0o644); err != nil {
		t.Fatal(err)
	}
	fixed, err := e.st.Fix()
	if err != nil {
		t.Fatal(err)
	}
	got := kinds(fixed)
	slices.Sort(got)
	if want := []string{"entry-out-of-sync@6", "marker-without-entry@7", "orphan-entry@3", "pgpass-permission@0"}; !slices.Equal(got, want) {
		t.Errorf("fixed = %v, want %v", got, want)
	}
	e.checkSvc(t, svc)
	e.checkPgpass(t, "# 손으로 쓴 줄\nother:1:d:u:keep-dummy\n# psql-manager:a\nh2:1:d:u:a-dummy\n")
	fi, _ := os.Stat(e.ppPath)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o", fi.Mode().Perm())
	}
	// 고친 뒤에는 정보성 항목(b의 비밀번호 없음)만 남는다.
	if left := kinds(diagnose(t, e)); !slices.Equal(left, []string{"no-password@7"}) {
		t.Errorf("left = %v", left)
	}
}

func TestFixSkipsEditsWithSyntaxErrors(t *testing.T) {
	svc := "[a]\nbroken\n"
	pp := "# psql-manager:gone\nx:1:d:u:x-dummy\n"
	e := newEnv(t, svc, pp)
	os.Chmod(e.ppPath, 0o640)
	fixed, err := e.st.Fix()
	if !errors.Is(err, ErrFixBlocked) {
		t.Fatalf("err = %v, want ErrFixBlocked", err)
	}
	if got := kinds(fixed); !slices.Equal(got, []string{"pgpass-permission@0"}) {
		t.Errorf("fixed = %v", got)
	}
	e.checkSvc(t, svc)
	e.checkPgpass(t, pp)
}

func TestFixNothing(t *testing.T) {
	e := newEnv(t, "[a]\nport=1\n", "")
	fixed, err := e.st.Fix()
	if err != nil || len(fixed) != 0 {
		t.Errorf("Fix = %v, %v", fixed, err)
	}
}

func TestFixResyncFailure(t *testing.T) {
	// 재동기화할 새 키가 표식 없는 줄에 가려지면 아무것도 바꾸지 않고 실패한다.
	svc := "[a]\nhost=h2\nport=1\ndbname=d\nuser=u\n"
	pp := "h2:1:d:u:other-dummy\n# psql-manager:a\nh:1:d:u:a-dummy\n"
	e := newEnv(t, svc, pp)
	if _, err := e.st.Fix(); err == nil || !strings.Contains(err.Error(), "a: ") {
		t.Fatalf("err = %v", err)
	}
	e.checkUnchanged(t, svc, pp)
}

func TestDiagnoseReadError(t *testing.T) {
	e := newEnv(t, "", "")
	e.st.ServicePath = e.dir
	if _, err := e.st.Diagnose(); err == nil {
		t.Error("Diagnose of unreadable file succeeded")
	}
	if _, err := e.st.Fix(); err == nil {
		t.Error("Fix of unreadable file succeeded")
	}
}
