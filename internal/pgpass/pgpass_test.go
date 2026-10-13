package pgpass

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func mustParse(t *testing.T, s string) *File {
	t.Helper()
	f, err := Parse([]byte(s))
	if err != nil {
		var pe ParseErrors
		if !errors.As(err, &pe) {
			t.Fatalf("Parse: %v", err)
		}
	}
	return f
}

func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.Replace(s, old, new, 1)
}

// assertBytes는 비밀번호가 들어 있을 수 있는 내용을 출력하지 않고 비교한다.
func assertBytes(t *testing.T, f *File, want string) {
	t.Helper()
	got := string(f.Bytes())
	if got == want {
		return
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gl) || i < len(wl); i++ {
		if i >= len(gl) || i >= len(wl) || gl[i] != wl[i] {
			t.Errorf("output differs from line %d (got %d lines, want %d)", i+1, len(gl), len(wl))
			return
		}
	}
}

func TestEscapeRoundTrip(t *testing.T) {
	cases := map[string]string{
		"":      "",
		"plain": "plain",
		"a:b":   `a\:b`,
		`a\b`:   `a\\b`,
		`\:`:    `\\\:`,
		"#hash": "#hash",
		`::\\`:  `\:\:\\\\`,
		"한글:비번": `한글\:비번`,
	}
	for in, want := range cases {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
		if got := Unescape(Escape(in)); got != in {
			t.Errorf("Unescape(Escape(%q)) = %q", in, got)
		}
	}
	// libpq처럼 알 수 없는 이스케이프는 다음 글자로, 끝의 외톨이 \는 그대로 읽는다.
	if got := Unescape(`a\xb\`); got != `axb\` {
		t.Errorf("Unescape = %q", got)
	}
}

func TestParseRecords(t *testing.T) {
	f, err := Parse([]byte(sample))
	var pe ParseErrors
	if !errors.As(err, &pe) || len(pe) != 1 || pe[0].Line != 7 {
		t.Fatalf("err = %v, want one error at line 7", err)
	}
	if strings.Contains(err.Error(), "broken-line") {
		t.Errorf("error leaks line content: %v", err)
	}
	recs := f.Records()
	want := []Record{
		{Entry: Entry{"localhost", "5432", "*", "postgres", "local:pw"}, Line: 2, Wildcard: true},
		{Entry: Entry{"db.example.com", "5432", "settledb", "readonly", "s3cr3t:pw"}, Owner: "prod-settle-ro", Line: 5},
		{Entry: Entry{"legacy.example.com", "5432", "legacy", "app", `old\pw`}, Line: 6},
		{Entry: Entry{"*", "*", "*", "*", "fallback"}, Line: 10, Wildcard: true},
	}
	if !slices.Equal(recs, want) {
		t.Errorf("records = %v\nwant %v", recs, want)
	}
}

func TestLookup(t *testing.T) {
	f := mustParse(t, sample+"\\*:5432:literal:u:lit\n")
	cases := []struct {
		host, port, db, user string
		want                 string
	}{
		{"localhost", "5432", "anydb", "postgres", "local:pw"},
		{"", "5432", "anydb", "postgres", "local:pw"}, // 빈 host는 localhost
		{"db.example.com", "5432", "settledb", "readonly", "s3cr3t:pw"},
		{"legacy.example.com", "5432", "legacy", "app", `old\pw`},
		{"other", "1", "x", "y", "fallback"},
		{"*", "5432", "literal", "u", "fallback"}, // 와일드카드 줄이 위에 있어 가린다
	}
	for _, c := range cases {
		e, ok := f.Lookup(c.host, c.port, c.db, c.user)
		if !ok || e.Password != c.want {
			t.Errorf("Lookup(%q,%q,%q,%q) did not return the expected entry", c.host, c.port, c.db, c.user)
		}
	}
	// \*는 와일드카드가 아니다.
	lit := mustParse(t, "\\*:5432:literal:u:lit\n")
	if _, ok := lit.Lookup("other", "5432", "literal", "u"); ok {
		t.Error(`escaped \* matched as wildcard`)
	}
	if e, ok := lit.Lookup("*", "5432", "literal", "u"); !ok || e.Password != "lit" {
		t.Error(`escaped \* did not match literal "*"`)
	}
	if _, ok := mustParse(t, "a:b:c:d:e\n").Lookup("a", "b", "c", "x"); ok {
		t.Error("Lookup matched a different user")
	}
}

func TestLookupRecord(t *testing.T) {
	f := mustParse(t, sample)
	r, ok := f.LookupRecord("db.example.com", "5432", "settledb", "readonly")
	if !ok || r.Owner != "prod-settle-ro" || r.Line != 5 || r.Wildcard {
		t.Errorf("record = %v, %v", r, ok)
	}
	r, ok = f.LookupRecord("x", "1", "d", "u")
	if !ok || r.Owner != "" || r.Line != 10 || !r.Wildcard {
		t.Errorf("wildcard record = %v, %v", r, ok)
	}
	if _, ok := mustParse(t, "").LookupRecord("x", "1", "d", "u"); ok {
		t.Error("empty file matched")
	}
}

func TestManaged(t *testing.T) {
	f := mustParse(t, sample)
	e, ok := f.Managed("prod-settle-ro")
	if !ok || e.Host != "db.example.com" || e.Password != "s3cr3t:pw" {
		t.Errorf("Managed = %v, %v", e, ok)
	}
	if _, ok := f.Managed("missing"); ok {
		t.Error("Managed(missing) found an entry")
	}
}

// 새 줄은 그 줄을 가리게 될(일치하는) 첫 와일드카드 줄과, 그 바로 위에 붙은
// 주석보다 위에 들어간다. 일치하는 와일드카드 줄이 없으면 파일 끝에 붙는다.
func TestSetManagedInsertsAboveShadowingWildcard(t *testing.T) {
	newSvc := Entry{"new.example.com", "5432", "newdb", "app", `p:w\#x`}
	const newPair = "# psql-manager:new-svc\nnew.example.com:5432:newdb:app:p\\:w\\\\#x\n"
	local := Entry{"localhost", "5432", "otherdb", "postgres", "pw"}
	const localPair = "# psql-manager:new-svc\nlocalhost:5432:otherdb:postgres:pw\n"
	const other = "# 다른 호스트\nother.example.com:*:*:*:pw\n"
	cases := []struct {
		name, in string
		e        Entry
		want     func(t *testing.T, in string) string
	}{
		{"skips non-matching partial wildcard", sample, newSvc, func(t *testing.T, in string) string {
			return replaceOnce(t, in, "# 기본값\n", newPair+"# 기본값\n")
		}},
		{"above matching partial wildcard and its comment", sample, local, func(t *testing.T, in string) string {
			return localPair + in
		}},
		{"appends when no wildcard matches", other, newSvc, func(t *testing.T, in string) string {
			return in + newPair
		}},
	}
	for _, c := range cases {
		for _, v := range variants {
			t.Run(c.name+"/"+v.name, func(t *testing.T) {
				f := mustParse(t, v.apply(c.in))
				if err := f.SetManaged("new-svc", c.e); err != nil {
					t.Fatal(err)
				}
				assertBytes(t, f, v.apply(c.want(t, c.in)))
				if got, ok := f.Lookup(c.e.Host, c.e.Port, c.e.Database, c.e.User); !ok || got != c.e {
					t.Error("new entry is shadowed by another line")
				}
			})
		}
	}
}

func TestSetManagedAppendsWithoutWildcard(t *testing.T) {
	base := "# psql-manager:a\nh:5432:d:u:pw\n# 메모\nother:1:d:u:pw2\n"
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			f := mustParse(t, v.apply(base))
			if err := f.SetManaged("b", Entry{"", "5433", "db", "me", "x"}); err != nil {
				t.Fatal(err)
			}
			assertBytes(t, f, v.apply(base+"# psql-manager:b\nlocalhost:5433:db:me:x\n"))
		})
	}
}

func TestSetManagedUpdatesInPlace(t *testing.T) {
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			f := mustParse(t, v.apply(sample))
			if err := f.SetManaged("prod-settle-ro", Entry{"db2.example.com", "6432", "settledb", "readonly", "new"}); err != nil {
				t.Fatal(err)
			}
			want := replaceOnce(t, sample, "db.example.com:5432:settledb:readonly:s3cr3t\\:pw\n",
				"db2.example.com:6432:settledb:readonly:new\n")
			assertBytes(t, f, v.apply(want))
		})
	}
}

func TestSetManagedSameEntryIsNoop(t *testing.T) {
	f := mustParse(t, sample)
	if err := f.SetManaged("prod-settle-ro", Entry{"db.example.com", "5432", "settledb", "readonly", "s3cr3t:pw"}); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, f, sample)
}

func TestPasswordSpecialCharactersRoundTrip(t *testing.T) {
	passwords := []string{"a:b", `a\b`, "#start", "end#", `\:#\\::`, "*", "공백 포함 비번", `\`}
	for i, pw := range passwords {
		f := mustParse(t, "")
		name := fmt.Sprintf("svc%d", i)
		if err := f.SetManaged(name, Entry{"h:o\\st", "5432", "d:b", "u", pw}); err != nil {
			t.Fatal(err)
		}
		g := mustParse(t, string(f.Bytes()))
		e, ok := g.Managed(name)
		if !ok || e.Password != pw || e.Host != `h:o\st` || e.Database != "d:b" {
			t.Errorf("password #%d did not survive a write/read round trip", i)
		}
		if got, ok := g.Lookup(`h:o\st`, "5432", "d:b", "u"); !ok || got.Password != pw {
			t.Errorf("password #%d not found by Lookup", i)
		}
	}
}

func TestLiteralWildcardAndHashAreEscaped(t *testing.T) {
	f := mustParse(t, "")
	if err := f.SetManaged("a", Entry{"#weird", "*", "db", "u", "pw"}); err != nil {
		t.Fatal(err)
	}
	if got, want := string(f.Bytes()), "# psql-manager:a\n\\#weird:\\*:db:u:pw\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	g := mustParse(t, string(f.Bytes()))
	recs := g.Records()
	if len(recs) != 1 || recs[0].Host != "#weird" || recs[0].Port != "*" || recs[0].Wildcard {
		t.Errorf("records = %v", recs)
	}
}

// 소켓 접속(빈 host 또는 /로 시작하는 소켓 디렉터리)은 libpq처럼 localhost로 쓰고 찾는다.
func TestSocketHostWrittenAsLocalhost(t *testing.T) {
	for _, host := range []string{"", "/var/run/postgresql", "/tmp"} {
		f := mustParse(t, "")
		if err := f.SetManaged("sock", Entry{host, "5432", "db", "u", "pw"}); err != nil {
			t.Fatal(err)
		}
		if got, want := string(f.Bytes()), "# psql-manager:sock\nlocalhost:5432:db:u:pw\n"; got != want {
			t.Errorf("host %q: got %q, want %q", host, got, want)
		}
		for _, q := range []string{"", "/var/run/postgresql", "localhost"} {
			if _, ok := f.Lookup(q, "5432", "db", "u"); !ok {
				t.Errorf("Lookup host %q did not match localhost", q)
			}
		}
	}
	for host, want := range map[string]string{
		"db.example.com": "db.example.com", "127.0.0.1": "127.0.0.1", "relative/dir": "relative/dir",
	} {
		if got := NormalizeHost(host); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestSetManagedRejectsBadInput(t *testing.T) {
	f := mustParse(t, sample)
	checks := []struct {
		name string
		e    Entry
		svc  string
		want error
	}{
		{"newline in password", Entry{"h", "1", "d", "u", "a\nb"}, "x", ErrInvalidValue},
		{"cr in password", Entry{"h", "1", "d", "u", "a\r"}, "x", ErrInvalidValue},
		{"newline in host", Entry{"h\n", "1", "d", "u", "pw"}, "x", ErrInvalidValue},
		{"empty password", Entry{"h", "1", "d", "u", ""}, "x", ErrEmptyPassword},
		{"bad name", Entry{"h", "1", "d", "u", "pw"}, "a b", ErrInvalidName},
		{"empty port", Entry{"h", "", "d", "u", "pw"}, "x", ErrMissingField},
		{"empty database", Entry{"h", "1", "", "u", "pw"}, "x", ErrMissingField},
		{"empty user", Entry{"h", "1", "d", "", "pw"}, "x", ErrMissingField},
	}
	for _, c := range checks {
		if err := f.SetManaged(c.svc, c.e); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	assertBytes(t, f, sample)
}

func TestRemoveManagedKeepsUnmanaged(t *testing.T) {
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			f := mustParse(t, v.apply(sample))
			if !f.RemoveManaged("prod-settle-ro") {
				t.Fatal("RemoveManaged returned false")
			}
			want := replaceOnce(t, sample, "# psql-manager:prod-settle-ro\ndb.example.com:5432:settledb:readonly:s3cr3t\\:pw\n", "")
			assertBytes(t, f, v.apply(want))
			if f.RemoveManaged("prod-settle-ro") || f.RemoveManaged("legacy") {
				t.Error("RemoveManaged removed something without a marker")
			}
		})
	}
}

func TestRenameManagedChangesMarkerOnly(t *testing.T) {
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			f := mustParse(t, v.apply(sample))
			if err := f.RenameManaged("prod-settle-ro", "settle-readonly"); err != nil {
				t.Fatal(err)
			}
			want := replaceOnce(t, sample, "# psql-manager:prod-settle-ro\n", "# psql-manager:settle-readonly\n")
			assertBytes(t, f, v.apply(want))
		})
	}
	f := mustParse(t, "# psql-manager:a\nh:1:d:u:p\n# psql-manager:b\nh:2:d:u:p\n")
	checks := []struct {
		err, want error
	}{
		{f.RenameManaged("a", "b"), ErrDuplicate},
		{f.RenameManaged("missing", "c"), ErrNotManaged},
		{f.RenameManaged("a", "bad name"), ErrInvalidName},
		{f.RenameManaged("a", "a"), nil},
	}
	for i, c := range checks {
		if !errors.Is(c.err, c.want) {
			t.Errorf("check %d: err = %v, want %v", i, c.err, c.want)
		}
	}
}

func TestMarkerWithoutEntry(t *testing.T) {
	in := "# psql-manager:x\n\nh:5432:d:u:pw\n"
	f := mustParse(t, in)
	if _, ok := f.Managed("x"); ok {
		t.Error("marker separated by a blank line must not own the entry")
	}
	if !f.HasMarker("x") || f.HasMarker("y") {
		t.Error("HasMarker is wrong")
	}
	if recs := f.Records(); len(recs) != 1 || recs[0].Owner != "" {
		t.Errorf("records = %v", recs)
	}
	if err := f.SetManaged("x", Entry{"n", "1", "d", "u", "pw"}); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, f, "# psql-manager:x\nn:1:d:u:pw\n\nh:5432:d:u:pw\n")

	g := mustParse(t, in)
	if !g.RemoveManaged("x") {
		t.Fatal("RemoveManaged returned false")
	}
	assertBytes(t, g, "\nh:5432:d:u:pw\n")
}

func TestInvalidMarkerIsPlainComment(t *testing.T) {
	f := mustParse(t, "# psql-manager:bad name\nh:1:d:u:pw\n# psql-manager:\nh:2:d:u:pw\n")
	for _, r := range f.Records() {
		if r.Owner != "" {
			t.Errorf("line %d has owner %q", r.Line, r.Owner)
		}
	}
}

func TestEntryFormattingMasksPassword(t *testing.T) {
	const secret = "top-secret-value"
	e := Entry{"h", "5432", "d", "u", secret}
	r := Record{Entry: e, Owner: "svc", Line: 1}
	for _, s := range []string{
		fmt.Sprint(e), fmt.Sprintf("%v %+v %#v %s", e, e, e, e),
		fmt.Sprintf("%v %+v", r, r), fmt.Sprintf("%v", []Entry{e}),
	} {
		if strings.Contains(s, secret) {
			t.Errorf("formatted entry leaks the password: %s", strings.ReplaceAll(s, secret, "<LEAK>"))
		}
	}
	if s := (Entry{}).String(); s != "::::" {
		t.Errorf("empty entry = %q", s)
	}
}

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PGPASSFILE", "")
	if p, err := DefaultPath(); err != nil || p != home+"/.pgpass" {
		t.Errorf("DefaultPath() = %q, %v", p, err)
	}
	t.Setenv("PGPASSFILE", "/custom/pgpass")
	if p, err := DefaultPath(); err != nil || p != "/custom/pgpass" {
		t.Errorf("DefaultPath() with env = %q, %v", p, err)
	}
}
