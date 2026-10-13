package cli

import (
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/Eung/psql-manager/internal/store"
)

// multiFlag는 여러 번 줄 수 있는 문자열 플래그다.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parse는 플래그와 위치 인자가 섞여 있어도 모두 읽는다(예: add name --host h).
// "--" 뒤의 인자는 플래그로 해석하지 않고 위치 인자로 돌려준다.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for i, arg := range args {
		if arg == "--" {
			args, rest = args[:i], args[i+1:]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, errHelp
			}
			return nil, usagef("%v", err)
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos, args = append(pos, args[0]), args[1:]
	}
	return append(pos, rest...), nil
}

// setFlags는 명령줄에 실제로 주어진 플래그 이름을 모은다.
func setFlags(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// connKeys는 전용 옵션이 있는 접속 파라미터다. 옵션 이름과 서비스 파일 키가 다를 수 있다.
var connKeys = []struct{ flag, key string }{
	{"host", "host"}, {"port", "port"}, {"db", "dbname"}, {"user", "user"},
}

// serviceFlags는 add와 edit이 함께 쓰는 옵션이다.
type serviceFlags struct {
	conn          map[string]*string // 옵션 이름 → 값
	desc, tags    string
	options       multiFlag
	passwordStdin bool
	set           map[string]bool
}

func (o *serviceFlags) register(fs *flag.FlagSet) {
	o.conn = map[string]*string{}
	for _, c := range connKeys {
		o.conn[c.flag] = fs.String(c.flag, "", "")
	}
	fs.StringVar(&o.desc, "desc", "", "")
	fs.StringVar(&o.tags, "tags", "", "")
	fs.Var(&o.options, "option", "")
	fs.BoolVar(&o.passwordStdin, "password-stdin", false, "")
}

// anyConn은 접속 관련 옵션(host, port, db, user, option)이 하나라도 주어졌는지 알려 준다.
func (o *serviceFlags) anyConn() bool {
	for _, c := range connKeys {
		if o.set[c.flag] {
			return true
		}
	}
	return o.set["option"]
}

// parseOptions는 --option k=v 목록을 파라미터로 바꾼다. 값이 빈 항목도 그대로 돌려준다.
func (o *serviceFlags) parseOptions() ([]store.Param, error) {
	var out []store.Param
	seen := map[string]bool{}
	for _, kv := range o.options {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, usagef("--option needs key=value, got %q", kv)
		}
		for _, c := range connKeys {
			if k == c.key {
				return nil, usagef("use --%s instead of --option %s=...", c.flag, k)
			}
		}
		if seen[k] {
			return nil, usagef("--option %s given more than once", k)
		}
		seen[k] = true
		out = append(out, store.Param{Key: k, Value: v})
	}
	return out, nil
}
