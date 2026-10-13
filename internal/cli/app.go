package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/Eung/psql-manager/internal/store"
)

// 종료 코드(명세 4절).
const (
	exitOK       = 0
	exitUsage    = 2
	exitNotFound = 3
	exitFile     = 4
	exitConnect  = 5
)

// App은 CLI 실행에 필요한 입출력과 환경이다. 테스트에서 각 필드를 바꿔 끼운다.
type App struct {
	Version string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Getenv  func(key string) string

	// IsTerminal은 표준 입력이 터미널인지 알려 준다. 터미널일 때만 대화형으로 묻는다.
	IsTerminal func() bool
	// ReadPassword는 화면에 표시하지 않고 비밀번호 한 줄을 읽는다.
	ReadPassword func() (string, error)
	// OpenStore는 사용할 Store를 만든다.
	OpenStore func() (*store.Store, error)
	// Exec는 현재 프로세스를 psql로 교체한다. 기본값은 syscall.Exec다.
	Exec func(path string, argv, env []string) error

	in *bufio.Reader
}

// New는 실제 표준 입출력과 환경을 쓰는 App을 만든다.
func New(version string) *App {
	a := &App{
		Version: version,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Getenv:  os.Getenv,
		IsTerminal: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd()))
		},
		ReadPassword: func() (string, error) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			return string(b), err
		},
		Exec: syscall.Exec,
	}
	a.OpenStore = func() (*store.Store, error) {
		st, err := store.Open()
		if err != nil {
			return nil, err
		}
		st.Getenv = a.Getenv
		return st, nil
	}
	return a
}

// command는 서브커맨드 하나다.
type command struct {
	name    string
	args    string // 사용법에 보일 인자
	summary string
	run     func(a *App, args []string) error
}

var commands []command

func init() {
	commands = []command{
		{"add", "<name> [options]", "add a service to both files", (*App).cmdAdd},
		{"list", "[--tag <t>] [--json]", "list services", (*App).cmdList},
		{"show", "<name> [--reveal-password] [--json]", "show one service in detail", (*App).cmdShow},
		{"edit", "<name> [options]", "change a service; .pgpass follows host/port/dbname/user", (*App).cmdEdit},
		{"rm", "<name> [-y]", "remove a service and its managed .pgpass entry", (*App).cmdRm},
		{"search", "<word>... [--json]", "search name, desc, tags, host and dbname", (*App).cmdSearch},
		{"connect", "[name] [-- psql args...]", "connect with psql (pick from a list without a name)", (*App).cmdConnect},
		{"test", "<name> | --all", "check that psql can connect (select 1)", (*App).cmdTest},
		{"doctor", "[--fix]", "check both files for problems", (*App).cmdDoctor},
		{"path", "", "print the files and psql in use", (*App).cmdPath},
	}
}

// usageError는 잘못된 사용법이다. 종료 코드 2로 끝난다.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// errHelp는 -h, --help를 받아 사용법을 출력했다는 뜻이다.
var errHelp = errors.New("help requested")

// Run은 인자를 실행하고 종료 코드를 돌려준다.
func (a *App) Run(args []string) int {
	a.in = bufio.NewReader(a.Stdin)
	err := a.run(args)
	if err == nil || errors.Is(err, errHelp) {
		return exitOK
	}
	fmt.Fprintf(a.Stderr, "psql-manager: %v\n", err)
	code := exitCode(err)
	if code == exitUsage {
		fmt.Fprintln(a.Stderr, "Run 'psql-manager help' for usage.")
	}
	return code
}

func (a *App) run(args []string) error {
	if len(args) == 0 {
		// 터미널이면 번호 선택 화면으로 접속한다(M5에서 TUI로 바뀐다).
		if a.IsTerminal() {
			return a.cmdConnect(nil)
		}
		a.printUsage(a.Stderr)
		return usagef("no command given")
	}
	name, rest := args[0], args[1:]
	switch name {
	case "help", "-h", "--help":
		if len(rest) > 0 {
			if c := findCommand(rest[0]); c != nil {
				a.printCommandUsage(a.Stdout, c)
				return nil
			}
			return usagef("unknown command %q", rest[0])
		}
		a.printUsage(a.Stdout)
		return nil
	case "version", "--version":
		fmt.Fprintln(a.Stdout, a.Version)
		return nil
	}
	c := findCommand(name)
	if c == nil {
		return usagef("unknown command %q", name)
	}
	err := c.run(a, rest)
	if errors.Is(err, errHelp) {
		a.printCommandUsage(a.Stdout, c)
	}
	return err
}

func findCommand(name string) *command {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

func (a *App) printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: psql-manager <command> [arguments]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-8s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'psql-manager help <command>' for details.")
}

func (a *App) printCommandUsage(w io.Writer, c *command) {
	fmt.Fprintf(w, "Usage: psql-manager %s %s\n\n%s\n", c.name, c.args, c.summary)
	if help := commandHelp[c.name]; help != "" {
		fmt.Fprintf(w, "\n%s", help)
	}
}

// commandHelp는 명령별 옵션 설명이다.
var commandHelp = map[string]string{
	"add": serviceOptionsHelp + `  --password-stdin   read the password from the first line of stdin

On a terminal, the password is always prompted (empty for none). When no
connection option is given, the other values are prompted too.
`,
	"edit": serviceOptionsHelp + `  --rename <new>     rename the service (only the .pgpass marker changes)
  --password-stdin   set the password from the first line of stdin
  --change-password  prompt for a new password (terminal only)
  --remove-password  delete the managed .pgpass entry

An empty value removes the key, e.g. --port "" or --option sslmode=.
`,
	"list": `Options:
  --tag <t>   only services with this tag
  --json      print JSON
`,
	"show": `Options:
  --reveal-password  print the password instead of masking it
  --json             print JSON (never includes the password)
`,
	"rm": `Options:
  -y, --yes   do not ask for confirmation
`,
	"search": `Options:
  --json   print JSON
`,
	"connect": `Replaces this process with psql "service=<name>". Arguments after --
are passed to psql as they are. Without a name, pick a service from a list.
`,
	"test": `Runs psql "service=<name> connect_timeout=3" -w -X -Atc "select 1".
Exits with 5 when a connection fails.

Options:
  --all   test every service
`,
	"doctor": `Checks both files and reports problems. Exits with 4 when errors remain.

Options:
  --fix   fix what can be fixed safely (permission, orphan entries,
          out-of-sync entries)
`,
}

const serviceOptionsHelp = `Options:
  --host <host>      host name or socket directory
  --port <port>      port
  --db <dbname>      database name
  --user <user>      user name
  --desc <text>      one-line description
  --tags <a,b>       comma-separated tags
  --option <k=v>     other libpq parameter, repeatable
`

// exitCode는 오류를 명세 4절의 종료 코드로 바꾼다.
func exitCode(err error) int {
	var ue *usageError
	switch {
	case errors.As(err, &ue):
		return exitUsage
	case errors.Is(err, errConnect):
		return exitConnect
	case errors.Is(err, errDoctor):
		return exitFile
	case errors.Is(err, store.ErrNotFound):
		return exitNotFound
	case errors.Is(err, store.ErrExists), errors.Is(err, store.ErrInvalidName),
		errors.Is(err, store.ErrMissingField), errors.Is(err, store.ErrInvalidInput):
		return exitUsage
	default:
		// 파일 읽기·쓰기, 권한, 잠금, 두 파일 상태의 충돌은 모두 파일 오류로 본다.
		return exitFile
	}
}

// openStore는 Store를 열고 경고를 stderr로 보내도록 설정한다.
func (a *App) openStore() (*store.Store, error) {
	st, err := a.OpenStore()
	if err != nil {
		return nil, err
	}
	st.Warn = func(msg string) { fmt.Fprintf(a.Stderr, "psql-manager: warning: %s\n", msg) }
	return st, nil
}

// serviceError는 store의 오류를 서비스 이름이 들어간 메시지로 바꾼다.
// errors.Is로는 원래 오류를 그대로 구분할 수 있다.
type serviceError struct {
	msg string
	err error
}

func (e *serviceError) Error() string { return e.msg }
func (e *serviceError) Unwrap() error { return e.err }

// notFound는 ErrNotFound, ErrExists에 서비스 이름을 붙인 메시지를 만든다.
func notFound(name string, err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return &serviceError{fmt.Sprintf("service %q not found", name), err}
	case errors.Is(err, store.ErrExists):
		return &serviceError{fmt.Sprintf("service %q already exists; use 'psql-manager edit' to change it", name), err}
	}
	return err
}

// splitTags는 쉼표로 구분된 태그를 나눈다. 빈 문자열이면 빈 슬라이스다.
func splitTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}
