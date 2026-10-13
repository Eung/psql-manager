package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Eung/psql-manager/internal/connect"
	"github.com/Eung/psql-manager/internal/store"
)

// oneName은 위치 인자가 서비스 이름 하나인지 확인한다.
func oneName(cmd string, pos []string) (string, error) {
	if len(pos) != 1 {
		return "", usagef("%s needs exactly one service name", cmd)
	}
	return pos[0], nil
}

func checkName(name string) error {
	if !store.ValidName(name) {
		return usagef("invalid service name %q (allowed: A-Z a-z 0-9 . _ -)", name)
	}
	return nil
}

func (a *App) cmdAdd(args []string) error {
	fs := newFlagSet("add")
	var o serviceFlags
	o.register(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	name, err := oneName("add", pos)
	if err != nil {
		return err
	}
	if err := checkName(name); err != nil {
		return err
	}
	o.set = setFlags(fs)
	tty := a.IsTerminal()

	// 터미널에서 접속 옵션 없이 실행하면 값을 하나씩 묻는다.
	if tty && !o.passwordStdin && !o.anyConn() {
		if err := a.promptService(&o); err != nil {
			return err
		}
	}

	svc := store.Service{Name: name, Desc: o.desc, Tags: splitTags(o.tags)}
	for _, c := range connKeys {
		if v := *o.conn[c.flag]; v != "" {
			svc.Params = append(svc.Params, store.Param{Key: c.key, Value: v})
		}
	}
	opts, err := o.parseOptions()
	if err != nil {
		return err
	}
	for _, p := range opts {
		if p.Value != "" {
			svc.Params = append(svc.Params, p)
		}
	}

	password := ""
	switch {
	case o.passwordStdin:
		if password, err = a.readPasswordStdin(); err != nil {
			return err
		}
	case tty:
		if password, err = a.promptPassword("Password (empty for none): ", true); err != nil {
			return err
		}
	}

	st, err := a.openStore()
	if err != nil {
		return err
	}
	if err := st.Add(svc, password); err != nil {
		return notFound(name, err)
	}
	if password != "" {
		fmt.Fprintf(a.Stdout, "added service %q with a password in %s\n", name, st.PgpassPath)
	} else {
		fmt.Fprintf(a.Stdout, "added service %q (no password)\n", name)
	}
	return nil
}

// promptService는 주어지지 않은 값을 대화형으로 묻는다.
func (a *App) promptService(o *serviceFlags) error {
	questions := []struct {
		flag, question, def string
	}{
		{"host", "host (empty for local socket): ", ""},
		{"port", "port [5432]: ", "5432"},
		{"db", "dbname: ", ""},
		{"user", "user: ", ""},
	}
	for _, q := range questions {
		v, err := a.prompt(q.question)
		if err != nil {
			return err
		}
		if v = strings.TrimSpace(v); v == "" {
			v = q.def
		}
		*o.conn[q.flag] = v
	}
	if !o.set["desc"] {
		v, err := a.prompt("desc: ")
		if err != nil {
			return err
		}
		o.desc = strings.TrimSpace(v)
	}
	if !o.set["tags"] {
		v, err := a.prompt("tags (comma separated): ")
		if err != nil {
			return err
		}
		o.tags = v
	}
	return nil
}

func (a *App) cmdEdit(args []string) error {
	fs := newFlagSet("edit")
	var o serviceFlags
	o.register(fs)
	rename := fs.String("rename", "", "")
	changePassword := fs.Bool("change-password", false, "")
	removePassword := fs.Bool("remove-password", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	name, err := oneName("edit", pos)
	if err != nil {
		return err
	}
	o.set = setFlags(fs)

	var u store.Update
	for _, c := range connKeys {
		if !o.set[c.flag] {
			continue
		}
		if v := *o.conn[c.flag]; v == "" {
			u.Unset = append(u.Unset, c.key)
		} else {
			u.Set = append(u.Set, store.Param{Key: c.key, Value: v})
		}
	}
	opts, err := o.parseOptions()
	if err != nil {
		return err
	}
	for _, p := range opts {
		if p.Value == "" {
			u.Unset = append(u.Unset, p.Key)
		} else {
			u.Set = append(u.Set, p)
		}
	}
	if o.set["desc"] {
		u.Desc = &o.desc
	}
	if o.set["tags"] {
		tags := splitTags(o.tags)
		u.Tags = &tags
	}
	if o.set["rename"] {
		if err := checkName(*rename); err != nil {
			return err
		}
		u.Rename = *rename
	}

	n := 0
	for _, b := range []bool{o.passwordStdin, *changePassword, *removePassword} {
		if b {
			n++
		}
	}
	if n > 1 {
		return usagef("use only one of --password-stdin, --change-password, --remove-password")
	}
	switch {
	case o.passwordStdin:
		pw, err := a.readPasswordStdin()
		if err != nil {
			return err
		}
		u.Password = &pw
	case *changePassword:
		if !a.IsTerminal() {
			return usagef("--change-password needs a terminal; use --password-stdin in scripts")
		}
		pw, err := a.promptPassword("New password: ", false)
		if err != nil {
			return err
		}
		u.Password = &pw
	case *removePassword:
		empty := ""
		u.Password = &empty
	}

	if u.Rename == "" && u.Desc == nil && u.Tags == nil && len(u.Set) == 0 && len(u.Unset) == 0 && u.Password == nil {
		return usagef("edit: nothing to change")
	}

	st, err := a.openStore()
	if err != nil {
		return err
	}
	if err := st.Update(name, u); err != nil {
		if errors.Is(err, store.ErrExists) && u.Rename != "" {
			return notFound(u.Rename, err)
		}
		return notFound(name, err)
	}
	if u.Rename != "" && u.Rename != name {
		fmt.Fprintf(a.Stdout, "updated service %q (renamed to %q)\n", name, u.Rename)
	} else {
		fmt.Fprintf(a.Stdout, "updated service %q\n", name)
	}
	return nil
}

func (a *App) cmdRm(args []string) error {
	fs := newFlagSet("rm")
	yes := fs.Bool("y", false, "")
	fs.BoolVar(yes, "yes", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	name, err := oneName("rm", pos)
	if err != nil {
		return err
	}
	st, err := a.openStore()
	if err != nil {
		return err
	}
	if !*yes {
		// 없는 서비스라면 묻기 전에 알린다.
		if _, err := st.Get(name); err != nil {
			return notFound(name, err)
		}
		if !a.IsTerminal() {
			return usagef("rm asks for confirmation; pass -y when not on a terminal")
		}
		ok, err := a.confirm(fmt.Sprintf("Remove service %q and its managed .pgpass entry?", name))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(a.Stderr, "canceled")
			return nil
		}
	}
	if err := st.Remove(name); err != nil {
		return notFound(name, err)
	}
	fmt.Fprintf(a.Stdout, "removed service %q\n", name)
	return nil
}

func (a *App) cmdList(args []string) error {
	fs := newFlagSet("list")
	tag := fs.String("tag", "", "")
	asJSON := fs.Bool("json", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("list takes no arguments")
	}
	st, err := a.openStore()
	if err != nil {
		return err
	}
	svcs, err := st.List()
	if err != nil {
		return err
	}
	if setFlags(fs)["tag"] {
		svcs = filterTag(svcs, *tag)
	}
	if *asJSON {
		return a.printJSON(toJSONList(svcs))
	}
	if len(svcs) == 0 {
		fmt.Fprintln(a.Stderr, "no services; add one with 'psql-manager add <name>'")
		return nil
	}
	a.printTable(svcs)
	return nil
}

func filterTag(svcs []store.Service, tag string) []store.Service {
	var out []store.Service
	for _, s := range svcs {
		for _, t := range s.Tags {
			if strings.EqualFold(t, tag) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func (a *App) cmdSearch(args []string) error {
	fs := newFlagSet("search")
	asJSON := fs.Bool("json", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(pos, " "))
	if query == "" {
		return usagef("search needs at least one word")
	}
	st, err := a.openStore()
	if err != nil {
		return err
	}
	results, err := st.Search(query)
	if err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(toJSONResults(results))
	}
	if len(results) == 0 {
		fmt.Fprintf(a.Stderr, "no services match %q\n", query)
		return nil
	}
	svcs := make([]store.Service, len(results))
	for i, r := range results {
		svcs[i] = r.Service
	}
	a.printTable(svcs)
	return nil
}

func (a *App) cmdShow(args []string) error {
	fs := newFlagSet("show")
	reveal := fs.Bool("reveal-password", false, "")
	asJSON := fs.Bool("json", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	name, err := oneName("show", pos)
	if err != nil {
		return err
	}
	if *reveal && *asJSON {
		return usagef("--reveal-password cannot be used with --json")
	}
	st, err := a.openStore()
	if err != nil {
		return err
	}
	svc, err := st.Get(name)
	if err != nil {
		return notFound(name, err)
	}
	if *asJSON {
		return a.printJSON(toJSON(svc))
	}
	password := ""
	if *reveal {
		if password, _, err = st.Password(name); err != nil {
			return notFound(name, err)
		}
	}
	a.printShow(svc, st.PgpassPath, *reveal, password)
	return nil
}

func (a *App) cmdPath(args []string) error {
	fs := newFlagSet("path")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("path takes no arguments")
	}
	st, err := a.openStore()
	if err != nil {
		return err
	}
	source := func(env string) string {
		if a.Getenv(env) != "" {
			return env
		}
		return "default"
	}
	fmt.Fprintf(a.Stdout, "%-14s%s (%s)\n", "service file:", st.ServicePath, source("PGSERVICEFILE"))
	fmt.Fprintf(a.Stdout, "%-14s%s (%s)\n", "pgpass:", st.PgpassPath, source("PGPASSFILE"))
	if p, src, err := connect.FindPsql(a.Getenv); err != nil {
		fmt.Fprintf(a.Stdout, "%-14snot found (%s)\n", "psql:", src)
	} else {
		fmt.Fprintf(a.Stdout, "%-14s%s (%s)\n", "psql:", p, src)
	}
	return nil
}
