package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Eung/psql-manager/internal/store"
)

// minDetailWidth보다 좁으면 상세 패널을 접는다(명세 6절).
const minDetailWidth = 80

// passwordMask는 가린 비밀번호 표시다. 길이를 짐작할 수 없도록 항상 같은 길이로 쓴다.
const passwordMask = "●●●●"

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	boxStyle      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	selectedStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	dimStyle      = lipgloss.NewStyle().Faint(true)
	okStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	failStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	labelStyle    = lipgloss.NewStyle().Width(9)
)

const listHelp = "Enter 접속  ^E 편집  ^A 추가  ^D 삭제  ^T 테스트  ^R 비밀번호  Tab 태그  Esc 종료"
const formHelp = "Tab/↑↓ 이동  Enter 저장  Esc 취소"

// View는 bubbletea 모델 인터페이스를 구현한다.
func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("psql-manager"))
	b.WriteString("\n")
	if m.mode == modeForm {
		b.WriteString(m.formView())
	} else {
		search := m.search.View()
		if m.tag != "" {
			search += dimStyle.Render("   tag: " + m.tag)
		}
		b.WriteString(search + "\n")
		b.WriteString(m.panels())
	}
	b.WriteString("\n")
	if m.status != "" {
		if m.statusErr {
			b.WriteString(errorStyle.Render(m.status))
		} else {
			b.WriteString(m.status)
		}
	}
	b.WriteString("\n")
	help := listHelp
	if m.mode == modeForm {
		help = formHelp
	}
	b.WriteString(dimStyle.Render(help))
	return b.String()
}

// bodyHeight는 목록·상세 상자 안쪽 높이다. 제목, 검색, 상태, 도움말 줄과 테두리를 뺀다.
func (m *Model) bodyHeight() int {
	return max(3, m.height-6)
}

func (m *Model) panels() string {
	h := m.bodyHeight()
	if len(m.all) == 0 {
		msg := "등록된 서비스가 없습니다.\nCtrl+A로 첫 서비스를 추가하세요."
		return boxStyle.Width(max(20, m.width-2)).Height(h).Render(msg)
	}
	listWidth := m.width - 2
	showDetail := m.width >= minDetailWidth
	if showDetail {
		listWidth = max(24, m.width*2/5)
	}
	inner := listWidth - 4 // 테두리와 좌우 여백
	list := boxStyle.Width(listWidth - 2).Height(h).Render(m.listView(inner, h))
	if !showDetail {
		return list
	}
	detailWidth := m.width - listWidth - 2
	detail := boxStyle.Width(detailWidth - 2).Height(h).Render(m.detailView(detailWidth - 4))
	return lipgloss.JoinHorizontal(lipgloss.Top, list, detail)
}

func (m *Model) listView(width, height int) string {
	if len(m.view) == 0 {
		return dimStyle.Render("일치하는 서비스가 없습니다")
	}
	// 선택한 줄이 보이도록 위치를 정한다.
	start := 0
	if m.cursor >= height {
		start = m.cursor - height + 1
	}
	end := min(len(m.view), start+height)
	trunc := lipgloss.NewStyle().MaxWidth(width)
	var lines []string
	for i := start; i < end; i++ {
		s := m.view[i]
		mark := "  "
		switch t, ok := m.tests[s.Name]; {
		case !ok:
		case t.running:
			mark = dimStyle.Render("…") + " "
		case t.ok:
			mark = okStyle.Render("●") + " "
		default:
			mark = failStyle.Render("○") + " "
		}
		name := s.Name
		if i == m.cursor {
			name = selectedStyle.Render("▶ " + name)
		} else {
			name = "  " + name
		}
		lines = append(lines, trunc.Render(name+" "+mark))
	}
	return strings.Join(lines, "\n")
}

// sourceNote는 서비스 파일이 아닌 곳에서 온 값의 출처를 짧게 적는다.
func sourceNote(f store.Field) string {
	switch f.Source {
	case store.SourceEnv:
		return dimStyle.Render(" (env " + f.Detail + ")")
	case store.SourceDefault:
		return dimStyle.Render(" (default)")
	}
	return ""
}

func (m *Model) detailView(width int) string {
	c := m.current()
	if c == nil {
		return ""
	}
	trunc := lipgloss.NewStyle().MaxWidth(width)
	var lines []string
	row := func(label, value string) {
		lines = append(lines, trunc.Render(labelStyle.Render(label)+value))
	}
	e := c.Effective
	host := e.Host.Value
	if host == "" {
		host = "(소켓)"
	}
	row("host", host+sourceNote(e.Host))
	row("port", e.Port.Value+sourceNote(e.Port))
	row("dbname", e.DBName.Value+sourceNote(e.DBName))
	row("user", e.User.Value+sourceNote(e.User))
	for _, p := range c.Params {
		switch p.Key {
		case "host", "port", "dbname", "user":
			continue
		}
		row(p.Key, p.Value)
	}
	row("tags", strings.Join(c.Tags, ", "))
	row("desc", c.Desc)

	switch {
	case c.Password == store.PasswordNone:
		row("pass", dimStyle.Render("(없음)"))
	case m.reveal:
		row("pass", m.revealed+dimStyle.Render(" ("+c.Password.String()+")"))
	default:
		note := " (설정됨"
		if c.Password != store.PasswordManaged {
			note += ", " + c.Password.String()
		}
		row("pass", passwordMask+dimStyle.Render(note+")"))
	}
	if t, ok := m.tests[c.Name]; ok && !t.running && !t.ok {
		lines = append(lines, "", trunc.Render(failStyle.Render("테스트 실패: "+t.msg)))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) formView() string {
	f := m.form
	title := "새 서비스"
	if f.orig != nil {
		title = "편집: " + f.orig.Name
	}
	var lines []string
	lines = append(lines, titleStyle.Render(title), "")
	for i, in := range f.inputs {
		label := labelStyle.Render(fieldLabels[i])
		if i == f.focus {
			label = selectedStyle.Render(fmt.Sprintf("%-9s", fieldLabels[i]))
		}
		lines = append(lines, label+" "+in.View())
	}
	if f.err != "" {
		lines = append(lines, "", errorStyle.Render(f.err))
	}
	return boxStyle.Width(max(20, m.width-2)).Render(strings.Join(lines, "\n"))
}
