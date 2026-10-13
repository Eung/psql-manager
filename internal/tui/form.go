package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Eung/psql-manager/internal/store"
)

// 폼 필드 순서(명세 6절).
const (
	fName = iota
	fHost
	fPort
	fDB
	fUser
	fPassword
	fOptions
	fDesc
	fTags
	fieldCount
)

var fieldLabels = [fieldCount]string{"이름", "host", "port", "dbname", "user", "password", "options", "desc", "tags"}

// paramFields는 서비스 파일 키와 연결되는 필드다. options는 libpq의 options 파라미터다.
var paramFields = []struct {
	field int
	key   string
}{{fHost, "host"}, {fPort, "port"}, {fDB, "dbname"}, {fUser, "user"}, {fOptions, "options"}}

// form은 추가·편집 폼이다.
type form struct {
	orig   *store.Service // nil이면 추가
	inputs [fieldCount]textinput.Model
	focus  int
	err    string
}

func newForm(orig *store.Service) *form {
	f := &form{orig: orig}
	for i := range f.inputs {
		in := textinput.New()
		in.Prompt = ""
		in.CharLimit = 1024
		f.inputs[i] = in
	}
	pw := &f.inputs[fPassword]
	pw.EchoMode = textinput.EchoPassword
	pw.EchoCharacter = '●'
	f.inputs[fHost].Placeholder = "비워 두면 로컬 소켓"
	f.inputs[fTags].Placeholder = "쉼표로 구분"
	if orig == nil {
		pw.Placeholder = "비워 두면 비밀번호 없음"
	} else {
		pw.Placeholder = "비워 두면 기존 값 유지"
		f.inputs[fName].SetValue(orig.Name)
		for _, p := range paramFields {
			f.inputs[p.field].SetValue(orig.Get(p.key))
		}
		f.inputs[fDesc].SetValue(orig.Desc)
		f.inputs[fTags].SetValue(strings.Join(orig.Tags, ", "))
	}
	f.inputs[0].Focus()
	return f
}

func (f *form) value(i int) string {
	if i == fPassword {
		return f.inputs[i].Value()
	}
	return strings.TrimSpace(f.inputs[i].Value())
}

func (f *form) setFocus(i int) {
	f.inputs[f.focus].Blur()
	f.focus = (i + fieldCount) % fieldCount
	f.inputs[f.focus].Focus()
}

func splitTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (m *Model) openForm(orig *store.Service) {
	m.hide()
	m.form = newForm(orig)
	m.mode = modeForm
	m.search.Blur()
	if orig == nil {
		m.setStatus("새 서비스를 추가합니다")
	} else {
		m.setStatus("편집: " + orig.Name)
	}
}

func (m *Model) closeForm(status string) {
	m.form = nil
	m.mode = modeList
	m.search.Focus()
	m.setStatus(status)
}

func (m *Model) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.form
	switch msg.String() {
	case "esc", "ctrl+c":
		m.closeForm("취소했습니다")
		return m, nil
	case "tab", "down":
		f.setFocus(f.focus + 1)
		return m, nil
	case "shift+tab", "up":
		f.setFocus(f.focus - 1)
		return m, nil
	case "enter":
		m.submit()
		return m, nil
	}
	var cmd tea.Cmd
	f.inputs[f.focus], cmd = f.inputs[f.focus].Update(msg)
	return m, cmd
}

// submit은 폼 내용을 저장한다. 실패하면 폼에 오류를 보여 주고 열어 둔다.
func (m *Model) submit() {
	f := m.form
	name := f.value(fName)
	if name == "" {
		f.err = "이름을 입력하세요"
		return
	}
	if f.orig == nil {
		svc := store.Service{Name: name, Desc: f.value(fDesc), Tags: splitTags(f.value(fTags))}
		for _, p := range paramFields {
			if v := f.value(p.field); v != "" {
				svc.Params = append(svc.Params, store.Param{Key: p.key, Value: v})
			}
		}
		if err := m.cfg.Store.Add(svc, f.value(fPassword)); err != nil {
			f.err = err.Error()
			return
		}
		m.finishForm(name, "추가했습니다: "+name)
		return
	}

	orig := f.orig
	var u store.Update
	if name != orig.Name {
		u.Rename = name
	}
	for _, p := range paramFields {
		old, had := orig.Lookup(p.key)
		switch v := f.value(p.field); {
		case v == "" && had && old != "":
			u.Unset = append(u.Unset, p.key)
		case v != "" && v != old:
			u.Set = append(u.Set, store.Param{Key: p.key, Value: v})
		}
	}
	if d := f.value(fDesc); d != orig.Desc {
		u.Desc = &d
	}
	if tags := splitTags(f.value(fTags)); !slices.Equal(tags, orig.Tags) {
		u.Tags = &tags
	}
	// 편집 폼에서 비밀번호를 비워 두면 기존 값을 유지한다.
	if pw := f.value(fPassword); pw != "" {
		u.Password = &pw
	}
	if u.Rename == "" && len(u.Set) == 0 && len(u.Unset) == 0 && u.Desc == nil && u.Tags == nil && u.Password == nil {
		m.closeForm("바뀐 내용이 없습니다")
		return
	}
	if err := m.cfg.Store.Update(orig.Name, u); err != nil {
		f.err = err.Error()
		return
	}
	m.finishForm(name, "저장했습니다: "+name)
}

func (m *Model) finishForm(selectName, status string) {
	if name := m.form.orig; name != nil && name.Name != selectName {
		delete(m.tests, name.Name)
	}
	m.closeForm(status)
	if err := m.reload(selectName); err != nil {
		m.setError(err.Error())
	}
}
