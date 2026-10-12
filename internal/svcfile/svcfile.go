// Package svcfile reads and edits pg_service.conf files line by line so that
// every line the caller does not touch is written back byte for byte.
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

// Errors returned by File and Section methods.
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

// ValidName reports whether name is an acceptable service name ([A-Za-z0-9._-]+).
func ValidName(name string) bool { return nameRe.MatchString(name) }

// DefaultPath returns $PGSERVICEFILE, or ~/.pg_service.conf when it is unset.
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

// Param is one key=value line of a section.
type Param struct {
	Key   string
	Value string
}

// Meta is the management metadata kept in "# desc:" and "# tags:" comments
// directly above a section header.
type Meta struct {
	Desc string
	Tags []string
}

// ParseError describes one line Parse could not understand. It never
// contains the line's content.
type ParseError struct {
	Line int // 1-based
	Msg  string
}

// Error implements error.
func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// ParseErrors collects every line Parse could not understand.
type ParseErrors []*ParseError

// Error implements error.
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

// line is one physical line. text excludes the terminator, eol holds it.
type line struct {
	text  string
	eol   string
	kind  kind
	name  string // kindHeader
	key   string // kindParam
	value string // kindParam
}

// classify parses text without section context. reason is set for kindInvalid.
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

// File is a parsed pg_service.conf.
type File struct {
	lines      []*line
	eol        string // terminator for newly written lines
	noFinalEOL bool   // the last line had no terminator when read
}

// Parse splits data into lines and classifies them. It always returns a
// usable File; lines it cannot understand are kept verbatim and reported
// together as ParseErrors.
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

// splitLines splits data keeping each line's own terminator. A final line
// without terminator gets the default one and noFinal is reported instead.
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

// Bytes serializes the file. Untouched lines come back exactly as read.
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

// span locates one section: lines[lead:header] are its leading comments and
// lines[header+1:end] its body.
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

// Sections returns every section in file order, duplicates included.
func (f *File) Sections() []*Section {
	sp := f.spans()
	out := make([]*Section, len(sp))
	for i, s := range sp {
		out[i] = &Section{f: f, hdr: f.lines[s.header]}
	}
	return out
}

// Section returns the first section called name, as libpq would use it, or nil.
func (f *File) Section(name string) *Section {
	for _, s := range f.spans() {
		if f.lines[s.header].name == name {
			return &Section{f: f, hdr: f.lines[s.header]}
		}
	}
	return nil
}

// AddSection appends a new section at the end of the file, separated by one
// blank line, with "# desc:" and "# tags:" comments above its header.
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

// Section is a handle to one section of a File. It stays valid across edits
// to the file until the section itself is removed.
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

// Name returns the section name.
func (s *Section) Name() string { return s.hdr.name }

// Line returns the 1-based line number of the section header, or 0 if the
// section has been removed.
func (s *Section) Line() int {
	sp, err := s.span()
	if err != nil {
		return 0
	}
	return sp.header + 1
}

// Params returns the key=value lines of the section in file order.
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

// Get returns the value of the first key line, as libpq would use it.
func (s *Section) Get(key string) (string, bool) {
	for _, p := range s.Params() {
		if p.Key == key {
			return p.Value, true
		}
	}
	return "", false
}

// Set replaces the first line for key, or adds key after the last key line
// of the section. Setting the current value leaves the line untouched.
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

// Unset removes every line for key and reports whether any existed.
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

// Rename changes only the header line to [name].
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

// Remove deletes the section header, its body and the comments directly
// above it. Comments separated from the header by a blank line are kept, as
// are blank-separated comments trailing the body.
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

// Meta returns the desc and tags comments above the header.
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

// SetDesc writes the "# desc:" comment. An empty desc removes it.
func (s *Section) SetDesc(desc string) error {
	if err := validateText(desc); err != nil {
		return err
	}
	return s.setMeta("desc", desc, func(old string) bool { return old == desc })
}

// SetTags writes the "# tags:" comment. No tags removes it.
func (s *Section) SetTags(tags []string) error {
	v, err := formatTags(tags)
	if err != nil {
		return err
	}
	return s.setMeta("tags", v, func(old string) bool { return slices.Equal(splitTags(old), splitTags(v)) })
}

// setMeta replaces, removes or inserts one meta comment. A new desc goes to
// the top of the leading comments, a new tags line right below desc.
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

// metaLine recognizes "# desc: ..." and "# tags: ..." comments.
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
