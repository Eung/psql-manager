// Package svcfile은 pg_service.conf를 줄 단위로 읽고 편집해, 호출하는 쪽이
// 건드리지 않은 줄은 바이트 단위로 그대로 되쓴다.
package svcfile

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

// File과 Section 메서드가 돌려주는 오류.
var (
	ErrInvalidName      = errors.New("svcfile: invalid service name")
	ErrDuplicateSection = errors.New("svcfile: service already exists")
	ErrNoSection        = errors.New("svcfile: service not found")
	ErrInvalidKey       = errors.New("svcfile: invalid parameter key")
	ErrInvalidValue     = errors.New("svcfile: value must not contain line breaks")
	ErrPasswordKey      = errors.New("svcfile: password must not be stored in the service file")
)

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	keyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ValidName은 name이 허용되는 서비스명([A-Za-z0-9._-]+)인지 알려 준다.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// DefaultPath는 $PGSERVICEFILE을, 설정되어 있지 않으면 ~/.pg_service.conf를 돌려준다.
func DefaultPath() (string, error) {
	if p := os.Getenv("PGSERVICEFILE"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("svcfile: locate home directory: %w", err)
	}
	return filepath.Join(home, ".pg_service.conf"), nil
}

// Param은 섹션의 key=value 한 줄이다.
type Param struct {
	Key   string
	Value string
}

// Meta는 섹션 헤더 바로 위 "# desc:", "# tags:" 주석에 담긴 관리용 메타데이터다.
type Meta struct {
	Desc string
	Tags []string
}

// ParseError는 Parse가 이해하지 못한 줄 하나를 나타낸다. 줄 내용은 절대 담지 않는다.
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
	return "svcfile: " + strings.Join(msgs, "; ")
}

type kind int

const (
	kindBlank kind = iota
	kindComment
	kindHeader
	kindParam
	kindInvalid
)

// line은 물리적인 한 줄이다. text에는 줄바꿈 문자가 없고, 줄바꿈은 eol에 둔다.
type line struct {
	text  string
	eol   string
	kind  kind
	name  string // kindHeader일 때
	key   string // kindParam일 때
	value string // kindParam일 때
}

// classify는 섹션 문맥 없이 text를 분류한다. kindInvalid이면 reason에 이유를 담는다.
func classify(text, eol string) (l *line, reason string) {
	l = &line{text: text, eol: eol}
	t := strings.TrimSpace(text)
	switch {
	case t == "":
		l.kind = kindBlank
	case t[0] == '#' || t[0] == ';':
		l.kind = kindComment
	case t[0] == '[':
		if len(t) < 2 || t[len(t)-1] != ']' {
			l.kind = kindInvalid
			return l, "unterminated section header"
		}
		if t == "[]" {
			l.kind = kindInvalid
			return l, "empty section name"
		}
		l.kind, l.name = kindHeader, t[1:len(t)-1]
	default:
		k, v, ok := strings.Cut(t, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			l.kind = kindInvalid
			return l, "expected key=value"
		}
		l.kind, l.key, l.value = kindParam, k, strings.TrimSpace(v)
	}
	return l, ""
}

// File은 파싱한 pg_service.conf다.
type File struct {
	lines      []*line
	eol        string // 새로 쓰는 줄에 붙일 줄바꿈
	noFinalEOL bool   // 읽을 때 마지막 줄에 줄바꿈이 없었는지
}

// Parse는 data를 줄 단위로 나눠 분류한다. 항상 사용 가능한 File을 돌려주며,
// 이해하지 못한 줄은 원문 그대로 보존하고 ParseErrors로 한꺼번에 알린다.
func Parse(data []byte) (*File, error) {
	texts, eols, eol, noFinal := splitLines(data)
	f := &File{eol: eol, noFinalEOL: noFinal}
	var errs ParseErrors
	inSection := false
	for i := range texts {
		l, reason := classify(texts[i], eols[i])
		switch l.kind {
		case kindHeader:
			inSection = true
		case kindParam:
			if !inSection {
				l.kind, reason = kindInvalid, "parameter outside of a section"
			}
		}
		if l.kind == kindInvalid {
			errs = append(errs, &ParseError{Line: i + 1, Msg: reason})
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

// span은 섹션 하나의 위치다. lines[lead:header]는 헤더 앞 주석,
// lines[header+1:end]는 본문이다.
type span struct{ lead, header, end int }

func (f *File) spans() []span {
	var sp []span
	for i, l := range f.lines {
		if l.kind != kindHeader {
			continue
		}
		lead := i
		for lead > 0 && f.lines[lead-1].kind == kindComment {
			lead--
		}
		if n := len(sp); n > 0 {
			sp[n-1].end = lead
		}
		sp = append(sp, span{lead: lead, header: i})
	}
	if n := len(sp); n > 0 {
		sp[n-1].end = len(f.lines)
	}
	return sp
}

func (f *File) newLine(text string) *line {
	l, _ := classify(text, f.eol)
	return l
}

func (f *File) insert(at int, ls ...*line) {
	f.lines = slices.Insert(f.lines, at, ls...)
}

func (f *File) remove(from, to int) {
	f.lines = slices.Delete(f.lines, from, to)
}

// Sections는 중복을 포함한 모든 섹션을 파일 순서대로 돌려준다.
func (f *File) Sections() []*Section {
	sp := f.spans()
	out := make([]*Section, len(sp))
	for i, s := range sp {
		out[i] = &Section{f: f, hdr: f.lines[s.header]}
	}
	return out
}

// Section은 libpq처럼 이름이 name인 첫 섹션을 돌려준다. 없으면 nil이다.
func (f *File) Section(name string) *Section {
	for _, s := range f.spans() {
		if f.lines[s.header].name == name {
			return &Section{f: f, hdr: f.lines[s.header]}
		}
	}
	return nil
}

// AddSection은 파일 끝에 빈 줄 하나를 두고 새 섹션을 추가한다. 헤더 위에는
// "# desc:", "# tags:" 주석을 쓴다.
func (f *File) AddSection(name string, meta Meta, params []Param) (*Section, error) {
	if !ValidName(name) {
		return nil, ErrInvalidName
	}
	if f.Section(name) != nil {
		return nil, ErrDuplicateSection
	}
	if err := validateText(meta.Desc); err != nil {
		return nil, err
	}
	tags, err := formatTags(meta.Tags)
	if err != nil {
		return nil, err
	}
	for _, p := range params {
		if err := validateParam(p.Key, p.Value); err != nil {
			return nil, err
		}
	}

	var add []*line
	if n := len(f.lines); n > 0 && f.lines[n-1].kind != kindBlank {
		add = append(add, f.newLine(""))
	}
	if meta.Desc != "" {
		add = append(add, f.newLine("# desc: "+meta.Desc))
	}
	if tags != "" {
		add = append(add, f.newLine("# tags: "+tags))
	}
	hdr := f.newLine("[" + name + "]")
	add = append(add, hdr)
	for _, p := range params {
		add = append(add, f.newLine(p.Key+"="+p.Value))
	}
	f.lines = append(f.lines, add...)
	return &Section{f: f, hdr: hdr}, nil
}

// Section은 File의 섹션 하나를 가리키는 핸들이다. 파일을 편집해도 그 섹션이
// 지워지기 전까지는 계속 유효하다.
type Section struct {
	f   *File
	hdr *line
}

func (s *Section) span() (span, error) {
	for _, sp := range s.f.spans() {
		if s.f.lines[sp.header] == s.hdr {
			return sp, nil
		}
	}
	return span{}, ErrNoSection
}

// Name은 섹션 이름을 돌려준다.
func (s *Section) Name() string { return s.hdr.name }

// Line은 섹션 헤더의 줄 번호(1부터 시작)를 돌려준다. 섹션이 지워졌으면 0이다.
func (s *Section) Line() int {
	sp, err := s.span()
	if err != nil {
		return 0
	}
	return sp.header + 1
}

// Params는 섹션의 key=value 줄을 파일 순서대로 돌려준다.
func (s *Section) Params() []Param {
	sp, err := s.span()
	if err != nil {
		return nil
	}
	var out []Param
	for _, l := range s.f.lines[sp.header+1 : sp.end] {
		if l.kind == kindParam {
			out = append(out, Param{Key: l.key, Value: l.value})
		}
	}
	return out
}

// Get은 libpq처럼 key에 해당하는 첫 줄의 값을 돌려준다.
func (s *Section) Get(key string) (string, bool) {
	for _, p := range s.Params() {
		if p.Key == key {
			return p.Value, true
		}
	}
	return "", false
}

// Set은 key의 첫 줄을 교체하거나, 없으면 섹션의 마지막 키 줄 뒤에 추가한다.
// 현재 값과 같으면 줄을 건드리지 않는다.
func (s *Section) Set(key, value string) error {
	if err := validateParam(key, value); err != nil {
		return err
	}
	sp, err := s.span()
	if err != nil {
		return err
	}
	last := sp.header
	for i := sp.header + 1; i < sp.end; i++ {
		l := s.f.lines[i]
		if l.kind != kindParam {
			continue
		}
		if l.key == key {
			if l.value != value {
				eol := l.eol
				*l = *s.f.newLine(key + "=" + value)
				l.eol = eol
			}
			return nil
		}
		last = i
	}
	s.f.insert(last+1, s.f.newLine(key+"="+value))
	return nil
}

// Unset은 key에 해당하는 줄을 모두 지우고, 하나라도 있었는지 알려 준다.
func (s *Section) Unset(key string) bool {
	sp, err := s.span()
	if err != nil {
		return false
	}
	removed := false
	for i := sp.end - 1; i > sp.header; i-- {
		if l := s.f.lines[i]; l.kind == kindParam && l.key == key {
			s.f.remove(i, i+1)
			removed = true
		}
	}
	return removed
}

// Rename은 헤더 줄만 [name]으로 바꾼다.
func (s *Section) Rename(name string) error {
	if !ValidName(name) {
		return ErrInvalidName
	}
	if _, err := s.span(); err != nil {
		return err
	}
	if name == s.hdr.name {
		return nil
	}
	if s.f.Section(name) != nil {
		return ErrDuplicateSection
	}
	eol := s.hdr.eol
	*s.hdr = *s.f.newLine("[" + name + "]")
	s.hdr.eol = eol
	return nil
}

// Remove는 섹션 헤더, 본문, 헤더 바로 위 주석을 지운다. 헤더와 빈 줄로 분리된
// 주석과, 본문 뒤에 빈 줄로 분리되어 이어지는 주석은 남긴다.
func (s *Section) Remove() error {
	sp, err := s.span()
	if err != nil {
		return err
	}
	ls := s.f.lines
	last := sp.header
	for i := sp.header + 1; i < sp.end; i++ {
		if k := ls[i].kind; k == kindParam || k == kindInvalid {
			last = i
		}
	}
	// 마지막 키 줄에 빈 줄 없이 붙은 주석까지는 이 섹션의 것으로 본다.
	cut := last + 1
	for cut < sp.end && ls[cut].kind == kindComment {
		cut++
	}
	from := sp.lead
	// 앞이 이미 빈 줄(또는 파일 시작)이면 뒤따르는 빈 줄을 함께 지워 빈 줄이 겹치지 않게 한다.
	if from == 0 || ls[from-1].kind == kindBlank {
		for cut < len(ls) && ls[cut].kind == kindBlank {
			cut++
		}
	}
	// 파일 끝까지 지웠다면 섹션 구분용으로 남은 앞쪽 빈 줄도 정리한다.
	if cut == len(ls) {
		for from > 0 && ls[from-1].kind == kindBlank {
			from--
		}
	}
	s.f.remove(from, cut)
	return nil
}

// Meta는 헤더 위 desc, tags 주석을 읽어 돌려준다.
func (s *Section) Meta() Meta {
	var m Meta
	sp, err := s.span()
	if err != nil {
		return m
	}
	if i := s.findMeta(sp, "desc"); i >= 0 {
		_, m.Desc = metaLine(s.f.lines[i].text)
	}
	if i := s.findMeta(sp, "tags"); i >= 0 {
		_, v := metaLine(s.f.lines[i].text)
		m.Tags = splitTags(v)
	}
	return m
}

// SetDesc는 "# desc:" 주석을 쓴다. 빈 값이면 주석을 지운다.
func (s *Section) SetDesc(desc string) error {
	if err := validateText(desc); err != nil {
		return err
	}
	return s.setMeta("desc", desc, func(old string) bool { return old == desc })
}

// SetTags는 "# tags:" 주석을 쓴다. 태그가 없으면 주석을 지운다.
func (s *Section) SetTags(tags []string) error {
	v, err := formatTags(tags)
	if err != nil {
		return err
	}
	return s.setMeta("tags", v, func(old string) bool { return slices.Equal(splitTags(old), splitTags(v)) })
}

// setMeta는 메타 주석 하나를 교체, 삭제, 삽입한다. 새 desc는 헤더 앞 주석의 맨
// 위에, 새 tags는 desc 바로 아래에 넣는다.
func (s *Section) setMeta(key, value string, same func(old string) bool) error {
	sp, err := s.span()
	if err != nil {
		return err
	}
	if i := s.findMeta(sp, key); i >= 0 {
		_, old := metaLine(s.f.lines[i].text)
		switch {
		case value == "":
			s.f.remove(i, i+1)
		case !same(old):
			s.f.lines[i].text = "# " + key + ": " + value
		}
		return nil
	}
	if value == "" {
		return nil
	}
	at := sp.lead
	if key == "tags" {
		if d := s.findMeta(sp, "desc"); d >= 0 {
			at = d + 1
		}
	}
	s.f.insert(at, s.f.newLine("# "+key+": "+value))
	return nil
}

func (s *Section) findMeta(sp span, key string) int {
	for i := sp.lead; i < sp.header; i++ {
		if k, _ := metaLine(s.f.lines[i].text); k == key {
			return i
		}
	}
	return -1
}

// metaLine은 "# desc: ...", "# tags: ..." 주석을 알아본다.
func metaLine(text string) (key, value string) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "#") {
		return "", ""
	}
	t = strings.TrimSpace(t[1:])
	for _, k := range [...]string{"desc", "tags"} {
		if v, ok := strings.CutPrefix(t, k+":"); ok {
			return k, strings.TrimSpace(v)
		}
	}
	return "", ""
}

func splitTags(v string) []string {
	var out []string
	for _, t := range strings.Split(v, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func formatTags(tags []string) (string, error) {
	var out []string
	for _, t := range tags {
		if strings.Contains(t, ",") {
			return "", fmt.Errorf("%w: tag must not contain a comma", ErrInvalidValue)
		}
		if err := validateText(t); err != nil {
			return "", err
		}
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, ", "), nil
}

func validateText(v string) error {
	if strings.ContainsAny(v, "\r\n\x00") {
		return ErrInvalidValue
	}
	return nil
}

func validateParam(key, value string) error {
	if strings.EqualFold(key, "password") {
		return ErrPasswordKey
	}
	if !keyRe.MatchString(key) {
		return ErrInvalidKey
	}
	return validateText(value)
}
