package pgpass

import (
	"bytes"
	"strings"
	"testing"
)

// sample에는 관리하지 않는 줄, 관리 줄 한 쌍, 잘못된 줄, 와일드카드 줄이 섞여 있다.
// 비밀번호는 모두 테스트용 더미 값이다.
const sample = `# 손으로 관리하는 pgpass
localhost:5432:*:postgres:local\:pw

# psql-manager:prod-settle-ro
db.example.com:5432:settledb:readonly:s3cr3t\:pw
legacy.example.com:5432:legacy:app:old\\pw
broken-line-without-enough-fields

# 기본값
*:*:*:*:fallback
`

// variant는 같은 LF 텍스트를 줄바꿈 형식과 파일 끝 개행 유무로 바꿔 표현하는 방법 하나다.
type variant struct {
	name  string
	apply func(string) string
}

var variants = []variant{
	{"lf", func(s string) string { return s }},
	{"crlf", func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }},
	{"lf-no-final-newline", func(s string) string { return strings.TrimSuffix(s, "\n") }},
	{"crlf-no-final-newline", func(s string) string {
		return strings.TrimSuffix(strings.ReplaceAll(s, "\n", "\r\n"), "\r\n")
	}},
}

func TestRoundTripByteIdentical(t *testing.T) {
	type tc struct{ name, in string }
	var cases []tc
	for _, v := range variants {
		cases = append(cases, tc{"sample/" + v.name, v.apply(sample)})
	}
	cases = append(cases,
		tc{"empty", ""},
		tc{"single-lf", "\n"},
		tc{"comment-only-no-newline", "#x"},
		tc{"entry-no-newline", "a:b:c:d:e"},
		tc{"mixed-eol", "a:b:c:d:e\r\n#x\nf:g:h:i:j\r\n"},
		tc{"trailing-backslash", "h:p:d:u:pw\\\n"},
		tc{"whitespace-line", "   \n"},
		tc{"extra-fields", "h:p:d:u:pw:extra\n"},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := Parse([]byte(c.in))
			if got := f.Bytes(); !bytes.Equal(got, []byte(c.in)) {
				// 실패 메시지에도 비밀번호가 섞이지 않도록 길이만 출력한다.
				t.Errorf("round trip changed bytes (got %d bytes, want %d)", len(got), len(c.in))
			}
		})
	}
}
