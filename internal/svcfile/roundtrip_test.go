package svcfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readSample은 testdata에서 주석이 많은 샘플 서비스 파일을 읽는다.
func readSample(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "svcfile", "full.conf"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

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
	sample := readSample(t)
	type tc struct{ name, in string }
	var cases []tc
	for _, v := range variants {
		cases = append(cases, tc{"sample/" + v.name, v.apply(sample)})
	}
	cases = append(cases,
		tc{"empty", ""},
		tc{"single-lf", "\n"},
		tc{"single-crlf", "\r\n"},
		tc{"comment-only-no-newline", "# only a comment"},
		tc{"header-only", "[a]"},
		tc{"mixed-eol", "[a]\r\nhost=x\nport=1\r\n"},
		tc{"whitespace-lines", "  \n\t\n"},
		tc{"trailing-cr-at-eof", "[a]\nhost=x\r"},
		tc{"invalid-lines", "garbage line\n[a]\nnot a param\n[b\n=v\n"},
		tc{"utf8-and-spaces", "  # 들여쓴 주석  \n[서비스]\n  키 = 값  \n"},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := Parse([]byte(c.in)) // 파싱 오류가 있어도 원문은 보존되어야 한다
			if got := f.Bytes(); !bytes.Equal(got, []byte(c.in)) {
				t.Errorf("round trip changed bytes\n got %q\nwant %q", got, c.in)
			}
		})
	}
}
