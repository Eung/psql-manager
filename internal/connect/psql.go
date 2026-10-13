package connect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrPsqlNotFound는 실행할 psql을 찾지 못했다는 뜻이다.
var ErrPsqlNotFound = errors.New("connect: psql not found; install the PostgreSQL client or set PSQL_MANAGER_PSQL")

// psql 경로를 어디서 찾았는지 나타내는 값.
const (
	SourceEnv  = "PSQL_MANAGER_PSQL"
	SourcePath = "PATH"
)

// FindPsql은 PSQL_MANAGER_PSQL이 있으면 그 경로를, 없으면 PATH에서 psql을 찾아
// 경로와 출처를 돌려준다. exec.LookPath와 같은 규칙이지만, 테스트에서 환경을 바꿔
// 끼울 수 있도록 getenv로 PATH를 읽는다. 상대 경로 디렉터리는 건너뛴다.
func FindPsql(getenv func(string) string) (path, source string, err error) {
	if p := getenv(SourceEnv); p != "" {
		if !isExecutable(p) {
			return "", SourceEnv, fmt.Errorf("%w: %s is not an executable file", ErrPsqlNotFound, p)
		}
		return p, SourceEnv, nil
	}
	for _, dir := range filepath.SplitList(getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		if p := filepath.Join(dir, "psql"); isExecutable(p) {
			return p, SourcePath, nil
		}
	}
	return "", SourcePath, ErrPsqlNotFound
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}
