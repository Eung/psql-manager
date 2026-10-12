// Package pgpass reads and edits .pgpass files. Lines without a
// "# psql-manager:<name>" marker are never modified.
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

// MarkerPrefix starts the comment that marks the next line as owned by a service.
const MarkerPrefix = "# psql-manager:"

// Errors returned by File methods.
var (
	ErrInvalidName   = errors.New("pgpass: invalid service name")
	ErrInvalidValue  = errors.New("pgpass: value must not contain line breaks")
	ErrEmptyPassword = errors.New("pgpass: password is empty")
	ErrNotManaged    = errors.New("pgpass: no managed entry for service")
	ErrDuplicate     = errors.New("pgpass: service already has a managed entry")
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// DefaultPath returns $PGPASSFILE, or ~/.pgpass when it is unset.
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

// NormalizeHost maps an empty host (socket connection) to "localhost", the
// name libpq looks up in .pgpass.
func NormalizeHost(host string) string {
	if host == "" {
		return "localhost"
	}
	return host
}

// Entry is one hostname:port:database:username:password line, unescaped.
type Entry struct {
	Host     string
	Port     string
	Database string
	User     string
	Password string
}

// String formats the entry with the password masked.
func (e Entry) String() string {
	return fmt.Sprintf("%s:%s:%s:%s:%s", e.Host, e.Port, e.Database, e.User, mask(e.Password))
}

// GoString formats the entry for %#v with the password masked.
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

// Record is a parsed entry line together with its ownership.
type Record struct {
	Entry
	Owner    string // service name from the marker above, "" when unmanaged
	Line     int    // 1-based
	Wildcard bool   // any of the four key fields is "*"
}

// ParseError describes one line Parse could not understand. It never
// contains the line's content, which may hold a password.
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
	owner string // marker comment: service name
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

// splitFields splits at unescaped colons and unescapes "\x" to "x". A field
// is a wildcard only when its raw text is exactly "*".
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

// Escape escapes ':' and '\' for use in a .pgpass field.
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

// Unescape reverses Escape the way libpq reads a field: "\x" becomes "x".
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

// keyField escapes a host, port, database or user value. A literal "*" is
// escaped so it is not read as a wildcard.
func keyField(s string) string {
	if s == "*" {
		return `\*`
	}
	return Escape(s)
}

// format renders e as one .pgpass line. A leading '#' is escaped so the line
// is not read as a comment.
func format(e Entry) string {
	host := keyField(e.Host)
	if strings.HasPrefix(host, "#") {
		host = `\` + host
	}
	return strings.Join([]string{host, keyField(e.Port), keyField(e.Database), keyField(e.User), Escape(e.Password)}, ":")
}

// File is a parsed .pgpass.
type File struct {
	lines      []*line
	eol        string
	noFinalEOL bool
}

// Parse splits data into lines. It always returns a usable File; malformed
// lines are kept verbatim and reported together as ParseErrors.
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

func (f *File) newLine(text string) *line { return classify(text, f.eol) }

// owner returns the service name marking lines[i], if any.
func (f *File) owner(i int) string {
	if i > 0 {
		return f.lines[i-1].owner
	}
	return ""
}

// Records returns every entry line in file order.
func (f *File) Records() []Record {
	var out []Record
	for i, l := range f.lines {
		if l.kind != kindEntry {
			continue
		}
		out = append(out, Record{
			Entry:    l.entry,
			Owner:    f.owner(i),
			Line:     i + 1,
			Wildcard: slices.Contains(l.wild[:], true),
		})
	}
	return out
}

// Lookup returns the first entry matching the connection parameters, as libpq
// does. An empty host is looked up as "localhost".
func (f *File) Lookup(host, port, database, user string) (Entry, bool) {
	want := [4]string{NormalizeHost(host), port, database, user}
	for _, l := range f.lines {
		if l.kind != kindEntry {
			continue
		}
		got := [4]string{l.entry.Host, l.entry.Port, l.entry.Database, l.entry.User}
		match := true
		for k := range got {
			if !l.wild[k] && got[k] != want[k] {
				match = false
				break
			}
		}
		if match {
			return l.entry, true
		}
	}
	return Entry{}, false
}

// marker returns the index of the first marker for name, or -1.
func (f *File) marker(name string) int {
	for i, l := range f.lines {
		if l.owner == name {
			return i
		}
	}
	return -1
}

// managedEntry reports whether the marker at m is followed by an entry line.
func (f *File) managedEntry(m int) bool {
	return m+1 < len(f.lines) && f.lines[m+1].kind == kindEntry
}

// Managed returns the entry owned by service name.
func (f *File) Managed(name string) (Entry, bool) {
	m := f.marker(name)
	if m < 0 || !f.managedEntry(m) {
		return Entry{}, false
	}
	return f.lines[m+1].entry, true
}

// SetManaged writes the entry owned by service name. An existing entry line
// is replaced in place; a new marker and entry pair is inserted above the
// first wildcard line (and the comments attached to it), or appended.
// An empty host is written as "localhost".
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
	for i, l := range f.lines {
		if l.kind == kindEntry && slices.Contains(l.wild[:], true) {
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

// RemoveManaged deletes the marker for name and the entry it owns, and
// reports whether a marker existed.
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

// RenameManaged rewrites only the marker comment from oldName to newName.
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
