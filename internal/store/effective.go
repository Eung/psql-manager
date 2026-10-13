package store

import (
	"os"
	"os/user"
)

// Source는 실제 적용 값이 어디서 왔는지 나타낸다.
type Source string

// Source 값.
const (
	SourceService Source = "service" // 서비스 파일
	SourceEnv     Source = "env"     // 환경변수
	SourceDefault Source = "default" // libpq 기본값
)

// Field는 libpq가 실제로 쓸 값 하나와 그 출처다.
type Field struct {
	Value  string
	Source Source
	// Detail은 출처를 보충 설명한다. env면 환경변수 이름("PGUSER"), default면
	// "unix socket", "OS user", "same as user" 같은 설명이고, service면 ""이다.
	Detail string
}

// Effective는 psql "service=<name>"으로 접속할 때 libpq가 쓸 host, port,
// dbname, user다. 현재 프로세스의 환경변수와 OS 사용자 기준이다.
type Effective struct {
	Host   Field
	Port   Field
	DBName Field
	User   Field
}

// Resolved는 user를 정할 수 있었는지 알려 준다. false면 pgpass 매칭을 판단할 수 없다.
func (e Effective) Resolved() bool { return e.User.Value != "" }

// Fallback은 서비스 파일이 아닌 곳(환경변수, 기본값)에서 온 값이 하나라도 있는지
// 알려 준다. 목록에서 "환경에 따라 달라질 수 있음"을 표시할 때 쓴다.
func (e Effective) Fallback() bool {
	for _, f := range [...]Field{e.Host, e.Port, e.DBName, e.User} {
		if f.Source != SourceService {
			return true
		}
	}
	return false
}

const defaultPort = "5432"

// envVars는 서비스 파일에 키가 없을 때 libpq가 보는 환경변수다.
var envVars = map[string]string{"host": "PGHOST", "port": "PGPORT", "dbname": "PGDATABASE", "user": "PGUSER"}

func (s *Store) getenv(key string) string {
	if s.Getenv != nil {
		return s.Getenv(key)
	}
	return os.Getenv(key)
}

func (s *Store) username() (string, error) {
	if s.Username != nil {
		return s.Username()
	}
	return currentUsername()
}

// currentUsername은 libpq처럼 현재 OS 사용자 이름을 돌려준다.
func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// resolve는 libpq와 같은 순서로 실제 적용 값을 정한다. 서비스 파일에 키가 있으면
// 그 값을 쓰고, 값이 비어 있으면 환경변수를 보지 않고 기본값으로 간다. 키가 없을
// 때만 환경변수를 보고, 그래도 없으면 기본값을 쓴다.
func (s *Store) resolve(svc Service) Effective {
	pick := func(key string) (Field, bool) {
		if v, ok := svc.Lookup(key); ok {
			if v == "" {
				return Field{}, false
			}
			return Field{Value: v, Source: SourceService}, true
		}
		if v := s.getenv(envVars[key]); v != "" {
			return Field{Value: v, Source: SourceEnv, Detail: envVars[key]}, true
		}
		return Field{}, false
	}
	var e Effective
	var ok bool
	if e.Host, ok = pick("host"); !ok {
		e.Host = Field{Source: SourceDefault, Detail: "unix socket"}
	}
	if e.Port, ok = pick("port"); !ok {
		e.Port = Field{Value: defaultPort, Source: SourceDefault}
	}
	if e.User, ok = pick("user"); !ok {
		e.User = Field{Source: SourceDefault, Detail: "OS user"}
		if name, err := s.username(); err == nil {
			e.User.Value = name
		} else {
			e.User.Detail = "OS user (unknown)"
		}
	}
	if e.DBName, ok = pick("dbname"); !ok {
		e.DBName = Field{Value: e.User.Value, Source: SourceDefault, Detail: "same as user"}
	}
	return e
}
