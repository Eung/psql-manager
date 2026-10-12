package svcfile

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// replaceOnce replaces old with new exactly once and fails if old is absent.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.Replace(s, old, new, 1)
}

func mustParse(t *testing.T, s string) *File {
	t.Helper()
	f, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

func mustSection(t *testing.T, f *File, name string) *Section {
	t.Helper()
	s := f.Section(name)
	if s == nil {
		t.Fatalf("section %q not found", name)
	}
	return s
}

const rwBlock = "# desc: 운영 DB 쓰기\n[prod-settle-rw]\n  host = db.example.com   \nport=5432\n# 예전 포트: 6432\ndbname=settledb\nuser=writer\n\t\n"

// TestEditsKeepOtherBytes applies one edit to the sample and checks the whole
// output against the original with only the expected part replaced, for
// every line-ending variant.
func TestEditsKeepOtherBytes(t *testing.T) {
	sample := readSample(t)
	cases := []struct {
		name   string
		edit   func(t *testing.T, f *File) error
		expect func(t *testing.T, s string) string
	}{
		{
			name: "set existing key",
			edit: func(t *testing.T, f *File) error { return mustSection(t, f, "prod-settle-ro").Set("port", "6432") },
			expect: func(t *testing.T, s string) string {
				return replaceOnce(t, s, "port=5432\ndbname=settledb\nuser=readonly", "port=6432\ndbname=settledb\nuser=readonly")
			},
		},
		{
			name: "set key written with spaces",
			edit: func(t *testing.T, f *File) error {
				return mustSection(t, f, "prod-settle-rw").Set("host", "db2.example.com")
			},
			expect: func(t *testing.T, s string) string {
				return replaceOnce(t, s, "  host = db.example.com   \n", "host=db2.example.com\n")
			},
		},
		{
			name: "set same value is a no-op",
			edit: func(t *testing.T, f *File) error {
				return mustSection(t, f, "prod-settle-rw").Set("host", "db.example.com")
			},
			expect: func(t *testing.T, s string) string { return s },
		},
		{
			name: "add key after last key line",
			edit: func(t *testing.T, f *File) error {
				return mustSection(t, f, "prod-settle-rw").Set("sslmode", "require")
			},
			expect: func(t *testing.T, s string) string {
				return replaceOnce(t, s, "user=writer\n", "user=writer\nsslmode=require\n")
			},
		},
		{
			name: "add key to last section of file",
			edit: func(t *testing.T, f *File) error { return mustSection(t, f, "dev-local").Set("sslmode", "disable") },
			expect: func(t *testing.T, s string) string {
				return replaceOnce(t, s, "user=me\n", "user=me\nsslmode=disable\n")
			},
		},
		{
			name: "unset key",
			edit: func(t *testing.T, f *File) error {
				if !mustSection(t, f, "stg-settle").Unset("host") {
					return errors.New("Unset reported nothing removed")
				}
				return nil
			},
			expect: func(t *testing.T, s string) string { return replaceOnce(t, s, "host=stg.example.com\n", "") },
		},
		{
			name: "add section",
			edit: func(t *testing.T, f *File) error {
				_, err := f.AddSection("new-svc", Meta{Desc: "새 서비스", Tags: []string{"dev", " test "}},
					[]Param{{"host", "localhost"}, {"port", "5433"}})
				return err
			},
			expect: func(t *testing.T, s string) string {
				return s + "\n# desc: 새 서비스\n# tags: dev, test\n[new-svc]\nhost=localhost\nport=5433\n"
			},
		},
		{
			name:   "remove middle section keeps blank-separated comment",
			edit:   func(t *testing.T, f *File) error { return mustSection(t, f, "prod-settle-rw").Remove() },
			expect: func(t *testing.T, s string) string { return replaceOnce(t, s, rwBlock, "") },
		},
		{
			name: "remove first section keeps preamble",
			edit: func(t *testing.T, f *File) error { return mustSection(t, f, "prod-settle-ro").Remove() },
			expect: func(t *testing.T, s string) string {
				return replaceOnce(t, s, "# desc: 운영 DB 읽기 전용 (정산)\n# tags: prod, readonly, settlement\n"+
					"# 담당: 김OO / 점검: 매주 화요일\n[prod-settle-ro]\nhost=db.example.com\nport=5432\n"+
					"dbname=settledb\nuser=readonly\nsslmode=require\noptions=-c timezone=Asia/Seoul\n\n", "")
			},
		},
		{
			name: "remove last section trims separator",
			edit: func(t *testing.T, f *File) error { return mustSection(t, f, "dev-local").Remove() },
			expect: func(t *testing.T, s string) string {
				return replaceOnce(t, s, "\n\n# tags: dev\n[dev-local]\ndbname=devdb\nuser=me\n", "")
			},
		},
		{
			name: "rename changes header only",
			edit: func(t *testing.T, f *File) error {
				return mustSection(t, f, "prod-settle-ro").Rename("prod-settle-readonly")
			},
			expect: func(t *testing.T, s string) string {
				return replaceOnce(t, s, "[prod-settle-ro]\n", "[prod-settle-readonly]\n")
			},
		},
		{
			name: "replace desc",
			edit: func(t *testing.T, f *File) error { return mustSection(t, f, "prod-settle-ro").SetDesc("정산 읽기") },
			expect: func(t *testing.T, s string) string {
				return replaceOnce(t, s, "# desc: 운영 DB 읽기 전용 (정산)\n", "# desc: 정산 읽기\n")
			},
		},
		{
			name: "remove desc",
			edit: func(t *testing.T, f *File) error { return mustSection(t, f, "prod-settle-ro").SetDesc("") },
			expect: func(t *testing.T, s string) string {
				return replaceOnce(t, s, "# desc: 운영 DB 읽기 전용 (정산)\n", "")
			},
		},
		{
			name: "insert desc above header",
			edit: func(t *testing.T, f *File) error { return mustSection(t, f, "stg-settle").SetDesc("스테이징") },
			expect: func(t *testing.T, s string) string {
				return replaceOnce(t, s, "\t\n[stg-settle]\n", "\t\n# desc: 스테이징\n[stg-settle]\n")
			},
		},
		{
			name: "insert desc above existing tags",
			edit: func(t *testing.T, f *File) error { return mustSection(t, f, "dev-local").SetDesc("로컬") },
			expect: func(t *testing.T, s string) string {
				return replaceOnce(t, s, "# tags: dev\n", "# desc: 로컬\n# tags: dev\n")
			},
		},
		{
			name: "insert tags below desc",
			edit: func(t *testing.T, f *File) error {
				return mustSection(t, f, "prod-settle-rw").SetTags([]string{"prod", "rw"})
			},
			expect: func(t *testing.T, s string) string {
				return replaceOnce(t, s, "# desc: 운영 DB 쓰기\n", "# desc: 운영 DB 쓰기\n# tags: prod, rw\n")
			},
		},
		{
			name: "same tags is a no-op",
			edit: func(t *testing.T, f *File) error {
				return mustSection(t, f, "prod-settle-ro").SetTags([]string{"prod", "readonly", "settlement"})
			},
			expect: func(t *testing.T, s string) string { return s },
		},
		{
			name:   "remove tags",
			edit:   func(t *testing.T, f *File) error { return mustSection(t, f, "dev-local").SetTags(nil) },
			expect: func(t *testing.T, s string) string { return replaceOnce(t, s, "# tags: dev\n", "") },
		},
	}
	for _, c := range cases {
		for _, v := range variants {
			t.Run(c.name+"/"+v.name, func(t *testing.T) {
				f := mustParse(t, v.apply(sample))
				if err := c.edit(t, f); err != nil {
					t.Fatalf("edit: %v", err)
				}
				want := v.apply(c.expect(t, sample))
				if got := string(f.Bytes()); got != want {
					t.Errorf("unexpected output\n got %q\nwant %q", got, want)
				}
			})
		}
	}
}

func TestAddThenRemoveRestoresOriginal(t *testing.T) {
	sample := readSample(t)
	inputs := []string{"", "# 머리말만\n", "[a]\nhost=x"}
	for _, v := range variants {
		inputs = append(inputs, v.apply(sample))
	}
	for _, in := range inputs {
		f := mustParse(t, in)
		s, err := f.AddSection("tmp", Meta{Desc: "d", Tags: []string{"t"}}, []Param{{"host", "h"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Remove(); err != nil {
			t.Fatal(err)
		}
		if got := string(f.Bytes()); got != in {
			t.Errorf("add+remove changed file\n got %q\nwant %q", got, in)
		}
	}
}

func TestAddSectionToEmptyFile(t *testing.T) {
	f := mustParse(t, "")
	if _, err := f.AddSection("a", Meta{}, []Param{{"host", "x"}}); err != nil {
		t.Fatal(err)
	}
	if got, want := string(f.Bytes()), "[a]\nhost=x\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRemoveSectionWithoutBlankBefore(t *testing.T) {
	f := mustParse(t, "[a]\nx=1\n[b]\ny=2\n\n# group\n[c]\nz=3\n")
	if err := mustSection(t, f, "b").Remove(); err != nil {
		t.Fatal(err)
	}
	if got, want := string(f.Bytes()), "[a]\nx=1\n\n# group\n[c]\nz=3\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadModel(t *testing.T) {
	f := mustParse(t, readSample(t))
	var names []string
	for _, s := range f.Sections() {
		names = append(names, s.Name())
	}
	if want := []string{"prod-settle-ro", "prod-settle-rw", "stg-settle", "dev-local"}; !slices.Equal(names, want) {
		t.Errorf("sections = %v, want %v", names, want)
	}

	ro := mustSection(t, f, "prod-settle-ro")
	m := ro.Meta()
	if m.Desc != "운영 DB 읽기 전용 (정산)" || !slices.Equal(m.Tags, []string{"prod", "readonly", "settlement"}) {
		t.Errorf("meta = %+v", m)
	}
	wantParams := []Param{
		{"host", "db.example.com"}, {"port", "5432"}, {"dbname", "settledb"},
		{"user", "readonly"}, {"sslmode", "require"}, {"options", "-c timezone=Asia/Seoul"},
	}
	if got := ro.Params(); !slices.Equal(got, wantParams) {
		t.Errorf("params = %v", got)
	}
	if ro.Line() != 9 {
		t.Errorf("Line() = %d, want 9", ro.Line())
	}

	rw := mustSection(t, f, "prod-settle-rw")
	if v, ok := rw.Get("host"); !ok || v != "db.example.com" {
		t.Errorf("Get(host) = %q, %v", v, ok)
	}
	if _, ok := rw.Get("sslmode"); ok {
		t.Error("Get(sslmode) found a missing key")
	}
	if m := rw.Meta(); m.Desc != "운영 DB 쓰기" || m.Tags != nil {
		t.Errorf("rw meta = %+v", m)
	}
	// 빈 줄로 분리된 주석은 메타로 읽지 않는다.
	if m := mustSection(t, f, "stg-settle").Meta(); m.Desc != "" || m.Tags != nil {
		t.Errorf("stg meta = %+v", m)
	}
	if f.Section("missing") != nil {
		t.Error("Section(missing) != nil")
	}
}

func TestParseErrorsKeepLines(t *testing.T) {
	in := "host=x\n[a]\nbogus-secret\n[b\n=v\n[]\n[c]\nk=v\n"
	f, err := Parse([]byte(in))
	var pe ParseErrors
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want ParseErrors", err)
	}
	var lines []int
	for _, e := range pe {
		lines = append(lines, e.Line)
	}
	if want := []int{1, 3, 4, 5, 6}; !slices.Equal(lines, want) {
		t.Errorf("error lines = %v, want %v", lines, want)
	}
	if strings.Contains(err.Error(), "bogus-secret") {
		t.Errorf("error leaks line content: %v", err)
	}
	if !strings.Contains(err.Error(), "line 3:") {
		t.Errorf("error lacks line number: %v", err)
	}
	if string(f.Bytes()) != in {
		t.Error("invalid lines were not preserved")
	}
	if got := len(f.Sections()); got != 2 {
		t.Errorf("sections = %d, want 2", got)
	}
	// 섹션 안의 이해하지 못한 줄도 섹션을 지우면 함께 지워진다.
	if err := mustSection(t, f, "a").Remove(); err != nil {
		t.Fatal(err)
	}
	if got, want := string(f.Bytes()), "host=x\n[c]\nk=v\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDuplicateSections(t *testing.T) {
	f := mustParse(t, "[a]\nx=1\n\n[a]\nx=2\n")
	if len(f.Sections()) != 2 {
		t.Fatalf("sections = %d, want 2", len(f.Sections()))
	}
	if v, _ := mustSection(t, f, "a").Get("x"); v != "1" {
		t.Errorf("first section must win, got x=%s", v)
	}
	if _, err := f.AddSection("a", Meta{}, nil); !errors.Is(err, ErrDuplicateSection) {
		t.Errorf("AddSection duplicate: err = %v", err)
	}
}

func TestDuplicateKeyFirstWins(t *testing.T) {
	f := mustParse(t, "[a]\nx=1\nx=2\n")
	s := mustSection(t, f, "a")
	if err := s.Set("x", "3"); err != nil {
		t.Fatal(err)
	}
	if got, want := string(f.Bytes()), "[a]\nx=3\nx=2\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !s.Unset("x") || string(f.Bytes()) != "[a]\n" {
		t.Errorf("Unset should remove every line for the key, got %q", f.Bytes())
	}
	if s.Unset("x") {
		t.Error("Unset of missing key reported true")
	}
	if err := s.Set("host", "h"); err != nil {
		t.Fatal(err)
	}
	if got, want := string(f.Bytes()), "[a]\nhost=h\n"; got != want {
		t.Errorf("Set into empty section: got %q, want %q", got, want)
	}
}

func TestValidation(t *testing.T) {
	f := mustParse(t, "[a]\nhost=x\n\n[b]\n")
	a := mustSection(t, f, "a")
	checks := []struct {
		name string
		err  error
		want error
	}{
		{"bad name", func() error { _, err := f.AddSection("bad name", Meta{}, nil); return err }(), ErrInvalidName},
		{"bracket name", func() error { _, err := f.AddSection("a]", Meta{}, nil); return err }(), ErrInvalidName},
		{"password key", a.Set("password", "x"), ErrPasswordKey},
		{"password key upper", a.Set("PASSWORD", "x"), ErrPasswordKey},
		{"password param on add", func() error {
			_, err := f.AddSection("c", Meta{}, []Param{{"password", "x"}})
			return err
		}(), ErrPasswordKey},
		{"bad key", a.Set("bad key", "x"), ErrInvalidKey},
		{"newline value", a.Set("host", "a\nb"), ErrInvalidValue},
		{"cr value", a.Set("host", "a\rb"), ErrInvalidValue},
		{"newline desc", a.SetDesc("a\nb"), ErrInvalidValue},
		{"newline desc on add", func() error { _, err := f.AddSection("c", Meta{Desc: "a\nb"}, nil); return err }(), ErrInvalidValue},
		{"comma tag", a.SetTags([]string{"a,b"}), ErrInvalidValue},
		{"comma tag on add", func() error {
			_, err := f.AddSection("c", Meta{Tags: []string{"a,b"}}, nil)
			return err
		}(), ErrInvalidValue},
		{"newline tag", a.SetTags([]string{"a\n"}), ErrInvalidValue},
		{"rename invalid", a.Rename("a b"), ErrInvalidName},
		{"rename to existing", a.Rename("b"), ErrDuplicateSection},
		{"rename to self", a.Rename("a"), nil},
	}
	for _, c := range checks {
		if !errors.Is(c.err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, c.err, c.want)
		}
	}
	if got, want := string(f.Bytes()), "[a]\nhost=x\n\n[b]\n"; got != want {
		t.Errorf("failed edits changed the file: %q", got)
	}
}

func TestRemovedSectionHandle(t *testing.T) {
	f := mustParse(t, "[a]\nhost=x\n")
	s := mustSection(t, f, "a")
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(); !errors.Is(err, ErrNoSection) {
		t.Errorf("Remove twice: %v", err)
	}
	if err := s.Set("k", "v"); !errors.Is(err, ErrNoSection) {
		t.Errorf("Set: %v", err)
	}
	if err := s.SetDesc("d"); !errors.Is(err, ErrNoSection) {
		t.Errorf("SetDesc: %v", err)
	}
	if err := s.Rename("b"); !errors.Is(err, ErrNoSection) {
		t.Errorf("Rename: %v", err)
	}
	if s.Params() != nil || s.Unset("host") || s.Line() != 0 || s.Meta().Desc != "" {
		t.Error("removed section still exposes data")
	}
	if s.Name() != "a" {
		t.Errorf("Name() = %q", s.Name())
	}
}

func TestHandleSurvivesEdits(t *testing.T) {
	f := mustParse(t, "[a]\nx=1\n\n[b]\ny=2\n")
	b := mustSection(t, f, "b")
	if err := mustSection(t, f, "a").SetDesc("앞에 줄이 늘어난다"); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("y", "3"); err != nil {
		t.Fatal(err)
	}
	if got, want := string(f.Bytes()), "# desc: 앞에 줄이 늘어난다\n[a]\nx=1\n\n[b]\ny=3\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMetaCommentForms(t *testing.T) {
	f := mustParse(t, "#desc:붙여 쓴 설명\n;tags: x\n#   tags:  a ,, b  \n[a]\n")
	m := mustSection(t, f, "a").Meta()
	if m.Desc != "붙여 쓴 설명" || !slices.Equal(m.Tags, []string{"a", "b"}) {
		t.Errorf("meta = %+v", m)
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"a", "prod-settle.ro_1", "A.B-C"} {
		if !ValidName(n) {
			t.Errorf("ValidName(%q) = false", n)
		}
	}
	for _, n := range []string{"", "a b", "[a]", "a\n", "서비스", "a/b"} {
		if ValidName(n) {
			t.Errorf("ValidName(%q) = true", n)
		}
	}
}

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PGSERVICEFILE", "")
	if p, err := DefaultPath(); err != nil || p != home+"/.pg_service.conf" {
		t.Errorf("DefaultPath() = %q, %v", p, err)
	}
	t.Setenv("PGSERVICEFILE", "/custom/service.conf")
	if p, err := DefaultPath(); err != nil || p != "/custom/service.conf" {
		t.Errorf("DefaultPath() with env = %q, %v", p, err)
	}
}
