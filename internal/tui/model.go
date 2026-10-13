// Package tui는 서비스를 골라 접속하는 대화형 화면을 구현한다.
package tui

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Eung/psql-manager/internal/connect"
	"github.com/Eung/psql-manager/internal/store"
)

// Config는 TUI가 쓰는 의존성이다.
type Config struct {
	Store *store.Store
	// Sort는 서비스를 최근 접속 순으로 정렬한다. nil이면 받은 순서를 쓴다.
	Sort func([]store.Service) []store.Service
	// Test는 서비스 하나의 접속을 시험한다. 화면이 멈추지 않도록 tea.Cmd 안에서 부른다.
	Test func(ctx context.Context, name string) connect.Result
}

// mode는 화면의 현재 상태다.
type mode int

const (
	modeList          mode = iota // 목록과 검색
	modeForm                      // 추가·편집 폼
	modeConfirmDelete             // 삭제 확인
)

// testState는 접속 테스트 상태다.
type testState struct {
	running bool
	ok      bool
	msg     string
}

// testDoneMsg는 비동기 접속 테스트가 끝났다는 메시지다.
type testDoneMsg struct {
	name string
	res  connect.Result
}

// Model은 bubbletea 모델이다.
type Model struct {
	cfg Config

	all    []store.Service // 최근 접속 순으로 정렬한 전체 목록(이름 중복 제거)
	view   []store.Service // 태그 필터와 검색어를 적용한 목록
	cursor int
	search textinput.Model
	tags   []string // 전체 태그(이름순)
	tag    string   // 선택한 태그 필터, ""면 전체

	width, height int

	reveal   bool   // 상세 패널 비밀번호 표시 여부
	revealed string // 표시 중인 비밀번호. 가리면 바로 지운다.

	tests     map[string]testState
	status    string
	statusErr bool

	mode mode
	form *form

	// Selected는 Enter로 고른 서비스 이름이다. 종료 뒤 호출하는 쪽이 접속한다.
	Selected string
	quitting bool
}

// New는 서비스 목록을 읽어 모델을 만든다.
func New(cfg Config) (*Model, error) {
	m := &Model{cfg: cfg, width: 100, height: 30, tests: map[string]testState{}}
	m.search = textinput.New()
	m.search.Prompt = "검색: "
	m.search.Placeholder = "이름, desc, tags, host, dbname"
	m.search.Focus()
	// 목록을 읽을 때 생기는 경고(형식 오류 등)는 상태 줄에 보여 준다.
	cfg.Store.Warn = func(msg string) { m.setError(msg) }
	if err := m.reload(""); err != nil {
		return nil, err
	}
	return m, nil
}

// Init은 bubbletea 모델 인터페이스를 구현한다.
func (m *Model) Init() tea.Cmd { return textinput.Blink }

// pkgPrefix는 하위 패키지가 오류 메시지 앞에 붙이는 출처다. 화면에는 보이지 않게 뗀다.
var pkgPrefix = regexp.MustCompile(`(^|: |\n)(?:store|svcfile|pgpass|fsutil|connect|history): `)

func (m *Model) setStatus(s string) { m.status, m.statusErr = s, false }
func (m *Model) setError(s string)  { m.status, m.statusErr = stripPrefixes(s), true }

// stripPrefixes는 패키지 접두어를 메시지 맨 앞이나 ": " 바로 뒤에서만 뗀다.
func stripPrefixes(s string) string {
	for {
		t := pkgPrefix.ReplaceAllString(s, "${1}")
		if t == s {
			return s
		}
		s = t
	}
}

// reload는 서비스 목록을 다시 읽고, 가능하면 keep을 선택한 상태로 둔다.
func (m *Model) reload(keep string) error {
	svcs, err := m.cfg.Store.List()
	if err != nil {
		return err
	}
	if m.cfg.Sort != nil {
		svcs = m.cfg.Sort(svcs)
	}
	// 같은 이름의 섹션이 여럿이면 libpq가 쓰는 첫 섹션만 보여 준다.
	seen := map[string]bool{}
	m.all = m.all[:0]
	tagSet := map[string]bool{}
	for _, s := range svcs {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		m.all = append(m.all, s)
		for _, t := range s.Tags {
			tagSet[t] = true
		}
	}
	m.tags = m.tags[:0]
	for t := range tagSet {
		m.tags = append(m.tags, t)
	}
	slices.Sort(m.tags)
	if m.tag != "" && !tagSet[m.tag] {
		m.tag = ""
	}
	m.refilter(keep)
	return nil
}

// refilter는 태그 필터와 검색어를 다시 적용한다. keep이 남아 있으면 그 줄을 선택한다.
func (m *Model) refilter(keep string) {
	var base []store.Service
	for _, s := range m.all {
		if m.tag == "" || slices.Contains(s.Tags, m.tag) {
			base = append(base, s)
		}
	}
	if q := strings.TrimSpace(m.search.Value()); q != "" {
		m.view = m.view[:0]
		for _, r := range store.Match(base, q) {
			m.view = append(m.view, r.Service)
		}
	} else {
		m.view = base
	}
	m.cursor = 0
	for i, s := range m.view {
		if s.Name == keep {
			m.cursor = i
		}
	}
}

// current는 선택한 서비스를 돌려준다. 목록이 비어 있으면 nil이다.
func (m *Model) current() *store.Service {
	if m.cursor < 0 || m.cursor >= len(m.view) {
		return nil
	}
	return &m.view[m.cursor]
}

func (m *Model) currentName() string {
	if c := m.current(); c != nil {
		return c.Name
	}
	return ""
}

// hide는 표시 중인 비밀번호를 가리고 메모리에서도 지운다.
func (m *Model) hide() { m.reveal, m.revealed = false, "" }

// Update는 bubbletea 모델 인터페이스를 구현한다.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case testDoneMsg:
		m.tests[msg.name] = testState{ok: msg.res.OK, msg: msg.res.Message}
		if msg.res.OK {
			m.setStatus("접속 성공: " + msg.name)
		} else {
			m.setError("접속 실패: " + msg.name + ": " + msg.res.Message)
		}
		return m, nil
	case tea.KeyMsg:
		switch m.mode {
		case modeForm:
			return m.updateForm(msg)
		case modeConfirmDelete:
			return m.updateConfirm(msg)
		}
		return m.updateList(msg)
	}
	// 커서 깜빡임 같은 그 밖의 메시지는 포커스가 있는 입력창에 넘긴다.
	var cmd tea.Cmd
	if m.mode == modeForm {
		m.form.inputs[m.form.focus], cmd = m.form.inputs[m.form.focus].Update(msg)
	} else {
		m.search, cmd = m.search.Update(msg)
	}
	return m, cmd
}

func (m *Model) move(d int) {
	if len(m.view) == 0 {
		return
	}
	m.cursor = max(0, min(len(m.view)-1, m.cursor+d))
	m.hide()
}

func (m *Model) quit() (tea.Model, tea.Cmd) {
	m.hide()
	m.quitting = true
	return m, tea.Quit
}

func (m *Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "ctrl+p":
		m.move(-1)
	case "down", "ctrl+n":
		m.move(1)
	case "enter":
		if c := m.current(); c != nil {
			m.Selected = c.Name
			return m.quit()
		}
	case "ctrl+a":
		m.openForm(nil)
		return m, textinput.Blink
	case "ctrl+e":
		if c := m.current(); c != nil {
			m.openForm(c)
			return m, textinput.Blink
		}
	case "ctrl+d":
		if c := m.current(); c != nil {
			m.mode = modeConfirmDelete
			m.setError("삭제할까요? " + c.Name + " (서비스 섹션과 관리 중인 .pgpass 줄) [y/N]")
		}
	case "ctrl+t":
		if c := m.current(); c != nil && m.cfg.Test != nil {
			name, test := c.Name, m.cfg.Test
			m.tests[name] = testState{running: true}
			m.setStatus("접속 테스트 중: " + name)
			return m, func() tea.Msg { return testDoneMsg{name: name, res: test(context.Background(), name)} }
		}
	case "ctrl+r":
		m.toggleReveal()
	case "tab":
		m.nextTag()
	case "esc", "ctrl+c":
		switch {
		case m.tag != "":
			m.tag = ""
			m.refilter(m.currentName())
		case m.search.Value() != "":
			m.search.SetValue("")
			m.refilter(m.currentName())
		default:
			return m.quit()
		}
	default:
		before := m.search.Value()
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		if m.search.Value() != before {
			m.hide()
			m.refilter(m.currentName())
		}
		return m, cmd
	}
	return m, nil
}

// toggleReveal은 선택한 서비스의 비밀번호 표시를 켜고 끈다.
func (m *Model) toggleReveal() {
	if m.reveal {
		m.hide()
		return
	}
	c := m.current()
	if c == nil {
		return
	}
	pw, st, err := m.cfg.Store.Password(c.Name)
	switch {
	case err != nil:
		m.setError(err.Error())
	case st == store.PasswordNone:
		m.setStatus("이 서비스에 쓰이는 비밀번호가 없습니다")
	default:
		m.reveal, m.revealed = true, pw
	}
}

// nextTag는 태그 필터를 전체 → 첫 태그 → … → 전체 순으로 바꾼다.
func (m *Model) nextTag() {
	if len(m.tags) == 0 {
		return
	}
	i := slices.Index(m.tags, m.tag) // 전체("")면 -1
	if i+1 < len(m.tags) {
		m.tag = m.tags[i+1]
	} else {
		m.tag = ""
	}
	m.hide()
	m.refilter(m.currentName())
}

func (m *Model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mode = modeList
	name := m.currentName()
	if s := msg.String(); s != "y" && s != "Y" {
		m.setStatus("삭제를 취소했습니다")
		return m, nil
	}
	if err := m.cfg.Store.Remove(name); err != nil {
		m.setError(err.Error())
		return m, nil
	}
	delete(m.tests, name)
	m.hide()
	if err := m.reload(""); err != nil {
		m.setError(err.Error())
		return m, nil
	}
	m.setStatus("삭제했습니다: " + name)
	return m, nil
}

// Run은 TUI를 띄우고, Enter로 고른 서비스 이름을 돌려준다. 고르지 않고 끝내면 ""다.
// 접속은 호출하는 쪽이 TUI가 터미널을 복원한 뒤에 한다.
func Run(cfg Config) (string, error) {
	m, err := New(cfg)
	if err != nil {
		return "", err
	}
	final, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return "", err
	}
	return final.(*Model).Selected, nil
}
