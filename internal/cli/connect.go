package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"text/tabwriter"

	"github.com/Eung/psql-manager/internal/connect"
	"github.com/Eung/psql-manager/internal/history"
	"github.com/Eung/psql-manager/internal/store"
	"github.com/Eung/psql-manager/internal/tui"
)

// errConnect는 접속 테스트가 실패했다는 뜻이다. 종료 코드 5로 끝난다.
var errConnect = errors.New("connection failed")

// errDoctor는 doctor가 고치지 못한 오류가 남았다는 뜻이다. 종료 코드 4로 끝난다.
var errDoctor = errors.New("problems found")

// testParallel은 test --all에서 동시에 실행할 psql 수다.
const testParallel = 4

// history는 접속 이력 파일을 돌려준다.
func (a *App) history() (*history.History, error) {
	p, err := history.DefaultPath(a.Getenv)
	if err != nil {
		return nil, err
	}
	return &history.History{Path: p}, nil
}

// sortByHistory는 서비스를 최근 접속 순으로, 이력이 없는 서비스는 이름순으로 정렬한다.
// 이력을 읽지 못하면 경고만 하고 이름순으로 정렬한다.
func (a *App) sortByHistory(svcs []store.Service) []store.Service {
	return a.sortServices(svcs, true)
}

// sortServices는 sortByHistory와 같지만, warn이 false면 경고를 출력하지 않는다.
// TUI가 화면을 차지하고 있을 때 stderr에 쓰면 화면이 깨지기 때문이다.
func (a *App) sortServices(svcs []store.Service, warn bool) []store.Service {
	var entries []history.Entry
	if h, err := a.history(); err == nil {
		if entries, err = h.Load(); err != nil && warn {
			a.warnf("cannot read history: %v", err)
		}
	}
	names := make([]string, len(svcs))
	byName := map[string][]store.Service{}
	for i, s := range svcs {
		names[i] = s.Name
		byName[s.Name] = append(byName[s.Name], s)
	}
	history.Sort(names, history.LastUsed(entries))
	out := make([]store.Service, 0, len(svcs))
	for _, n := range names {
		out = append(out, byName[n][0])
		byName[n] = byName[n][1:]
	}
	return out
}

func (a *App) cmdConnect(args []string) error {
	fs := newFlagSet("connect")
	pos, extra, err := parseRest(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usagef("connect takes at most one service name; pass psql arguments after --")
	}
	st, err := a.openStore()
	if err != nil {
		return err
	}
	var name string
	if len(pos) == 1 {
		name = pos[0]
		if _, err := st.Get(name); err != nil {
			return notFound(name, err)
		}
	} else if !a.IsTerminal() {
		return usagef("no service name given; pass a name when not on a terminal")
	}
	// TUI를 띄우기 전에 psql을 찾아 둔다. 고른 뒤에 없다는 걸 알면 헛수고다.
	psql, _, err := connect.FindPsql(a.Getenv)
	if err != nil {
		return err
	}
	if name == "" {
		name, err = a.RunTUI(tui.Config{
			Store: st,
			Sort:  func(s []store.Service) []store.Service { return a.sortServices(s, false) },
			Test:  func(ctx context.Context, n string) connect.Result { return connect.Test(ctx, psql, n) },
		})
		if err != nil || name == "" {
			return err
		}
	}
	// 이력을 쓰지 못해도 접속은 막지 않는다.
	if h, err := a.history(); err == nil {
		err = h.Record(name)
		if err != nil {
			a.warnf("cannot write history: %v", err)
		}
	}
	if err := a.Exec(psql, connect.Argv(psql, name, extra), os.Environ()); err != nil {
		return fmt.Errorf("run %s: %w", psql, err)
	}
	return nil
}

func (a *App) cmdTest(args []string) error {
	fs := newFlagSet("test")
	all := fs.Bool("all", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	switch {
	case *all && len(pos) > 0:
		return usagef("test takes either a service name or --all")
	case !*all && len(pos) != 1:
		return usagef("test needs exactly one service name or --all")
	}
	st, err := a.openStore()
	if err != nil {
		return err
	}
	var names []string
	if *all {
		svcs, err := st.List()
		if err != nil {
			return err
		}
		for _, s := range a.sortByHistory(svcs) {
			if !contains(names, s.Name) {
				names = append(names, s.Name)
			}
		}
		if len(names) == 0 {
			fmt.Fprintln(a.Stderr, "no services to test")
			return nil
		}
	} else {
		if _, err := st.Get(pos[0]); err != nil {
			return notFound(pos[0], err)
		}
		names = pos
	}
	psql, _, err := connect.FindPsql(a.Getenv)
	if err != nil {
		return err
	}

	results := make([]connect.Result, len(names))
	var wg sync.WaitGroup
	sem := make(chan struct{}, testParallel)
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = connect.Test(context.Background(), psql, n)
		}()
	}
	wg.Wait()

	failed := 0
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	for i, r := range results {
		if r.OK {
			fmt.Fprintf(tw, "ok\t%s\n", names[i])
		} else {
			failed++
			fmt.Fprintf(tw, "FAIL\t%s\t%s\n", names[i], r.Message)
		}
	}
	tw.Flush()
	if failed > 0 {
		return fmt.Errorf("%w: %d of %d", errConnect, failed, len(names))
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (a *App) cmdDoctor(args []string) error {
	fs := newFlagSet("doctor")
	fix := fs.Bool("fix", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("doctor takes no arguments")
	}
	st, err := a.openStore()
	if err != nil {
		return err
	}
	if *fix {
		fixed, err := st.Fix()
		if len(fixed) > 0 {
			a.printProblems("fixed", fixed)
			fmt.Fprintln(a.Stdout)
		}
		switch {
		case errors.Is(err, store.ErrFixBlocked):
			a.warnf("%v", err)
		case err != nil:
			return err
		}
	}
	ps, err := st.Diagnose()
	if err != nil {
		return err
	}
	if len(ps) == 0 {
		fmt.Fprintln(a.Stdout, "no problems found")
		return nil
	}
	a.printProblems("", ps)

	count := map[store.Severity]int{}
	fixable := 0
	for _, p := range ps {
		count[p.Severity]++
		if p.Fixable {
			fixable++
		}
	}
	fmt.Fprintf(a.Stdout, "\n%s, %s, %d info\n", plural(count[store.SeverityError], "error"),
		plural(count[store.SeverityWarning], "warning"), count[store.SeverityInfo])
	if fixable > 0 && !*fix {
		fmt.Fprintf(a.Stdout, "run 'psql-manager doctor --fix' to fix %d of them\n", fixable)
	}
	if count[store.SeverityError] > 0 {
		return fmt.Errorf("%w: %s", errDoctor, plural(count[store.SeverityError], "error"))
	}
	return nil
}

// printProblems는 점검 결과를 한 줄에 하나씩 출력한다. label이 있으면 심각도 대신 쓴다.
func (a *App) printProblems(label string, ps []store.Problem) {
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	for _, p := range ps {
		sev := label
		if sev == "" {
			sev = p.Severity.String()
		}
		loc := p.File
		if p.Line > 0 {
			loc = fmt.Sprintf("%s:%d", p.File, p.Line)
		}
		msg := p.Message
		if p.Service != "" {
			msg = fmt.Sprintf("service %q: %s", p.Service, msg)
		}
		if p.Fixable && label == "" {
			msg += " (fixable)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", sev, loc, p.Kind, msg)
	}
	tw.Flush()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
