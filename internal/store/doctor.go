package store

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/Eung/psql-manager/internal/fsutil"
	"github.com/Eung/psql-manager/internal/pgpass"
	"github.com/Eung/psql-manager/internal/svcfile"
)

// ErrFixBlocked는 형식 오류 때문에 파일을 편집하는 수정을 건너뛰었다는 뜻이다.
var ErrFixBlocked = errors.New("store: fix the syntax errors first; fixes that edit the files were skipped")

// Severity는 점검 결과의 심각도다.
type Severity int

// Severity 값. 숫자가 클수록 심각하다.
const (
	SeverityInfo Severity = iota
	SeverityWarning
	SeverityError
)

// String은 "info", "warning", "error"를 돌려준다.
func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	default:
		return "info"
	}
}

// 점검 항목의 종류. 스크립트가 의존할 수 있으므로 이름을 바꾸지 않는다.
const (
	KindServiceSyntax      = "service-syntax"       // 서비스 파일에 해석할 수 없는 줄
	KindPgpassSyntax       = "pgpass-syntax"        // .pgpass에 형식이 잘못된 줄
	KindDuplicateService   = "duplicate-service"    // 같은 이름의 섹션이 또 있음
	KindPgpassPermission   = "pgpass-permission"    // .pgpass 권한이 0600이 아님
	KindOrphanEntry        = "orphan-entry"         // 표식은 있는데 서비스가 없음
	KindMarkerWithoutEntry = "marker-without-entry" // 표식 아래에 항목 줄이 없음
	KindEntryOutOfSync     = "entry-out-of-sync"    // 관리 줄의 키가 서비스 값과 다름
	KindEntryShadowed      = "entry-shadowed"       // 관리 줄보다 앞 줄이 먼저 매칭됨
	KindLineShadowed       = "line-shadowed"        // 표식 없는 줄이 앞 줄에 가려 쓰이지 않음
	KindNoPassword         = "no-password"          // 서비스에 맞는 .pgpass 줄이 없음
)

// Problem은 점검 결과 하나다. 메시지에는 비밀번호를 넣지 않는다.
type Problem struct {
	Kind     string
	Severity Severity
	File     string // 문제가 있는 파일 경로
	Line     int    // 줄 번호, 파일 전체에 대한 문제면 0
	Service  string // 관련 서비스 이름, 없으면 ""
	Message  string
	Fixable  bool // Fix로 고칠 수 있는지
}

// Diagnose는 두 파일의 불일치를 점검한다. 파일을 바꾸지 않는다.
// 결과는 심각도 순(같으면 발견 순)이다.
func (s *Store) Diagnose() ([]Problem, error) {
	sn, err := s.read()
	if err != nil {
		return nil, err
	}
	var ps []Problem
	add := func(p Problem) { ps = append(ps, p) }

	var se svcfile.ParseErrors
	if errors.As(sn.svcErr, &se) {
		for _, e := range se {
			add(Problem{Kind: KindServiceSyntax, Severity: SeverityError, File: s.ServicePath, Line: e.Line,
				Message: e.Msg + "; libpq may refuse to connect and psql-manager will not edit the file"})
		}
	}
	var pe pgpass.ParseErrors
	if errors.As(sn.ppErr, &pe) {
		for _, e := range pe {
			add(Problem{Kind: KindPgpassSyntax, Severity: SeverityError, File: s.PgpassPath, Line: e.Line,
				Message: e.Msg + "; libpq ignores this line and psql-manager will not edit the file"})
		}
	}
	if sn.ppExists {
		fi, err := os.Stat(s.PgpassPath)
		if err != nil {
			return nil, err
		}
		if perm := fi.Mode().Perm(); perm != fsutil.FileMode {
			add(Problem{Kind: KindPgpassPermission, Severity: SeverityError, File: s.PgpassPath, Fixable: true,
				Message: fmt.Sprintf("permission is %04o; libpq ignores the file unless it is 0600", perm)})
		}
	}

	// 서비스 이름별 첫 섹션(libpq가 쓰는 섹션)과 중복 섹션.
	first := map[string]int{}
	var names []string
	for _, sec := range sn.svc.Sections() {
		if line, ok := first[sec.Name()]; ok {
			add(Problem{Kind: KindDuplicateService, Severity: SeverityError, File: s.ServicePath, Line: sec.Line(),
				Service: sec.Name(), Message: fmt.Sprintf("duplicate section; libpq uses the one at line %d", line)})
			continue
		}
		first[sec.Name()] = sec.Line()
		names = append(names, sec.Name())
	}

	for _, m := range sn.pp.Markers() {
		switch _, ok := first[m.Name]; {
		case !ok:
			add(Problem{Kind: KindOrphanEntry, Severity: SeverityError, File: s.PgpassPath, Line: m.Line,
				Service: m.Name, Fixable: true, Message: "entry is marked for a service that does not exist"})
		case !m.HasEntry:
			add(Problem{Kind: KindMarkerWithoutEntry, Severity: SeverityError, File: s.PgpassPath, Line: m.Line,
				Service: m.Name, Fixable: true, Message: "marker is not followed by an entry line"})
		}
	}

	// 관리 줄 번호 → 소유 서비스. 아래의 "가려진 줄" 점검에서 중복 보고를 피한다.
	managedLines := map[int]bool{}
	for _, name := range names {
		svc, _ := s.toService(sn.svc.Section(name), sn.pp)
		switch svc.Password {
		case PasswordNone:
			add(Problem{Kind: KindNoPassword, Severity: SeverityInfo, File: s.ServicePath, Line: svc.Line, Service: name,
				Message: "no .pgpass entry matches; psql will ask for a password unless the server needs none"})
		case PasswordManaged:
			managedLines[svc.PasswordLine] = true
			if p, ok := s.checkManaged(sn.pp, svc); ok {
				add(p)
			}
		}
	}

	// 표식 없는 줄 중 앞 줄(와일드카드 등)에 가려 절대 쓰이지 않는 줄.
	for _, r := range sn.pp.Records() {
		if r.Wildcard || managedLines[r.Line] || r.Owner != "" {
			continue
		}
		if hit, ok := sn.pp.LookupRecord(r.Host, r.Port, r.Database, r.User); ok && hit.Line != r.Line {
			msg := fmt.Sprintf("never used: line %d matches first", hit.Line)
			if hit.Wildcard {
				msg = fmt.Sprintf("never used: wildcard line %d above it matches first; move the wildcard line below", hit.Line)
			}
			add(Problem{Kind: KindLineShadowed, Severity: SeverityWarning, File: s.PgpassPath, Line: r.Line, Message: msg})
		}
	}

	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Severity > ps[j].Severity })
	return ps, nil
}

// checkManaged는 관리 줄의 키가 서비스 값과 같은지, 앞 줄에 가려지지 않는지 본다.
func (s *Store) checkManaged(pp *pgpass.File, svc Service) (Problem, bool) {
	p := Problem{Severity: SeverityError, File: s.PgpassPath, Line: svc.PasswordLine, Service: svc.Name}
	have, _ := pp.Managed(svc.Name)
	want, err := entryFor(svc.Params, have.Password)
	if err != nil {
		p.Kind = KindEntryOutOfSync
		p.Message = "service needs port, dbname and user in the service file to keep its .pgpass entry"
		return p, true
	}
	key := func(e pgpass.Entry) string { return fmt.Sprintf("%s:%s:%s:%s", e.Host, e.Port, e.Database, e.User) }
	if key(have) != key(want) {
		p.Kind, p.Fixable = KindEntryOutOfSync, true
		p.Message = fmt.Sprintf("entry is for %s but the service uses %s", key(have), key(want))
		return p, true
	}
	if hit, ok := pp.LookupRecord(want.Host, want.Port, want.Database, want.User); ok && hit.Line != svc.PasswordLine {
		p.Kind = KindEntryShadowed
		p.Message = fmt.Sprintf("line %d matches first, so this password is never used", hit.Line)
		return p, true
	}
	return Problem{}, false
}

// Fix는 Fixable인 문제를 고치고, 고친 문제를 돌려준다. 파일에 형식 오류가 있으면
// 권한만 고치고, 두 파일을 바꾸는 수정은 건너뛴 채 ErrFixBlocked를 돌려준다
// (형식 오류는 사용자가 먼저 고친다).
func (s *Store) Fix() ([]Problem, error) {
	ps, err := s.Diagnose()
	if err != nil {
		return nil, err
	}
	var fixed, edits []Problem
	syntaxErr := false
	for _, p := range ps {
		switch {
		case p.Kind == KindServiceSyntax || p.Kind == KindPgpassSyntax:
			syntaxErr = true
		case p.Kind == KindPgpassPermission:
			if _, _, err := fsutil.FixPerm(s.PgpassPath); err != nil {
				return fixed, err
			}
			fixed = append(fixed, p)
		case p.Fixable:
			edits = append(edits, p)
		}
	}
	if len(edits) == 0 {
		return fixed, nil
	}
	if syntaxErr {
		return fixed, ErrFixBlocked
	}
	err = s.mutate(func(sn *snapshot) error {
		for _, p := range edits {
			switch p.Kind {
			case KindOrphanEntry, KindMarkerWithoutEntry:
				sn.pp.RemoveManaged(p.Service)
			case KindEntryOutOfSync:
				have, _ := sn.pp.Managed(p.Service)
				if err := syncPassword(sn.pp, p.Service, sn.svc.Section(p.Service).Params(), have.Password); err != nil {
					return fmt.Errorf("%s: %w", p.Service, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return fixed, err
	}
	return append(fixed, edits...), nil
}
