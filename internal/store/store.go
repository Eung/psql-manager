package store

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/Eung/psql-manager/internal/fsutil"
	"github.com/Eung/psql-manager/internal/pgpass"
	"github.com/Eung/psql-manager/internal/svcfile"
)

// store 연산이 돌려주는 오류. 메시지에는 비밀번호를 절대 넣지 않는다.
var (
	ErrNotFound     = errors.New("store: service not found")
	ErrExists       = errors.New("store: service already exists")
	ErrInvalidName  = errors.New("store: invalid service name (allowed: A-Z a-z 0-9 . _ -)")
	ErrMissingField = errors.New("store: port, dbname and user are required to store a password")
	ErrKeyConflict  = errors.New("store: another service already stores a password for the same host:port:dbname:user")
	ErrShadowed     = errors.New("store: an earlier .pgpass line takes precedence over this password")
	ErrOrphanEntry  = errors.New("store: .pgpass still has an entry marked for this service; " +
		"provide a password to replace it or remove it with doctor --fix")
	ErrInvalidServiceFile = errors.New("store: service file has lines libpq cannot parse; fix them before editing")
	ErrInvalidPgpass      = errors.New("store: .pgpass has malformed lines; fix them before editing")
)

// Param은 서비스 파일의 key=value 한 줄이다.
type Param = svcfile.Param

// PasswordState는 서비스에 쓰일 비밀번호가 어디에 있는지 나타낸다.
type PasswordState int

// PasswordState 값.
const (
	PasswordNone      PasswordState = iota // 일치하는 pgpass 줄이 없음
	PasswordManaged                        // 이 서비스의 표식이 붙은 pgpass 줄이 있음
	PasswordUnmanaged                      // 표식 없는 pgpass 줄이 대신 매칭됨
	PasswordShared                         // 다른 서비스가 관리하는 pgpass 줄이 매칭됨
)

// String은 상태를 "none", "managed", "unmanaged", "shared"로 돌려준다.
func (p PasswordState) String() string {
	switch p {
	case PasswordManaged:
		return "managed"
	case PasswordUnmanaged:
		return "unmanaged"
	case PasswordShared:
		return "shared"
	default:
		return "none"
	}
}

// Service는 서비스 파일의 섹션 하나와 그 비밀번호 상태다. 비밀번호 값 자체는
// 담지 않는다. 값이 필요하면 Store.Password를 쓴다.
type Service struct {
	Name   string
	Desc   string
	Tags   []string
	Params []Param // 서비스 파일에 적힌 순서 그대로
	Line   int     // 서비스 파일에서 섹션 헤더의 줄 번호

	// 아래는 읽기 API가 채운다. Add에 넘길 때는 무시된다.
	Effective     Effective     // libpq가 실제로 쓸 값과 출처
	Password      PasswordState // 비밀번호 상태
	PasswordLine  int           // 매칭된 .pgpass 줄 번호, 없으면 0
	PasswordOwner string        // PasswordShared일 때 그 줄을 관리하는 서비스명
}

// Get은 key에 해당하는 첫 파라미터 값을 돌려준다. 없으면 ""이다.
func (s Service) Get(key string) string {
	v, _ := s.Lookup(key)
	return v
}

// Lookup은 key에 해당하는 첫 파라미터 값과, 키가 있었는지를 돌려준다.
func (s Service) Lookup(key string) (string, bool) {
	for _, p := range s.Params {
		if p.Key == key {
			return p.Value, true
		}
	}
	return "", false
}

// Update는 Store.Update에 넘기는 변경 내용이다. 비어 있는 필드는 바꾸지 않는다.
type Update struct {
	Rename   string    // 새 이름, ""면 유지
	Desc     *string   // nil이면 유지, ""면 삭제
	Tags     *[]string // nil이면 유지, 빈 슬라이스면 삭제
	Set      []Param   // 추가하거나 바꿀 파라미터
	Unset    []string  // 지울 파라미터 키
	Password *string   // nil이면 유지, ""면 pgpass 줄 삭제
}

// Store는 서비스 파일과 pgpass 파일을 서비스 단위로 함께 다룬다.
type Store struct {
	ServicePath string
	PgpassPath  string
	LockPath    string           // ""면 잠금을 걸지 않는다
	Warn        func(msg string) // 경고 출력, nil이면 버린다

	// 실제 적용 값을 계산할 때 쓰는 환경. nil이면 os.Getenv와 현재 OS 사용자를 쓴다.
	// 테스트에서 실제 환경의 영향을 받지 않도록 바꿔 끼운다.
	Getenv   func(key string) string
	Username func() (string, error)

	commit func(*fsutil.Pending) error // 테스트에서 rename 실패를 흉내 내기 위한 훅
}

// New는 주어진 경로로 Store를 만든다.
func New(servicePath, pgpassPath, lockPath string) *Store {
	return &Store{
		ServicePath: servicePath,
		PgpassPath:  pgpassPath,
		LockPath:    lockPath,
		commit:      (*fsutil.Pending).Commit,
	}
}

// Open은 PGSERVICEFILE, PGPASSFILE과 기본 경로 규칙으로 Store를 만든다.
func Open() (*Store, error) {
	sp, err := svcfile.DefaultPath()
	if err != nil {
		return nil, err
	}
	pp, err := pgpass.DefaultPath()
	if err != nil {
		return nil, err
	}
	lp, err := fsutil.DefaultLockPath()
	if err != nil {
		return nil, err
	}
	return New(sp, pp, lp), nil
}

func (s *Store) warn(format string, args ...any) {
	if s.Warn != nil {
		s.Warn(fmt.Sprintf(format, args...))
	}
}

// snapshot은 한 번 읽어 들인 두 파일과 원본 바이트다.
type snapshot struct {
	svc       *svcfile.File
	pp        *pgpass.File
	svcOrig   []byte
	ppOrig    []byte
	svcExists bool
	ppExists  bool
}

func readFile(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// load는 두 파일을 읽는다. 없는 파일은 빈 파일로 본다.
//
// 어느 파일이든 이해하지 못한 줄이 있으면 strict(쓰기)일 때는 사용자가 먼저
// 고치도록 줄 번호와 함께 오류를 돌려주고(두 파일 모두 해당하면 둘 다 알린다),
// 읽기일 때는 경고만 한다. 이해하지 못한 줄은 원문 그대로 보존한다.
func (s *Store) load(strict bool) (*snapshot, error) {
	var sn snapshot
	var err error
	if sn.svcOrig, sn.svcExists, err = readFile(s.ServicePath); err != nil {
		return nil, err
	}
	if sn.ppOrig, sn.ppExists, err = readFile(s.PgpassPath); err != nil {
		return nil, err
	}
	var invalid []error
	if sn.svc, err = svcfile.Parse(sn.svcOrig); err != nil {
		invalid = append(invalid, fmt.Errorf("%w: %s: %w", ErrInvalidServiceFile, s.ServicePath, err))
	}
	if sn.pp, err = pgpass.Parse(sn.ppOrig); err != nil {
		invalid = append(invalid, fmt.Errorf("%w: %s: %w", ErrInvalidPgpass, s.PgpassPath, err))
	}
	if strict && len(invalid) > 0 {
		return nil, errors.Join(invalid...)
	}
	for _, e := range invalid {
		s.warn("%v", e)
	}
	return &sn, nil
}

// mutate는 잠금을 잡고 두 파일을 읽어 fn으로 메모리에서 바꾼 뒤 한 번에 쓴다.
// fn이 오류를 돌려주면 아무것도 쓰지 않는다.
func (s *Store) mutate(fn func(*snapshot) error) error {
	if s.LockPath != "" {
		l, err := fsutil.Acquire(s.LockPath)
		if err != nil {
			return err
		}
		defer l.Release()
	}
	sn, err := s.load(true)
	if err != nil {
		return err
	}
	if err := fn(sn); err != nil {
		return err
	}
	return s.write(sn)
}

type change struct {
	path    string
	data    []byte
	existed bool
	p       *fsutil.Pending
}

// write는 바뀐 파일만 골라 임시 파일을 모두 쓴 다음 .bak을 만들고 차례로 rename한다.
// 임시 파일 단계에서 실패하면 원본은 그대로이고, 두 번째 rename이 실패하면
// 첫 번째 파일을 되돌린다.
func (s *Store) write(sn *snapshot) error {
	var cs []*change
	if b := sn.svc.Bytes(); !bytes.Equal(b, sn.svcOrig) {
		cs = append(cs, &change{path: s.ServicePath, data: b, existed: sn.svcExists})
	}
	if b := sn.pp.Bytes(); !bytes.Equal(b, sn.ppOrig) {
		if sn.ppExists {
			old, changed, err := fsutil.FixPerm(s.PgpassPath)
			if err != nil {
				return err
			}
			if changed {
				s.warn("%s: permission was %04o, changed to 0600", s.PgpassPath, old)
			}
		}
		cs = append(cs, &change{path: s.PgpassPath, data: b, existed: sn.ppExists})
	}

	abort := func() {
		for _, c := range cs {
			if c.p != nil {
				_ = c.p.Abort()
			}
		}
	}
	for _, c := range cs {
		p, err := fsutil.Prepare(c.path, c.data)
		if err != nil {
			abort()
			return err
		}
		c.p = p
	}
	for _, c := range cs {
		if c.existed {
			if err := fsutil.Backup(c.path); err != nil {
				abort()
				return err
			}
		}
	}
	for i, c := range cs {
		if err := s.commit(c.p); err != nil {
			abort()
			errs := []error{err}
			for _, done := range cs[:i] {
				if rerr := fsutil.Rollback(done.path, done.existed); rerr != nil {
					errs = append(errs, fmt.Errorf("store: could not restore %s: %w", done.path, rerr))
				}
			}
			return errors.Join(errs...)
		}
	}
	return nil
}

// toService는 섹션 하나를 Service로 만들고, 실제 적용 값과 비밀번호 상태를 채운다.
// 비밀번호 값은 Service에 넣지 않고 따로 돌려준다.
func (s *Store) toService(sec *svcfile.Section, pp *pgpass.File) (Service, string) {
	m := sec.Meta()
	svc := Service{Name: sec.Name(), Desc: m.Desc, Tags: m.Tags, Params: sec.Params(), Line: sec.Line()}
	svc.Effective = s.resolve(svc)

	// 이 서비스의 표식이 붙은 줄이 있으면 managed다.
	for _, r := range pp.Records() {
		if r.Owner == svc.Name {
			svc.Password, svc.PasswordLine = PasswordManaged, r.Line
			return svc, r.Password
		}
	}
	// 없으면 libpq가 실제로 찾을 키로 첫 일치 줄을 찾는다.
	eff := svc.Effective
	if !eff.Resolved() {
		return svc, ""
	}
	r, ok := pp.LookupRecord(eff.Host.Value, eff.Port.Value, eff.DBName.Value, eff.User.Value)
	if !ok {
		return svc, ""
	}
	svc.Password, svc.PasswordLine = PasswordUnmanaged, r.Line
	if r.Owner != "" {
		svc.Password, svc.PasswordOwner = PasswordShared, r.Owner
	}
	return svc, r.Password
}

// List는 서비스 파일의 모든 섹션을 파일 순서대로 돌려준다.
func (s *Store) List() ([]Service, error) {
	sn, err := s.load(false)
	if err != nil {
		return nil, err
	}
	var out []Service
	for _, sec := range sn.svc.Sections() {
		svc, _ := s.toService(sec, sn.pp)
		out = append(out, svc)
	}
	return out, nil
}

// Get은 이름이 name인 서비스를 돌려준다.
func (s *Store) Get(name string) (Service, error) {
	sn, err := s.load(false)
	if err != nil {
		return Service{}, err
	}
	sec := sn.svc.Section(name)
	if sec == nil {
		return Service{}, ErrNotFound
	}
	svc, _ := s.toService(sec, sn.pp)
	return svc, nil
}

// Password는 서비스에 쓰일 비밀번호와 그 상태를 돌려준다. 호출하는 쪽은 값을
// 로그나 오류 메시지에 남기지 않아야 한다.
func (s *Store) Password(name string) (string, PasswordState, error) {
	sn, err := s.load(false)
	if err != nil {
		return "", PasswordNone, err
	}
	sec := sn.svc.Section(name)
	if sec == nil {
		return "", PasswordNone, ErrNotFound
	}
	svc, pw := s.toService(sec, sn.pp)
	return pw, svc.Password, nil
}

// Add는 서비스를 서비스 파일에 추가하고, password가 있으면 pgpass 줄도 함께 쓴다.
// 서비스 파일에는 없는데 .pgpass에 같은 이름의 관리 줄(고아 항목)이 남아 있으면,
// 비밀번호를 주면 새 값으로 덮어쓰고 비밀번호가 없으면 ErrOrphanEntry로 거부한다.
func (s *Store) Add(svc Service, password string) error {
	if !svcfile.ValidName(svc.Name) {
		return ErrInvalidName
	}
	return s.mutate(func(sn *snapshot) error {
		if sn.svc.Section(svc.Name) != nil {
			return ErrExists
		}
		if password == "" && sn.pp.HasMarker(svc.Name) {
			return ErrOrphanEntry
		}
		sec, err := sn.svc.AddSection(svc.Name, svcfile.Meta{Desc: svc.Desc, Tags: svc.Tags}, svc.Params)
		if err != nil {
			return err
		}
		return syncPassword(sn.pp, svc.Name, sec.Params(), password)
	})
}

// Update는 서비스 하나를 바꾼다. host, port, dbname, user가 바뀌면 소유한
// pgpass 줄도 새 값으로 갱신하고, 이름이 바뀌면 표식만 바꾼다.
func (s *Store) Update(name string, u Update) error {
	if u.Rename != "" && !svcfile.ValidName(u.Rename) {
		return ErrInvalidName
	}
	return s.mutate(func(sn *snapshot) error {
		sec := sn.svc.Section(name)
		if sec == nil {
			return ErrNotFound
		}
		old, hadPassword := sn.pp.Managed(name)

		if u.Rename != "" && u.Rename != name {
			if err := sec.Rename(u.Rename); err != nil {
				if errors.Is(err, svcfile.ErrDuplicateSection) {
					return ErrExists
				}
				return err
			}
			// 같은 이름의 관리 줄이 남아 있으면(고아 항목) 이름을 넘겨받지 않고 거부한다.
			if err := sn.pp.RenameManaged(name, u.Rename); err != nil && !errors.Is(err, pgpass.ErrNotManaged) {
				if errors.Is(err, pgpass.ErrDuplicate) {
					return fmt.Errorf("%w: .pgpass still has an entry for %q", ErrExists, u.Rename)
				}
				return err
			}
			name = u.Rename
		}
		if u.Desc != nil {
			if err := sec.SetDesc(*u.Desc); err != nil {
				return err
			}
		}
		if u.Tags != nil {
			if err := sec.SetTags(*u.Tags); err != nil {
				return err
			}
		}
		for _, p := range u.Set {
			if err := sec.Set(p.Key, p.Value); err != nil {
				return err
			}
		}
		for _, k := range u.Unset {
			sec.Unset(k)
		}

		password := ""
		switch {
		case u.Password != nil:
			password = *u.Password
		case hadPassword:
			password = old.Password
		}
		return syncPassword(sn.pp, name, sec.Params(), password)
	})
}

// Remove는 서비스 섹션과 그 서비스가 소유한 pgpass 줄을 함께 지운다.
func (s *Store) Remove(name string) error {
	return s.mutate(func(sn *snapshot) error {
		sec := sn.svc.Section(name)
		if sec == nil {
			return ErrNotFound
		}
		if err := sec.Remove(); err != nil {
			return err
		}
		sn.pp.RemoveManaged(name)
		return nil
	})
}

// syncPassword는 name이 소유한 pgpass 줄을 params와 password에 맞춘다. password가
// 비어 있으면 소유 줄을 지운다.
func syncPassword(pp *pgpass.File, name string, params []Param, password string) error {
	if password == "" {
		pp.RemoveManaged(name)
		return nil
	}
	get := func(key string) string {
		for _, p := range params {
			if p.Key == key {
				return p.Value
			}
		}
		return ""
	}
	e := pgpass.Entry{
		Host:     pgpass.NormalizeHost(get("host")),
		Port:     get("port"),
		Database: get("dbname"),
		User:     get("user"),
		Password: password,
	}
	if e.Port == "" || e.Database == "" || e.User == "" {
		return ErrMissingField
	}
	for _, r := range pp.Records() {
		if r.Owner != "" && r.Owner != name && !r.Wildcard &&
			r.Host == e.Host && r.Port == e.Port && r.Database == e.Database && r.User == e.User {
			return fmt.Errorf("%w (service %q)", ErrKeyConflict, r.Owner)
		}
	}
	if err := pp.SetManaged(name, e); err != nil {
		return err
	}
	// 표식 없는 줄이 먼저 매칭되면 새 비밀번호가 쓰이지 않으므로 거부한다.
	if r, ok := pp.LookupRecord(e.Host, e.Port, e.Database, e.User); ok && r.Owner != name {
		return fmt.Errorf("%w (line %d)", ErrShadowed, r.Line)
	}
	return nil
}
