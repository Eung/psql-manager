// Package pgpass는 .pgpass 파일을 읽고 편집한다. "# psql-manager:<name>" 표식이
// 없는 줄은 절대 수정하지 않는다.
package pgpass

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// MarkerPrefix는 바로 다음 줄을 서비스가 소유한다고 표시하는 주석의 접두어다.
const MarkerPrefix = "# psql-manager:"

// File 메서드가 돌려주는 오류.
var (
	ErrInvalidName   = errors.New("pgpass: invalid service name")
	ErrInvalidValue  = errors.New("pgpass: value must not contain line breaks")
	ErrEmptyPassword = errors.New("pgpass: password is empty")
	ErrNotManaged    = errors.New("pgpass: no managed entry for service")
	ErrDuplicate     = errors.New("pgpass: service already has a managed entry")
	ErrMissingField  = errors.New("pgpass: port, database and user are required")
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// DefaultPath는 $PGPASSFILE을, 설정되어 있지 않으면 ~/.pgpass를 돌려준다.
func DefaultPath() (string, error) {
	if p := os.Getenv("PGPASSFILE"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("pgpass: locate home directory: %w", err)
	}
	return filepath.Join(home, ".pgpass"), nil
}

// NormalizeHost는 유닉스 도메인 소켓 host(빈 값이나 '/'로 시작하는 디렉터리 경로)를
// libpq가 .pgpass에서 찾는 이름인 "localhost"로 바꾼다.
func NormalizeHost(host string) string {
	if host == "" || strings.HasPrefix(host, "/") {
		return "localhost"
	}
	return host
}

// Entry는 이스케이프를 해석한 hostname:port:database:username:password 한 줄이다.
type Entry struct {
	Host     string
	Port     string
	Database string
	User     string
	Password string
}

// String은 비밀번호를 가린 형태로 항목을 출력한다.
func (e Entry) String() string {
	return fmt.Sprintf("%s:%s:%s:%s:%s", e.Host, e.Port, e.Database, e.User, mask(e.Password))
}

// GoString은 %#v 출력에서도 비밀번호를 가린다.
func (e Entry) GoString() string {
	return fmt.Sprintf("pgpass.Entry{Host:%q, Port:%q, Database:%q, User:%q, Password:%q}",
		e.Host, e.Port, e.Database, e.User, mask(e.Password))
}

func mask(pw string) string {
	if pw == "" {
		return ""
	}
	return "********"
}

// Record는 파싱한 항목 줄과 그 소유 정보다.
type Record struct {
	Entry
	Owner    string // 바로 위 표식의 서비스명, 관리 대상이 아니면 ""
	Line     int    // 1부터 시작
	Wildcard bool   // 네 키 필드 중 하나라도 "*"인지
}

// ParseError는 Parse가 이해하지 못한 줄 하나를 나타낸다. 비밀번호가 들어 있을 수
// 있으므로 줄 내용은 절대 담지 않는다.
type ParseError struct {
	Line int // 1부터 시작
	Msg  string
}

// Error는 error 인터페이스를 구현한다.
func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// ParseErrors는 Parse가 이해하지 못한 줄을 모두 모은다.
type ParseErrors []*ParseError

// Error는 error 인터페이스를 구현한다.
func (es ParseErrors) Error() string {
	msgs := make([]string, len(es))
	for i, e := range es {
		msgs[i] = e.Error()
	}
	return "pgpass: " + strings.Join(msgs, "; ")
}

type kind int

const (
	kindBlank kind = iota
	kindComment
	kindEntry
	kindInvalid
)

type line struct {
	text  string
	eol   string
	kind  kind
	owner string // 표식 주석일 때 서비스명
	entry Entry
	wild  [4]bool
}

func classify(text, eol string) *line {
	l := &line{text: text, eol: eol}
	switch {
	case strings.TrimSpace(text) == "":
		l.kind = kindBlank
	case text[0] == '#':
		l.kind = kindComment
		if name, ok := strings.CutPrefix(text, MarkerPrefix); ok {
			if name = strings.TrimSpace(name); nameRe.MatchString(name) {
				l.owner = name
			}
		}
	default:
		fields, wild := splitFields(text)
		if len(fields) < 5 {
			l.kind = kindInvalid
			return l
		}
		// libpq처럼 다섯 번째 필드까지만 쓰고 나머지는 무시한다.
		l.kind = kindEntry
		l.entry = Entry{fields[0], fields[1], fields[2], fields[3], fields[4]}
		copy(l.wild[:], wild)
	}
	return l
}

// splitFields는 이스케이프되지 않은 콜론에서 나누고 "\x"를 "x"로 해석한다.
// 원문이 정확히 "*"인 필드만 와일드카드로 본다.
func splitFields(text string) (fields []string, wild []bool) {
	var b strings.Builder
	start := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\\' && i+1 < len(text):
			i++
			b.WriteByte(text[i])
		case c == ':':
			fields, wild = append(fields, b.String()), append(wild, text[start:i] == "*")
			b.Reset()
			start = i + 1
		default:
			b.WriteByte(c)
		}
	}
	return append(fields, b.String()), append(wild, text[start:] == "*")
}

// Escape는 .pgpass 필드에 쓸 수 있도록 ':'와 '\'를 이스케이프한다.
func Escape(s string) string {
	if !strings.ContainsAny(s, `:\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == ':' || s[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Unescape는 libpq가 필드를 읽는 방식대로 Escape를 되돌린다. "\x"는 "x"가 된다.
func Unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// keyField는 host, port, database, user 값을 이스케이프한다. 글자 그대로의 "*"는
// 와일드카드로 읽히지 않도록 이스케이프한다.
func keyField(s string) string {
	if s == "*" {
		return `\*`
	}
	return Escape(s)
}

// format은 e를 .pgpass 한 줄로 만든다. 맨 앞의 '#'는 주석으로 읽히지 않도록
// 이스케이프한다.
func format(e Entry) string {
	host := keyField(e.Host)
	if strings.HasPrefix(host, "#") {
		host = `\` + host
	}
	return strings.Join([]string{host, keyField(e.Port), keyField(e.Database), keyField(e.User), Escape(e.Password)}, ":")
}

// File은 파싱한 .pgpass 파일이다.
type File struct {
	lines      []*line
	eol        string
	noFinalEOL bool
}

// Parse는 data를 줄 단위로 나눈다. 항상 사용 가능한 File을 돌려주며, 형식이
// 잘못된 줄은 원문 그대로 보존하고 ParseErrors로 한꺼번에 알린다.
func Parse(data []byte) (*File, error) {
	texts, eols, eol, noFinal := splitLines(data)
	f := &File{eol: eol, noFinalEOL: noFinal}
	var errs ParseErrors
	for i := range texts {
		l := classify(texts[i], eols[i])
		if l.kind == kindInvalid {
			errs = append(errs, &ParseError{Line: i + 1, Msg: "expected 5 colon-separated fields"})
		}
		f.lines = append(f.lines, l)
	}
	if len(errs) > 0 {
		return f, errs
	}
	return f, nil
}

// splitLines는 각 줄의 줄바꿈 문자를 그대로 유지하며 data를 나눈다. 줄바꿈 없이
// 끝나는 마지막 줄에는 기본 줄바꿈을 넣고, 대신 noFinal로 그 사실을 알린다.
func splitLines(data []byte) (texts, eols []string, defEOL string, noFinal bool) {
	s := string(data)
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			texts, eols = append(texts, s), append(eols, "")
			break
		}
		t, eol := s[:i], "\n"
		if strings.HasSuffix(t, "\r") {
			t, eol = t[:len(t)-1], "\r\n"
		}
		if defEOL == "" {
			defEOL = eol
		}
		texts, eols = append(texts, t), append(eols, eol)
		s = s[i+1:]
	}
	if defEOL == "" {
		defEOL = "\n"
	}
	if n := len(eols); n > 0 && eols[n-1] == "" {
		eols[n-1], noFinal = defEOL, true
	}
	return texts, eols, defEOL, noFinal
}

// Bytes는 파일을 직렬화한다. 건드리지 않은 줄은 읽은 그대로 돌려준다.
func (f *File) Bytes() []byte {
	var b bytes.Buffer
	for i, l := range f.lines {
		b.WriteString(l.text)
		if i < len(f.lines)-1 || !f.noFinalEOL {
			b.WriteString(l.eol)
		}
	}
	return b.Bytes()
}

func (f *File) newLine(text string) *line { return classify(text, f.eol) }

// owner는 lines[i]를 소유한다고 표시한 서비스명을 돌려준다. 없으면 ""이다.
func (f *File) owner(i int) string {
	if i > 0 {
		return f.lines[i-1].owner
	}
	return ""
}

// Records는 모든 항목 줄을 파일 순서대로 돌려준다.
func (f *File) Records() []Record {
	var out []Record
	for i, l := range f.lines {
		if l.kind == kindEntry {
			out = append(out, f.record(i))
		}
	}
	return out
}

// record는 lines[i] 항목 줄을 Record로 만든다.
func (f *File) record(i int) Record {
	l := f.lines[i]
	return Record{
		Entry:    l.entry,
		Owner:    f.owner(i),
		Line:     i + 1,
		Wildcard: slices.Contains(l.wild[:], true),
	}
}

// Lookup은 libpq처럼 접속 파라미터와 일치하는 첫 항목을 돌려준다. 소켓 host는
// "localhost"로 찾는다.
func (f *File) Lookup(host, port, database, user string) (Entry, bool) {
	r, ok := f.LookupRecord(host, port, database, user)
	return r.Entry, ok
}

// LookupRecord는 Lookup과 같지만 일치한 줄의 소유자와 줄 번호도 함께 돌려준다.
func (f *File) LookupRecord(host, port, database, user string) (Record, bool) {
	want := [4]string{NormalizeHost(host), port, database, user}
	for i, l := range f.lines {
		if l.matches(want) {
			return f.record(i), true
		}
	}
	return Record{}, false
}

// matches는 l이 want(host, port, database, user)에 대해 libpq가 사용할
// 항목 줄인지 알려 준다.
func (l *line) matches(want [4]string) bool {
	if l.kind != kindEntry {
		return false
	}
	got := [4]string{l.entry.Host, l.entry.Port, l.entry.Database, l.entry.User}
	for k := range got {
		if !l.wild[k] && got[k] != want[k] {
			return false
		}
	}
	return true
}

// marker는 name에 대한 첫 표식의 인덱스를 돌려준다. 없으면 -1이다.
func (f *File) marker(name string) int {
	for i, l := range f.lines {
		if l.owner == name {
			return i
		}
	}
	return -1
}

// managedEntry는 m 위치의 표식 바로 다음이 항목 줄인지 알려 준다.
func (f *File) managedEntry(m int) bool {
	return m+1 < len(f.lines) && f.lines[m+1].kind == kindEntry
}

// HasMarker는 name의 표식 주석이 있는지 알려 준다. 표식 아래에 항목 줄이 없어도 true다.
func (f *File) HasMarker(name string) bool { return f.marker(name) >= 0 }

// Managed는 서비스 name이 소유한 항목을 돌려준다.
func (f *File) Managed(name string) (Entry, bool) {
	m := f.marker(name)
	if m < 0 || !f.managedEntry(m) {
		return Entry{}, false
	}
	return f.lines[m+1].entry, true
}

// SetManaged는 서비스 name이 소유한 항목을 쓴다. 기존 항목 줄은 제자리에서
// 교체한다. 새 표식과 항목 한 쌍은 그 줄을 가리게 될 첫 와일드카드 줄(바로 위에
// 붙은 주석 포함)보다 위에 넣고, 일치하는 와일드카드 줄이 없으면 파일 끝에 붙인다.
// 소켓 host는 "localhost"로 쓴다. libpq는 빈 필드를 절대 매칭하지 않으므로
// port, database, user는 필수다.
func (f *File) SetManaged(name string, e Entry) error {
	if !nameRe.MatchString(name) {
		return ErrInvalidName
	}
	for _, v := range [...]string{e.Host, e.Port, e.Database, e.User, e.Password} {
		if strings.ContainsAny(v, "\r\n\x00") {
			return ErrInvalidValue
		}
	}
	if e.Password == "" {
		return ErrEmptyPassword
	}
	if e.Port == "" || e.Database == "" || e.User == "" {
		return ErrMissingField
	}
	e.Host = NormalizeHost(e.Host)
	text := format(e)

	if m := f.marker(name); m >= 0 {
		if f.managedEntry(m) {
			if old := f.lines[m+1]; old.text != text {
				eol := old.eol
				*old = *f.newLine(text)
				old.eol = eol
			}
			return nil
		}
		f.lines = slices.Insert(f.lines, m+1, f.newLine(text))
		return nil
	}

	at := len(f.lines)
	key := [4]string{e.Host, e.Port, e.Database, e.User}
	for i, l := range f.lines {
		if slices.Contains(l.wild[:], true) && l.matches(key) {
			at = i
			for at > 0 && f.lines[at-1].kind == kindComment {
				at--
			}
			break
		}
	}
	f.lines = slices.Insert(f.lines, at, f.newLine(MarkerPrefix+name), f.newLine(text))
	return nil
}

// RemoveManaged는 name의 표식과 그 표식이 소유한 항목을 지우고, 표식이 있었는지
// 알려 준다.
func (f *File) RemoveManaged(name string) bool {
	m := f.marker(name)
	if m < 0 {
		return false
	}
	end := m + 1
	if f.managedEntry(m) {
		end++
	}
	f.lines = slices.Delete(f.lines, m, end)
	return true
}

// RenameManaged는 표식 주석만 oldName에서 newName으로 바꾼다.
func (f *File) RenameManaged(oldName, newName string) error {
	if !nameRe.MatchString(newName) {
		return ErrInvalidName
	}
	m := f.marker(oldName)
	if m < 0 {
		return ErrNotManaged
	}
	if oldName == newName {
		return nil
	}
	if f.marker(newName) >= 0 {
		return ErrDuplicate
	}
	eol := f.lines[m].eol
	*f.lines[m] = *f.newLine(MarkerPrefix + newName)
	f.lines[m].eol = eol
	return nil
}
