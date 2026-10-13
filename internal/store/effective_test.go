package store

import (
	"errors"
	"testing"
)

// fakeEnv는 테스트용 환경변수 맵을 Getenv 함수로 바꾼다.
func fakeEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveSources(t *testing.T) {
	cases := []struct {
		name   string
		params []Param
		env    map[string]string
		want   Effective
	}{
		{
			name: "all from service file",
			params: []Param{{Key: "host", Value: "db"}, {Key: "port", Value: "6432"},
				{Key: "dbname", Value: "app"}, {Key: "user", Value: "u"}},
			env: map[string]string{"PGUSER": "ignored", "PGHOST": "ignored"},
			want: Effective{
				Host: Field{"db", SourceService, ""}, Port: Field{"6432", SourceService, ""},
				DBName: Field{"app", SourceService, ""}, User: Field{"u", SourceService, ""},
			},
		},
		{
			name:   "defaults",
			params: []Param{{Key: "sslmode", Value: "disable"}},
			want: Effective{
				Host: Field{"", SourceDefault, "unix socket"}, Port: Field{"5432", SourceDefault, ""},
				DBName: Field{"tester", SourceDefault, "same as user"}, User: Field{"tester", SourceDefault, "OS user"},
			},
		},
		{
			name:   "environment fills missing keys",
			params: []Param{{Key: "dbname", Value: "app"}},
			env:    map[string]string{"PGHOST": "envhost", "PGPORT": "7000", "PGUSER": "analyst", "PGDATABASE": "ignored"},
			want: Effective{
				Host: Field{"envhost", SourceEnv, "PGHOST"}, Port: Field{"7000", SourceEnv, "PGPORT"},
				DBName: Field{"app", SourceService, ""}, User: Field{"analyst", SourceEnv, "PGUSER"},
			},
		},
		{
			name:   "PGDATABASE before user default",
			params: nil,
			env:    map[string]string{"PGDATABASE": "envdb"},
			want: Effective{
				Host: Field{"", SourceDefault, "unix socket"}, Port: Field{"5432", SourceDefault, ""},
				DBName: Field{"envdb", SourceEnv, "PGDATABASE"}, User: Field{"tester", SourceDefault, "OS user"},
			},
		},
		{
			// libpq는 서비스 파일의 빈 값도 "설정됨"으로 보고 환경변수를 보지 않는다.
			name:   "empty service value skips environment",
			params: []Param{{Key: "port", Value: ""}, {Key: "user", Value: ""}},
			env:    map[string]string{"PGPORT": "7000", "PGUSER": "analyst"},
			want: Effective{
				Host: Field{"", SourceDefault, "unix socket"}, Port: Field{"5432", SourceDefault, ""},
				DBName: Field{"tester", SourceDefault, "same as user"}, User: Field{"tester", SourceDefault, "OS user"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &Store{Getenv: fakeEnv(c.env), Username: func() (string, error) { return "tester", nil }}
			if got := st.resolve(Service{Params: c.params}); got != c.want {
				t.Errorf("resolve =\n %+v\nwant\n %+v", got, c.want)
			}
		})
	}
}

func TestResolveUnknownUser(t *testing.T) {
	st := &Store{Getenv: fakeEnv(nil), Username: func() (string, error) { return "", errors.New("no user") }}
	e := st.resolve(Service{Params: []Param{{Key: "host", Value: "db"}}})
	if e.Resolved() || e.User.Detail != "OS user (unknown)" || e.DBName.Value != "" {
		t.Errorf("effective = %+v", e)
	}
}

func TestResolveDefaultEnvironment(t *testing.T) {
	// Getenv, Username이 nil이면 실제 환경을 쓴다. 값 자체는 환경마다 다르므로
	// 서비스 파일 값만으로 정해지는 경우만 확인한다.
	st := &Store{}
	e := st.resolve(Service{Params: []Param{{Key: "host", Value: "h"}, {Key: "port", Value: "1"},
		{Key: "dbname", Value: "d"}, {Key: "user", Value: "u"}}})
	if e.Fallback() || !e.Resolved() {
		t.Errorf("effective = %+v", e)
	}
	if name, err := st.username(); err != nil || name == "" {
		t.Errorf("username() = %q, %v", name, err)
	}
	t.Setenv("PSQL_MANAGER_TEST_VAR", "x")
	if st.getenv("PSQL_MANAGER_TEST_VAR") != "x" {
		t.Error("getenv did not fall back to os.Getenv")
	}
}

func TestFallback(t *testing.T) {
	all := Effective{
		Host: Field{Source: SourceService}, Port: Field{Source: SourceService},
		DBName: Field{Source: SourceService}, User: Field{Source: SourceService},
	}
	if all.Fallback() {
		t.Error("all from service file reported fallback")
	}
	all.Port.Source = SourceDefault
	if !all.Fallback() {
		t.Error("default port not reported as fallback")
	}
}

func TestPasswordStateWithSources(t *testing.T) {
	svc := baseSvc + `
[mine]
host=db.example.com
dbname=app

[report]
host=db.example.com
port=5432
user=analyst

[follower]
host=db.example.com
port=5432
dbname=settledb
user=readonly
`
	pp := basePgpass + "db.example.com:5432:app:tester:mine-dummy\ndb.example.com:5432:analyst:analyst:report-dummy\n"
	e := newEnv(t, svc, pp)

	check := func(name string, state PasswordState, line int, owner string) {
		t.Helper()
		s, err := e.st.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if s.Password != state || s.PasswordLine != line || s.PasswordOwner != owner {
			t.Errorf("%s: state=%v line=%d owner=%q, want %v %d %q", name, s.Password, s.PasswordLine, s.PasswordOwner, state, line, owner)
		}
	}
	check("prod-ro", PasswordManaged, 4, "")
	check("dev", PasswordUnmanaged, 5, "")          // host 기본값(소켓) → localhost
	check("mine", PasswordUnmanaged, 6, "")         // user 기본값(OS 사용자 tester)
	check("report", PasswordUnmanaged, 7, "")       // dbname 기본값(user와 같음)
	check("follower", PasswordShared, 4, "prod-ro") // 다른 서비스의 관리 줄을 같이 씀

	// PGUSER가 있으면 그 사용자로 찾으므로 mine은 일치하는 줄이 없다.
	e.st.Getenv = fakeEnv(map[string]string{"PGUSER": "someone"})
	check("mine", PasswordNone, 0, "")
	mine, _ := e.st.Get("mine")
	if mine.Effective.User != (Field{"someone", SourceEnv, "PGUSER"}) || !mine.Effective.Fallback() {
		t.Errorf("mine effective = %+v", mine.Effective)
	}

	// OS 사용자를 알 수 없으면 판단하지 않는다.
	e.st.Getenv = fakeEnv(nil)
	e.st.Username = func() (string, error) { return "", errors.New("no user") }
	check("mine", PasswordNone, 0, "")

	if pw, st, err := e.st.Password("follower"); err != nil || st != PasswordShared || pw != "ro-dummy:pw" {
		t.Errorf("Password(follower): state=%v err=%v", st, err)
	}
	if PasswordShared.String() != "shared" {
		t.Errorf("String = %q", PasswordShared.String())
	}
}
