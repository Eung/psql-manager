package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Eung/psql-manager/internal/fsutil"
)

// absent는 파일을 만들지 않는다는 뜻의 테스트용 표시다.
const absent = "\x00absent"

// 테스트용 더미 비밀번호.
const (
	pwRO  = "ro-dummy:pw"
	pwNew = "new-dummy-pw"
)

// baseSvc는 테스트 기본 서비스 파일이다.
const baseSvc = `# 머리말

# desc: 정산 읽기 전용
# tags: prod, readonly
[prod-ro]
host=db.example.com
port=5432
dbname=settledb
user=readonly

[dev]
dbname=devdb
user=me
port=5432
`

// basePgpass는 테스트 기본 pgpass 파일이다. 비밀번호는 모두 더미 값이다.
const basePgpass = `# 손으로 쓴 줄
legacy.example.com:5432:legacy:app:legacy-dummy
# psql-manager:prod-ro
db.example.com:5432:settledb:readonly:ro-dummy\:pw
localhost:5432:devdb:me:dev-dummy
`

type env struct {
	dir      string
	svcPath  string
	ppPath   string
	lockPath string
	st       *Store
	warns    []string
}

func newEnv(t *testing.T, svc, pp string) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{
		dir:      dir,
		svcPath:  filepath.Join(dir, "pg_service.conf"),
		ppPath:   filepath.Join(dir, "pgpass"),
		lockPath: filepath.Join(dir, "lock"),
	}
	for path, content := range map[string]string{e.svcPath: svc, e.ppPath: pp} {
		if content == absent {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.st = New(e.svcPath, e.ppPath, e.lockPath)
	e.st.Warn = func(msg string) { e.warns = append(e.warns, msg) }
	// 실제 환경변수와 OS 사용자의 영향을 받지 않도록 고정한다.
	e.st.Getenv = func(string) string { return "" }
	e.st.Username = func() (string, error) { return "tester", nil }
	return e
}

func (e *env) read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return absent
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkSvc는 서비스 파일 내용을 비교한다. 서비스 파일에는 비밀번호가 없으므로 내용을 출력한다.
func (e *env) checkSvc(t *testing.T, want string) {
	t.Helper()
	if got := e.read(t, e.svcPath); got != want {
		t.Errorf("service file\n got %q\nwant %q", got, want)
	}
}

// checkPgpass는 pgpass 내용을 비교한다. 비밀번호가 출력되지 않도록 줄 번호만 알린다.
func (e *env) checkPgpass(t *testing.T, want string) {
	t.Helper()
	got := e.read(t, e.ppPath)
	if got == want {
		return
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; ; i++ {
		if i >= len(gl) || i >= len(wl) || gl[i] != wl[i] {
			t.Errorf("pgpass differs from line %d (got %d lines, want %d)", i+1, len(gl), len(wl))
			return
		}
	}
}

// checkUnchanged는 두 파일이 처음 그대로이고 임시 파일도 남지 않았는지 확인한다.
func (e *env) checkUnchanged(t *testing.T, svc, pp string) {
	t.Helper()
	e.checkSvc(t, svc)
	e.checkPgpass(t, pp)
	e.checkNoTemp(t)
}

func (e *env) checkNoTemp(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range entries {
		if strings.Contains(en.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", en.Name())
		}
	}
}

func ptr[T any](v T) *T { return &v }

func newService() Service {
	return Service{
		Name: "stg",
		Desc: "스테이징",
		Tags: []string{"stg"},
		Params: []Param{
			{Key: "host", Value: "stg.example.com"}, {Key: "port", Value: "5432"},
			{Key: "dbname", Value: "stgdb"}, {Key: "user", Value: "app"},
		},
	}
}

const newSvcBlock = "\n# desc: 스테이징\n# tags: stg\n[stg]\nhost=stg.example.com\nport=5432\ndbname=stgdb\nuser=app\n"

func TestAddWritesBothFiles(t *testing.T) {
	e := newEnv(t, baseSvc, basePgpass)
	if err := e.st.Add(newService(), pwNew); err != nil {
		t.Fatal(err)
	}
	e.checkSvc(t, baseSvc+newSvcBlock)
	e.checkPgpass(t, basePgpass+"# psql-manager:stg\nstg.example.com:5432:stgdb:app:"+pwNew+"\n")
	for _, p := range []string{e.svcPath, e.ppPath, e.svcPath + ".bak", e.ppPath + ".bak"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s perm = %o", filepath.Base(p), fi.Mode().Perm())
		}
	}
	if e.read(t, e.svcPath+".bak") != baseSvc {
		t.Error("service backup is not the original")
	}
	e.checkNoTemp(t)

	svc, err := e.st.Get("stg")
	if err != nil {
		t.Fatal(err)
	}
	if svc.Password != PasswordManaged || svc.Desc != "스테이징" || svc.Get("host") != "stg.example.com" {
		t.Errorf("Get = %+v", svc)
	}
	pw, st, err := e.st.Password("stg")
	if err != nil || pw != pwNew || st != PasswordManaged {
		t.Errorf("Password: state=%v err=%v match=%v", st, err, pw == pwNew)
	}
}

func TestAddWithoutPasswordLeavesPgpass(t *testing.T) {
	e := newEnv(t, absent, absent)
	if err := e.st.Add(newService(), ""); err != nil {
		t.Fatal(err)
	}
	// 빈 파일에 추가하면 앞에 빈 줄이 붙지 않는다.
	e.checkSvc(t, strings.TrimPrefix(newSvcBlock, "\n"))
	if e.read(t, e.ppPath) != absent {
		t.Error("pgpass was created although no password was given")
	}
	if svc, _ := e.st.Get("stg"); svc.Password != PasswordNone {
		t.Errorf("password state = %v", svc.Password)
	}
}

func TestAddCreatesMissingFiles(t *testing.T) {
	e := newEnv(t, absent, absent)
	if err := e.st.Add(newService(), pwNew); err != nil {
		t.Fatal(err)
	}
	e.checkPgpass(t, "# psql-manager:stg\nstg.example.com:5432:stgdb:app:"+pwNew+"\n")
	if _, err := os.Stat(e.ppPath + ".bak"); !errors.Is(err, fs.ErrNotExist) {
		t.Error("backup created for a file that did not exist")
	}
}

func TestAddRejections(t *testing.T) {
	missingPort := newService()
	missingPort.Params = slices.DeleteFunc(slices.Clone(missingPort.Params), func(p Param) bool { return p.Key == "port" })
	dup := newService()
	dup.Name = "prod-ro"
	badName := newService()
	badName.Name = "bad name"
	withPassword := newService()
	withPassword.Params = append(slices.Clone(withPassword.Params), Param{Key: "password", Value: "x"})
	sameKey := newService()
	sameKey.Params = []Param{
		{Key: "host", Value: "db.example.com"}, {Key: "port", Value: "5432"},
		{Key: "dbname", Value: "settledb"}, {Key: "user", Value: "readonly"},
	}
	shadowed := newService()
	shadowed.Params = []Param{
		{Key: "host", Value: "legacy.example.com"}, {Key: "port", Value: "5432"},
		{Key: "dbname", Value: "legacy"}, {Key: "user", Value: "app"},
	}
	cases := []struct {
		name string
		svc  Service
		pw   string
		want error
	}{
		{"duplicate name", dup, "", ErrExists},
		{"invalid name", badName, "", ErrInvalidName},
		{"password without port", missingPort, pwNew, ErrMissingField},
		{"password param", withPassword, "", nil},
		{"same key as other service", sameKey, pwNew, ErrKeyConflict},
		{"shadowed by unmanaged line", shadowed, pwNew, ErrShadowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, baseSvc, basePgpass)
			err := e.st.Add(c.svc, c.pw)
			if c.want == nil {
				if err == nil {
					t.Fatal("Add succeeded")
				}
			} else if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if err != nil && strings.Contains(err.Error(), pwNew) {
				t.Error("error message contains the password")
			}
			e.checkUnchanged(t, baseSvc, basePgpass)
		})
	}
}

func TestAddWithoutPortAllowedWithoutPassword(t *testing.T) {
	e := newEnv(t, "", "")
	svc := Service{Name: "sock", Params: []Param{{Key: "dbname", Value: "db"}}}
	if err := e.st.Add(svc, ""); err != nil {
		t.Fatal(err)
	}
	e.checkSvc(t, "[sock]\ndbname=db\n")
}

func TestAddSocketHostWritesLocalhost(t *testing.T) {
	e := newEnv(t, "", "")
	svc := Service{Name: "sock", Params: []Param{
		{Key: "host", Value: "/var/run/postgresql"}, {Key: "port", Value: "5432"},
		{Key: "dbname", Value: "db"}, {Key: "user", Value: "me"},
	}}
	if err := e.st.Add(svc, pwNew); err != nil {
		t.Fatal(err)
	}
	e.checkSvc(t, "[sock]\nhost=/var/run/postgresql\nport=5432\ndbname=db\nuser=me\n")
	e.checkPgpass(t, "# psql-manager:sock\nlocalhost:5432:db:me:"+pwNew+"\n")
}

// 서비스 파일에는 없고 .pgpass에만 남은 관리 줄(고아 항목)이 있을 때의 Add.
func TestAddOverOrphanEntry(t *testing.T) {
	orphan := basePgpass + "# psql-manager:stg\nold.example.com:5432:old:old:old-dummy\n"
	markerOnly := basePgpass + "# psql-manager:stg\n"
	t.Run("with password replaces it", func(t *testing.T) {
		e := newEnv(t, baseSvc, orphan)
		if err := e.st.Add(newService(), pwNew); err != nil {
			t.Fatal(err)
		}
		e.checkPgpass(t, basePgpass+"# psql-manager:stg\nstg.example.com:5432:stgdb:app:"+pwNew+"\n")
	})
	t.Run("with password fills marker only", func(t *testing.T) {
		e := newEnv(t, baseSvc, markerOnly)
		if err := e.st.Add(newService(), pwNew); err != nil {
			t.Fatal(err)
		}
		e.checkPgpass(t, basePgpass+"# psql-manager:stg\nstg.example.com:5432:stgdb:app:"+pwNew+"\n")
	})
	for name, pp := range map[string]string{"without password is rejected": orphan, "marker only without password is rejected": markerOnly} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, baseSvc, pp)
			if err := e.st.Add(newService(), ""); !errors.Is(err, ErrOrphanEntry) {
				t.Fatalf("err = %v, want ErrOrphanEntry", err)
			}
			e.checkUnchanged(t, baseSvc, pp)
		})
	}
}

func TestUpdateKeyChangeSyncsPgpass(t *testing.T) {
	e := newEnv(t, baseSvc, basePgpass)
	err := e.st.Update("prod-ro", Update{Set: []Param{{Key: "host", Value: "db2.example.com"}, {Key: "port", Value: "6432"}}})
	if err != nil {
		t.Fatal(err)
	}
	e.checkSvc(t, strings.Replace(baseSvc, "host=db.example.com\nport=5432", "host=db2.example.com\nport=6432", 1))
	// 비밀번호는 그대로 두고 키만 바뀐다.
	e.checkPgpass(t, strings.Replace(basePgpass, "db.example.com:5432:settledb", "db2.example.com:6432:settledb", 1))
}

func TestUpdatePassword(t *testing.T) {
	t.Run("change", func(t *testing.T) {
		e := newEnv(t, baseSvc, basePgpass)
		if err := e.st.Update("prod-ro", Update{Password: ptr(pwNew)}); err != nil {
			t.Fatal(err)
		}
		e.checkSvc(t, baseSvc)
		e.checkPgpass(t, strings.Replace(basePgpass, `ro-dummy\:pw`, pwNew, 1))
		if _, err := os.Stat(e.svcPath + ".bak"); !errors.Is(err, fs.ErrNotExist) {
			t.Error("unchanged service file was rewritten")
		}
	})
	t.Run("remove", func(t *testing.T) {
		e := newEnv(t, baseSvc, basePgpass)
		if err := e.st.Update("prod-ro", Update{Password: ptr("")}); err != nil {
			t.Fatal(err)
		}
		e.checkPgpass(t, strings.Replace(basePgpass, "# psql-manager:prod-ro\ndb.example.com:5432:settledb:readonly:ro-dummy\\:pw\n", "", 1))
	})
	t.Run("set on service without password", func(t *testing.T) {
		e := newEnv(t, baseSvc, basePgpass)
		if err := e.st.Update("dev", Update{Set: []Param{{Key: "host", Value: "dev.example.com"}}, Password: ptr(pwNew)}); err != nil {
			t.Fatal(err)
		}
		e.checkPgpass(t, basePgpass+"# psql-manager:dev\ndev.example.com:5432:devdb:me:"+pwNew+"\n")
	})
}

func TestUpdateMetaAndUnset(t *testing.T) {
	e := newEnv(t, baseSvc, basePgpass)
	err := e.st.Update("dev", Update{
		Desc:  ptr("개발"),
		Tags:  ptr([]string{"dev", "local"}),
		Set:   []Param{{Key: "sslmode", Value: "disable"}},
		Unset: []string{"port"},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.checkSvc(t, strings.Replace(baseSvc, "\n[dev]\ndbname=devdb\nuser=me\nport=5432\n",
		"\n# desc: 개발\n# tags: dev, local\n[dev]\ndbname=devdb\nuser=me\nsslmode=disable\n", 1))
	e.checkPgpass(t, basePgpass)
}

func TestUpdateRename(t *testing.T) {
	e := newEnv(t, baseSvc, basePgpass)
	if err := e.st.Update("prod-ro", Update{Rename: "settle-ro"}); err != nil {
		t.Fatal(err)
	}
	e.checkSvc(t, strings.Replace(baseSvc, "[prod-ro]", "[settle-ro]", 1))
	e.checkPgpass(t, strings.Replace(basePgpass, "# psql-manager:prod-ro", "# psql-manager:settle-ro", 1))
	if _, err := e.st.Get("prod-ro"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old name still found: %v", err)
	}
	if svc, err := e.st.Get("settle-ro"); err != nil || svc.Password != PasswordManaged {
		t.Errorf("renamed service: %+v, %v", svc, err)
	}
}

// 관리 줄이 없는 서비스는 이름만 바뀌고 pgpass는 건드리지 않는다.
func TestUpdateRenameWithoutManagedEntry(t *testing.T) {
	e := newEnv(t, baseSvc, basePgpass)
	if err := e.st.Update("dev", Update{Rename: "dev2"}); err != nil {
		t.Fatal(err)
	}
	e.checkSvc(t, strings.Replace(baseSvc, "[dev]", "[dev2]", 1))
	e.checkPgpass(t, basePgpass)
	if _, err := os.Stat(e.ppPath + ".bak"); !errors.Is(err, fs.ErrNotExist) {
		t.Error("unchanged pgpass was rewritten")
	}
	// 같은 이름으로 바꾸는 것은 아무 일도 하지 않는다.
	if err := e.st.Update("dev2", Update{Rename: "dev2"}); err != nil {
		t.Fatal(err)
	}
	e.checkSvc(t, strings.Replace(baseSvc, "[dev]", "[dev2]", 1))
}

func TestUpdateRejections(t *testing.T) {
	orphan := basePgpass + "# psql-manager:taken\nold.example.com:5432:old:old:old-dummy\n"
	cases := []struct {
		name   string
		pp     string
		target string
		u      Update
		want   error
	}{
		{"not found", basePgpass, "missing", Update{}, ErrNotFound},
		{"rename to existing", basePgpass, "prod-ro", Update{Rename: "dev"}, ErrExists},
		{"rename invalid", basePgpass, "prod-ro", Update{Rename: "a b"}, ErrInvalidName},
		{"rename onto orphan entry", orphan, "prod-ro", Update{Rename: "taken"}, ErrExists},
		{"unset port with password", basePgpass, "prod-ro", Update{Unset: []string{"port"}}, ErrMissingField},
		{"key collides with other service", basePgpass, "dev", Update{
			Set:      []Param{{Key: "host", Value: "db.example.com"}, {Key: "dbname", Value: "settledb"}, {Key: "user", Value: "readonly"}},
			Password: ptr(pwNew),
		}, ErrKeyConflict},
		{"shadowed by unmanaged line", basePgpass, "prod-ro", Update{
			Set: []Param{{Key: "host", Value: "legacy.example.com"}, {Key: "dbname", Value: "legacy"}, {Key: "user", Value: "app"}},
		}, ErrShadowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, baseSvc, c.pp)
			if err := e.st.Update(c.target, c.u); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			e.checkUnchanged(t, baseSvc, c.pp)
		})
	}
}

func TestUpdateInvalidValues(t *testing.T) {
	for name, u := range map[string]Update{
		"desc newline": {Desc: ptr("a\nb")},
		"tags comma":   {Tags: ptr([]string{"a,b"})},
		"password key": {Set: []Param{{Key: "password", Value: "x"}}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, baseSvc, basePgpass)
			if err := e.st.Update("prod-ro", u); err == nil {
				t.Fatal("Update succeeded")
			}
			e.checkUnchanged(t, baseSvc, basePgpass)
		})
	}
}

func TestRemoveOnlyOwnedLines(t *testing.T) {
	e := newEnv(t, baseSvc, basePgpass)
	if err := e.st.Remove("prod-ro"); err != nil {
		t.Fatal(err)
	}
	e.checkSvc(t, "# 머리말\n\n[dev]\ndbname=devdb\nuser=me\nport=5432\n")
	// 표식 없는 줄(legacy, localhost)은 그대로 남는다.
	e.checkPgpass(t, "# 손으로 쓴 줄\nlegacy.example.com:5432:legacy:app:legacy-dummy\nlocalhost:5432:devdb:me:dev-dummy\n")

	if err := e.st.Remove("dev"); err != nil {
		t.Fatal(err)
	}
	// dev의 비밀번호 줄은 표식이 없으므로 지우지 않는다.
	e.checkPgpass(t, "# 손으로 쓴 줄\nlegacy.example.com:5432:legacy:app:legacy-dummy\nlocalhost:5432:devdb:me:dev-dummy\n")
	if err := e.st.Remove("dev"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Remove: %v", err)
	}
}

func TestListAndPasswordStates(t *testing.T) {
	svc := baseSvc + "\n[nouser]\nhost=x\n\n[nopw]\nhost=nopw.example.com\nuser=u\n"
	e := newEnv(t, svc, basePgpass)
	list, err := e.st.List()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]PasswordState{}
	var names []string
	for _, s := range list {
		got[s.Name] = s.Password
		names = append(names, s.Name)
	}
	if want := []string{"prod-ro", "dev", "nouser", "nopw"}; !slices.Equal(names, want) {
		t.Errorf("names = %v", names)
	}
	want := map[string]PasswordState{
		"prod-ro": PasswordManaged,
		"dev":     PasswordUnmanaged, // 빈 host는 localhost로 매칭
		"nouser":  PasswordNone,
		"nopw":    PasswordNone,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: state = %v, want %v", k, got[k], v)
		}
	}
	if pw, st, err := e.st.Password("dev"); err != nil || st != PasswordUnmanaged || pw != "dev-dummy" {
		t.Errorf("Password(dev): state=%v err=%v", st, err)
	}
	if _, _, err := e.st.Password("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Password(missing): %v", err)
	}
	if _, err := e.st.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing): %v", err)
	}
	if list[0].Line != 5 || list[0].Desc != "정산 읽기 전용" || !slices.Equal(list[0].Tags, []string{"prod", "readonly"}) {
		t.Errorf("prod-ro = %+v", list[0])
	}
	for _, st := range []PasswordState{PasswordNone, PasswordManaged, PasswordUnmanaged} {
		if st.String() == "" {
			t.Errorf("empty String for %d", st)
		}
	}
	if PasswordUnmanaged.String() != "unmanaged" {
		t.Errorf("String = %q", PasswordUnmanaged.String())
	}
}

func TestListEmpty(t *testing.T) {
	e := newEnv(t, absent, absent)
	list, err := e.st.List()
	if err != nil || len(list) != 0 {
		t.Errorf("List = %v, %v", list, err)
	}
}

// failSecond는 두 번째 rename을 실패시킨다. 임시 파일을 지워 실제 rename 오류를 낸다.
func failSecond(st *Store) {
	n := 0
	st.commit = func(p *fsutil.Pending) error {
		n++
		if n == 2 {
			_ = p.Abort()
		}
		return p.Commit()
	}
}

func TestSecondRenameFailureRestoresFirst(t *testing.T) {
	t.Run("existing files", func(t *testing.T) {
		e := newEnv(t, baseSvc, basePgpass)
		failSecond(e.st)
		if err := e.st.Add(newService(), pwNew); err == nil {
			t.Fatal("Add succeeded")
		}
		e.checkUnchanged(t, baseSvc, basePgpass)
	})
	t.Run("first file newly created", func(t *testing.T) {
		e := newEnv(t, absent, basePgpass)
		failSecond(e.st)
		if err := e.st.Add(newService(), pwNew); err == nil {
			t.Fatal("Add succeeded")
		}
		if e.read(t, e.svcPath) != absent {
			t.Error("newly created service file was not removed")
		}
		e.checkPgpass(t, basePgpass)
		e.checkNoTemp(t)
	})
	t.Run("restore failure is reported", func(t *testing.T) {
		e := newEnv(t, baseSvc, basePgpass)
		n := 0
		e.st.commit = func(p *fsutil.Pending) error {
			n++
			if n == 2 {
				// 첫 번째 파일의 백업을 없애 복구도 실패하게 한다.
				_ = os.Remove(e.svcPath + ".bak")
				_ = p.Abort()
			}
			return p.Commit()
		}
		err := e.st.Add(newService(), pwNew)
		if err == nil || !strings.Contains(err.Error(), "could not restore") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestTempFileFailureKeepsOriginals(t *testing.T) {
	e := newEnv(t, baseSvc, basePgpass)
	e.st.PgpassPath = filepath.Join(e.dir, "missing-dir", "pgpass")
	if err := e.st.Add(newService(), pwNew); err == nil {
		t.Fatal("Add succeeded")
	}
	e.checkUnchanged(t, baseSvc, basePgpass)
}

func TestBackupFailureKeepsOriginals(t *testing.T) {
	e := newEnv(t, baseSvc, basePgpass)
	// .bak 자리에 디렉터리를 두어 백업 rename을 실패시킨다.
	if err := os.Mkdir(e.ppPath+".bak", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Add(newService(), pwNew); err == nil {
		t.Fatal("Add succeeded")
	}
	e.checkSvc(t, baseSvc)
	e.checkPgpass(t, basePgpass)
}

func TestLockConflict(t *testing.T) {
	e := newEnv(t, baseSvc, basePgpass)
	l, err := fsutil.Acquire(e.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := e.st.Add(newService(), pwNew); !errors.Is(err, fsutil.ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	e.checkUnchanged(t, baseSvc, basePgpass)
	// 읽기는 잠금과 관계없이 된다.
	if _, err := e.st.List(); err != nil {
		t.Errorf("List while locked: %v", err)
	}
}

func TestPgpassPermissionFixedWithWarning(t *testing.T) {
	e := newEnv(t, baseSvc, basePgpass)
	if err := os.Chmod(e.ppPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Add(newService(), pwNew); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(e.ppPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o", fi.Mode().Perm())
	}
	if len(e.warns) != 1 || !strings.Contains(e.warns[0], "0644") {
		t.Errorf("warnings = %q", e.warns)
	}
}

// libpq가 거부할 수 있는 서비스 파일이면 쓰기를 거부하고, 읽기는 경고만 한다.
func TestInvalidServiceFileBlocksWrites(t *testing.T) {
	svc := strings.Replace(baseSvc, "port=5432\ndbname=settledb", "port 5432\ndbname=settledb", 1) + "[unterminated\n"
	e := newEnv(t, svc, basePgpass)
	writes := map[string]func() error{
		"add":    func() error { return e.st.Add(newService(), pwNew) },
		"update": func() error { return e.st.Update("dev", Update{Desc: ptr("x")}) },
		"remove": func() error { return e.st.Remove("dev") },
	}
	for name, w := range writes {
		err := w()
		if !errors.Is(err, ErrInvalidServiceFile) {
			t.Fatalf("%s: err = %v, want ErrInvalidServiceFile", name, err)
		}
		// 고칠 줄 번호는 알려 주되 줄 내용은 넣지 않는다.
		if msg := err.Error(); !strings.Contains(msg, "line 7") || !strings.Contains(msg, "line 15") ||
			strings.Contains(msg, "port 5432") || strings.Contains(msg, "unterminated\n") {
			t.Errorf("%s: error = %q", name, msg)
		}
	}
	e.checkUnchanged(t, svc, basePgpass)
	if len(e.warns) != 0 {
		t.Errorf("writes should fail instead of warning: %q", e.warns)
	}

	list, err := e.st.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if len(e.warns) != 1 || !strings.Contains(e.warns[0], "line 7") {
		t.Errorf("warnings = %q", e.warns)
	}
}

// .pgpass에 형식이 잘못된 줄이 있어도 쓰기를 거부하고, 읽기는 경고만 한다.
func TestBrokenPgpassBlocksWrites(t *testing.T) {
	pp := basePgpass + "secret-in-broken-line\n"
	e := newEnv(t, baseSvc, pp)
	err := e.st.Add(newService(), pwNew)
	if !errors.Is(err, ErrInvalidPgpass) || errors.Is(err, ErrInvalidServiceFile) {
		t.Fatalf("err = %v, want only ErrInvalidPgpass", err)
	}
	// 줄 번호는 알리되 비밀번호가 들어 있을 수 있는 줄 내용은 넣지 않는다.
	if msg := err.Error(); !strings.Contains(msg, "line 6") || strings.Contains(msg, "secret-in-broken-line") {
		t.Errorf("error = %q", msg)
	}
	e.checkUnchanged(t, baseSvc, pp)

	if _, err := e.st.Get("prod-ro"); err != nil {
		t.Fatal(err)
	}
	if len(e.warns) != 1 || !strings.Contains(e.warns[0], "line 6") || strings.Contains(e.warns[0], "secret-in-broken-line") {
		t.Errorf("warnings = %q", e.warns)
	}
}

// 두 파일이 모두 잘못되었으면 한 번에 둘 다 알린다.
func TestBothFilesInvalidReported(t *testing.T) {
	svc := baseSvc + "bogus\n"
	pp := basePgpass + "broken\n"
	e := newEnv(t, svc, pp)
	err := e.st.Remove("dev")
	if !errors.Is(err, ErrInvalidServiceFile) || !errors.Is(err, ErrInvalidPgpass) {
		t.Fatalf("err = %v, want both", err)
	}
	e.checkUnchanged(t, svc, pp)
}

func TestReadErrors(t *testing.T) {
	e := newEnv(t, baseSvc, basePgpass)
	e.st.ServicePath = e.dir // 디렉터리는 파일로 읽을 수 없다
	if _, err := e.st.List(); err == nil {
		t.Error("List of a directory succeeded")
	}
	if err := e.st.Add(newService(), ""); err == nil {
		t.Error("Add with unreadable service file succeeded")
	}
	e.st.ServicePath, e.st.PgpassPath = e.svcPath, e.dir
	if _, err := e.st.Get("dev"); err == nil {
		t.Error("Get with unreadable pgpass succeeded")
	}
	if _, _, err := e.st.Password("dev"); err == nil {
		t.Error("Password with unreadable pgpass succeeded")
	}
	if _, err := e.st.Search("dev"); err == nil {
		t.Error("Search with unreadable pgpass succeeded")
	}
}

func TestFullSampleKeepsOtherBytes(t *testing.T) {
	sample, err := os.ReadFile(filepath.Join("..", "..", "testdata", "svcfile", "full.conf"))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, string(sample), "")
	if err := e.st.Add(newService(), pwNew); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Update("prod-settle-ro", Update{Set: []Param{{Key: "port", Value: "6432"}}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Remove("stg"); err != nil {
		t.Fatal(err)
	}
	e.checkSvc(t, strings.Replace(string(sample), "port=5432\ndbname=settledb\nuser=readonly", "port=6432\ndbname=settledb\nuser=readonly", 1))
	e.checkPgpass(t, "")
}

func TestOpenUsesEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PGSERVICEFILE", "")
	t.Setenv("PGPASSFILE", filepath.Join(home, "custom-pgpass"))
	st, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if st.ServicePath != filepath.Join(home, ".pg_service.conf") ||
		st.PgpassPath != filepath.Join(home, "custom-pgpass") ||
		st.LockPath != filepath.Join(home, ".psql-manager.lock") {
		t.Errorf("paths = %q %q %q", st.ServicePath, st.PgpassPath, st.LockPath)
	}
}
