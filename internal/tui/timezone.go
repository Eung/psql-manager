package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// localtimePath는 시스템 시간대를 가리키는 심볼릭 링크다.
const localtimePath = "/etc/localtime"

// defaultPort는 추가 폼의 port 칸에 미리 넣는 값이다.
const defaultPort = "5432"

// tzName은 IANA 시간대 이름(예: Asia/Seoul, UTC, America/Argentina/Buenos_Aires) 형식이다.
var tzName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+)*$`)

// detectTimezone은 path가 가리키는 zoneinfo 파일 경로에서 시간대 이름을 읽는다.
// 예: /etc/localtime -> /usr/share/zoneinfo/Asia/Seoul 이면 "Asia/Seoul".
// 링크가 아니거나(일반 파일로 복사한 경우 등) 이름을 알아낼 수 없으면 ""다.
func detectTimezone(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	if tz := zoneFromPath(target); tz != "" {
		return tz
	}
	// 링크를 한 번 따라가서 알 수 없으면 끝까지 따라가 본다.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return zoneFromPath(real)
	}
	return ""
}

// zoneFromPath는 경로에서 마지막 "zoneinfo/" 뒤를 시간대 이름으로 돌려준다.
func zoneFromPath(p string) string {
	const marker = "zoneinfo/"
	i := strings.LastIndex(p, marker)
	if i < 0 {
		return ""
	}
	tz := p[i+len(marker):]
	// Debian 계열의 zoneinfo/posix/..., zoneinfo/right/... 변형
	for _, prefix := range []string{"posix/", "right/"} {
		tz = strings.TrimPrefix(tz, prefix)
	}
	if !tzName.MatchString(tz) {
		return ""
	}
	return tz
}

// timezoneOption은 시간대를 libpq options 파라미터 값으로 만든다. tz가 없으면 ""다.
func timezoneOption(tz string) string {
	if tz == "" {
		return ""
	}
	return "-c timezone=" + tz
}
