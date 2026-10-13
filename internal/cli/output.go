package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/Eung/psql-manager/internal/store"
)

// passwordMask는 가린 비밀번호 표시다. 길이를 짐작할 수 없도록 항상 같은 길이로 쓴다.
const passwordMask = "********"

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// printTable은 서비스 목록 표를 출력한다. 비밀번호는 상태만 보여 주고,
// 환경변수나 기본값으로 판정한 행에는 *를 붙인다.
func (a *App) printTable(svcs []store.Service) {
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tHOST\tDBNAME\tUSER\tPASSWORD\tTAGS\tDESC")
	marked := false
	for _, s := range svcs {
		pw := s.Password.String()
		if s.Password != store.PasswordManaged && s.Effective.Fallback() {
			pw += "*"
			marked = true
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, dash(s.Get("host")), dash(s.Get("dbname")),
			dash(s.Get("user")), pw, dash(strings.Join(s.Tags, ",")), dash(s.Desc))
	}
	tw.Flush()
	if marked {
		fmt.Fprintln(a.Stdout, "\n* judged with environment variables or libpq defaults; see 'psql-manager show <name>'")
	}
}

func sourceText(f store.Field) string {
	switch f.Source {
	case store.SourceService:
		return "service file"
	case store.SourceEnv:
		return "env: " + f.Detail
	}
	if f.Detail != "" {
		return "default: " + f.Detail
	}
	return "default"
}

// printShow는 서비스 한 개를 자세히 출력한다. reveal이 아니면 비밀번호를 가린다.
func (a *App) printShow(s store.Service, pgpassPath string, reveal bool, password string) {
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	row := func(k, v, note string) {
		if note == "" {
			// 출처 칸이 없는 줄은 값 뒤에 공백이 붙지 않게 값까지만 쓴다.
			fmt.Fprintf(tw, "%s\t%s\n", k, v)
			return
		}
		fmt.Fprintf(tw, "%s\t%s\t(%s)\n", k, v, note)
	}
	row("name", s.Name, "")
	row("desc", dash(s.Desc), "")
	row("tags", dash(strings.Join(s.Tags, ", ")), "")
	e := s.Effective
	for _, f := range []struct {
		key string
		f   store.Field
	}{{"host", e.Host}, {"port", e.Port}, {"dbname", e.DBName}, {"user", e.User}} {
		row(f.key, dash(f.f.Value), sourceText(f.f))
	}
	for _, p := range s.Params {
		switch p.Key {
		case "host", "port", "dbname", "user":
			continue
		}
		row(p.Key, dash(p.Value), "service file")
	}

	value := passwordMask
	if reveal {
		value = password
	}
	where := fmt.Sprintf("%s line %d", pgpassPath, s.PasswordLine)
	switch s.Password {
	case store.PasswordManaged:
		row("password", value, "managed, "+where)
	case store.PasswordUnmanaged:
		row("password", value, "unmanaged, "+where)
	case store.PasswordShared:
		row("password", value, fmt.Sprintf("shared with %q, %s", s.PasswordOwner, where))
	default:
		row("password", "-", "none")
	}
	tw.Flush()
}

// JSON 출력 형식. 필드 이름은 스크립트가 의존하므로 바꾸지 않는다. 비밀번호 값은 넣지 않는다.
type (
	jsonField struct {
		Value  string `json:"value"`
		Source string `json:"source"`
		Detail string `json:"detail"`
	}
	jsonEffective struct {
		Host   jsonField `json:"host"`
		Port   jsonField `json:"port"`
		DBName jsonField `json:"dbname"`
		User   jsonField `json:"user"`
	}
	jsonParam struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	jsonPassword struct {
		State string `json:"state"`
		Line  int    `json:"line"`
		Owner string `json:"owner"`
	}
	jsonService struct {
		Name      string        `json:"name"`
		Desc      string        `json:"desc"`
		Tags      []string      `json:"tags"`
		Line      int           `json:"line"`
		Params    []jsonParam   `json:"params"`
		Effective jsonEffective `json:"effective"`
		Password  jsonPassword  `json:"password"`
	}
	jsonResult struct {
		jsonService
		Score int `json:"score"`
	}
)

func toJSONField(f store.Field) jsonField {
	return jsonField{Value: f.Value, Source: string(f.Source), Detail: f.Detail}
}

func toJSON(s store.Service) jsonService {
	j := jsonService{
		Name:   s.Name,
		Desc:   s.Desc,
		Tags:   append([]string{}, s.Tags...),
		Line:   s.Line,
		Params: []jsonParam{},
		Effective: jsonEffective{
			Host: toJSONField(s.Effective.Host), Port: toJSONField(s.Effective.Port),
			DBName: toJSONField(s.Effective.DBName), User: toJSONField(s.Effective.User),
		},
		Password: jsonPassword{State: s.Password.String(), Line: s.PasswordLine, Owner: s.PasswordOwner},
	}
	for _, p := range s.Params {
		j.Params = append(j.Params, jsonParam{Key: p.Key, Value: p.Value})
	}
	return j
}

func toJSONList(svcs []store.Service) []jsonService {
	out := []jsonService{}
	for _, s := range svcs {
		out = append(out, toJSON(s))
	}
	return out
}

func toJSONResults(rs []store.Result) []jsonResult {
	out := []jsonResult{}
	for _, r := range rs {
		out = append(out, jsonResult{jsonService: toJSON(r.Service), Score: r.Score})
	}
	return out
}

func (a *App) printJSON(v any) error {
	enc := json.NewEncoder(a.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
