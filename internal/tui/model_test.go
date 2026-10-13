package tui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Eung/psql-manager/internal/connect"
	"github.com/Eung/psql-manager/internal/store"
)

// 테스트용 더미 비밀번호.
const (
	pwRO  = "ro-dummy-pw"
	pwNew = "new-dummy-pw"
)

const baseSvc = `# desc: 정산 읽기 전용
# tags: prod, readonly
[prod-ro]
host=db.example.com
port=5432
dbname=settledb
user=readonly
sslmode=require

# tags: stg
[stg-settle]
host=stg.example.com
port=5432
dbname=settledb
user=app

[dev]
dbname=devdb
user=me
`

const basePgpass = "# psql-manager:prod-ro\ndb.example.com:5432:settledb:readonly:" + pwRO + "\n"

type harness struct {
	m       *Model
	svcPath string
	ppPath  string
	tested  []string
	tz      string // 추가 폼에 미리 넣을 시간대
}

// newHarness는 임시 파일 위의 실제 Store로 모델을 만든다. 정렬은 이름 역순으로 해
// Sort가 실제로 쓰이는지 확인할 수 있게 한다.
func newHarness(t *testing.T, svc, pp string) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{svcPath: filepath.Join(dir, "pg_service.conf"), ppPath: filepath.Join(dir, "pgpass"), tz: "Asia/Seoul"}
	if svc != "" {
		os.WriteFile(h.svcPath, []byte(svc), 0o600)
	}
	if pp != "" {
		os.WriteFile(h.ppPath, []byte(pp), 0o600)
	}
	st := store.New(h.svcPath, h.ppPath, filepath.Join(dir, "lock"))
	st.Getenv = func(string) string { return "" }
	st.Username = func() (string, error) { return "tester", nil }
	m, err := New(Config{
		Store: st,
		Sort: func(s []store.Service) []store.Service {
			out := slices.Clone(s)
			slices.SortFunc(out, func(a, b store.Service) int { return strings.Compare(b.Name, a.Name) })
			return out
		},
		// 실제 /etc/localtime에 의존하지 않도록 고정한다.
		Timezone: func() string { return h.tz },
		Test: func(_ context.Context, name string) connect.Result {
			h.tested = append(h.tested, name)
			if name == "stg-settle" {
				return connect.Result{Message: "connection refused"}
			}
			return connect.Result{OK: true}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.m = m
	return h
}

// key는 키 이름을 tea.KeyMsg로 바꾼다. 이름이 아니면 글자 입력으로 본다.
func key(s string) tea.KeyMsg {
	named := map[string]tea.KeyType{
		"up": tea.KeyUp, "down": tea.KeyDown, "enter": tea.KeyEnter, "esc": tea.KeyEsc, "tab": tea.KeyTab,
		"shift+tab": tea.KeyShiftTab, "ctrl+c": tea.KeyCtrlC, "ctrl+p": tea.KeyCtrlP, "ctrl+n": tea.KeyCtrlN,
		"ctrl+a": tea.KeyCtrlA, "ctrl+e": tea.KeyCtrlE, "ctrl+d": tea.KeyCtrlD, "ctrl+t": tea.KeyCtrlT,
		"ctrl+r": tea.KeyCtrlR, "backspace": tea.KeyBackspace,
	}
	if k, ok := named[s]; ok {
		return tea.KeyMsg{Type: k}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// press는 키를 차례로 넣고 마지막 명령을 돌려준다.
func (h *harness) press(keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		_, cmd = h.m.Update(key(k))
	}
	return cmd
}

func (h *harness) names() []string {
	var out []string
	for _, s := range h.m.view {
		out = append(out, s.Name)
	}
	return out
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestInitialOrderAndMovement(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	if got := h.names(); !slices.Equal(got, []string{"stg-settle", "prod-ro", "dev"}) {
		t.Fatalf("order = %v (Sort not applied?)", got)
	}
	h.press("down", "ctrl+n", "down") // 끝에서 멈춘다
	if h.m.currentName() != "dev" {
		t.Errorf("current = %s", h.m.currentName())
	}
	h.press("up", "ctrl+p", "ctrl+p")
	if h.m.currentName() != "stg-settle" {
		t.Errorf("current = %s", h.m.currentName())
	}
	if h.m.Init() == nil {
		t.Error("Init returned no command")
	}
}

func TestSearchFiltersAndScores(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	h.press("settle")
	// 이름 일치(stg-settle)가 dbname 일치(prod-ro)보다 먼저 온다.
	if got := h.names(); !slices.Equal(got, []string{"stg-settle", "prod-ro"}) {
		t.Errorf("search settle = %v", got)
	}
	h.press(" prod")
	if got := h.names(); !slices.Equal(got, []string{"prod-ro"}) {
		t.Errorf("search 'settle prod' = %v", got)
	}
	h.press(" nothing")
	if len(h.m.view) != 0 || !strings.Contains(h.m.View(), "일치하는 서비스가 없습니다") {
		t.Error("empty search result not shown")
	}
	h.press("enter") // 고를 것이 없으면 아무 일도 없다
	if h.m.Selected != "" {
		t.Error("selected from an empty list")
	}
}

func TestEnterSelectsAndQuits(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	h.press("down")
	cmd := h.press("enter")
	if h.m.Selected != "prod-ro" || !isQuit(cmd) {
		t.Errorf("selected %q, quit %v", h.m.Selected, isQuit(cmd))
	}
	if h.m.View() != "" {
		t.Error("view should be empty after quitting")
	}
}

func TestEscClearsFilterThenQuits(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	h.press("tab") // 태그 필터 prod
	h.press("x")
	if h.m.tag != "prod" || h.m.search.Value() != "x" {
		t.Fatalf("tag %q, search %q", h.m.tag, h.m.search.Value())
	}
	if cmd := h.press("esc"); isQuit(cmd) || h.m.tag != "" {
		t.Fatal("first esc should clear the tag filter")
	}
	if cmd := h.press("ctrl+c"); isQuit(cmd) || h.m.search.Value() != "" {
		t.Fatal("second key should clear the search")
	}
	if cmd := h.press("esc"); !isQuit(cmd) || h.m.Selected != "" {
		t.Error("esc at top level should quit without selection")
	}
}

func TestTabCyclesTags(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	var seen []string
	for i := 0; i < 4; i++ {
		h.press("tab")
		seen = append(seen, h.m.tag+":"+strings.Join(h.names(), ","))
	}
	want := []string{"prod:prod-ro", "readonly:prod-ro", "stg:stg-settle", ":stg-settle,prod-ro,dev"}
	if !slices.Equal(seen, want) {
		t.Errorf("tag cycle = %v", seen)
	}
	if h.press("tab"); !strings.Contains(h.m.View(), "tag: prod") {
		t.Error("tag filter not shown")
	}
}

func TestDetailPanelAndReveal(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	h.press("down") // prod-ro
	v := h.m.View()
	for _, want := range []string{"db.example.com", "settledb", "readonly", "sslmode", "require", "정산 읽기 전용", passwordMask + " (설정됨)"} {
		if !strings.Contains(v, want) {
			t.Errorf("detail missing %q", want)
		}
	}
	if strings.Contains(v, pwRO) {
		t.Fatal("password shown before reveal")
	}
	h.press("ctrl+r")
	if !strings.Contains(h.m.View(), pwRO) {
		t.Error("ctrl+r did not reveal the password")
	}
	h.press("ctrl+r")
	if strings.Contains(h.m.View(), pwRO) || h.m.revealed != "" {
		t.Error("second ctrl+r did not hide and forget the password")
	}
	h.press("ctrl+r", "down")
	if strings.Contains(h.m.View(), pwRO) || h.m.reveal {
		t.Error("moving the selection should hide the password")
	}
	// 기본값으로 정해진 값은 출처를 표시한다.
	if v := h.m.View(); !strings.Contains(v, "(소켓)") || !strings.Contains(v, "(default)") || !strings.Contains(v, "(없음)") {
		t.Errorf("dev detail:\n%s", v)
	}
	h.press("ctrl+r")
	if h.m.reveal || !strings.Contains(h.m.status, "비밀번호가 없습니다") {
		t.Error("reveal of a service without password")
	}
}

func TestNarrowTerminalHidesDetail(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	h.m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	v := h.m.View()
	// 상세 패널에만 나오는 값(선택한 stg-settle의 dbname)이 보이지 않아야 한다.
	if strings.Contains(v, "settledb") || !strings.Contains(v, "stg-settle") {
		t.Errorf("narrow view:\n%s", v)
	}
}

func TestEmptyState(t *testing.T) {
	h := newHarness(t, "", "")
	if v := h.m.View(); !strings.Contains(v, "Ctrl+A로 첫 서비스를 추가하세요") {
		t.Errorf("empty view:\n%s", v)
	}
	for _, k := range []string{"enter", "ctrl+e", "ctrl+d", "ctrl+t", "ctrl+r", "tab", "down"} {
		if cmd := h.press(k); isQuit(cmd) || h.m.mode != modeList {
			t.Errorf("%s on empty list changed state", k)
		}
	}
}

func TestConnectionTestIsAsync(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	cmd := h.press("ctrl+t") // stg-settle
	if cmd == nil || len(h.tested) != 0 {
		t.Fatal("test should run inside the returned command")
	}
	if !strings.Contains(h.m.View(), "…") {
		t.Error("running mark missing")
	}
	h.m.Update(cmd())
	if v := h.m.View(); !strings.Contains(v, "○") || !strings.Contains(v, "connection refused") {
		t.Errorf("failure not shown:\n%s", v)
	}
	h.press("down")
	h.m.Update(h.press("ctrl+t")())
	if !strings.Contains(h.m.View(), "●") || !strings.Contains(h.m.status, "접속 성공: prod-ro") {
		t.Error("success not shown")
	}
}

func TestDeleteNeedsConfirmation(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	h.press("down", "ctrl+d")
	if h.m.mode != modeConfirmDelete || !strings.Contains(h.m.View(), "삭제할까요? prod-ro") {
		t.Fatal("no confirmation prompt")
	}
	h.press("n")
	if h.m.mode != modeList || len(h.m.all) != 3 {
		t.Fatal("n should cancel")
	}
	h.press("ctrl+d", "y")
	if got := h.names(); slices.Contains(got, "prod-ro") {
		t.Errorf("names after delete = %v", got)
	}
	b, _ := os.ReadFile(h.ppPath)
	if strings.Contains(string(b), "prod-ro") {
		t.Error("managed pgpass entry not removed")
	}
}

// fill은 폼의 현재 필드 값을 지우고 text를 입력한 뒤 다음 필드로 간다.
func (h *harness) fill(text string) {
	in := &h.m.form.inputs[h.m.form.focus]
	in.SetValue("")
	if text != "" {
		h.press(text)
	}
	h.press("tab")
}

func TestAddForm(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	h.press("ctrl+a")
	if h.m.mode != modeForm || !strings.Contains(h.m.View(), "새 서비스") {
		t.Fatal("add form not opened")
	}
	for _, v := range []string{"new-svc", "new.example.com", "5433", "newdb", "app", pwNew, "-c search_path=app", "새 서비스", "dev, test"} {
		h.fill(v)
	}
	if v := h.m.View(); strings.Contains(v, pwNew) {
		t.Fatal("password field is not masked")
	}
	h.press("enter")
	if h.m.mode != modeList || h.m.currentName() != "new-svc" {
		t.Fatalf("mode %v, current %q, form err %v", h.m.mode, h.m.currentName(), h.m.form)
	}
	svc, _ := os.ReadFile(h.svcPath)
	if !strings.HasSuffix(string(svc), "\n# desc: 새 서비스\n# tags: dev, test\n[new-svc]\nhost=new.example.com\nport=5433\ndbname=newdb\nuser=app\noptions=-c search_path=app\n") {
		t.Errorf("service file:\n%s", svc)
	}
	pp, _ := os.ReadFile(h.ppPath)
	if !strings.Contains(string(pp), "# psql-manager:new-svc\nnew.example.com:5433:newdb:app:"+pwNew+"\n") {
		t.Error("pgpass entry missing")
	}
}

func TestAddFormPrefillsPortAndTimezone(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	h.press("ctrl+a")
	f := h.m.form
	if f.inputs[fPort].Value() != "5432" || f.inputs[fOptions].Value() != "-c timezone=Asia/Seoul" {
		t.Fatalf("prefill: port %q, options %q", f.inputs[fPort].Value(), f.inputs[fOptions].Value())
	}
	if v := h.m.View(); !strings.Contains(v, "5432") || !strings.Contains(v, "-c timezone=Asia/Seoul") {
		t.Errorf("prefilled values not shown:\n%s", v)
	}
	// 이름만 넣고 저장하면 미리 넣은 값이 그대로 저장된다.
	h.fill("tz-default")
	h.press("enter")
	svc, _ := os.ReadFile(h.svcPath)
	if !strings.HasSuffix(string(svc), "\n[tz-default]\nport=5432\noptions=-c timezone=Asia/Seoul\n") {
		t.Errorf("service file:\n%s", svc)
	}

	// 사용자가 미리 넣은 값을 고치거나 지울 수 있다.
	h.press("ctrl+a")
	h.fill("tz-custom")
	h.m.form.setFocus(fPort)
	h.fill("6432")
	h.m.form.setFocus(fOptions)
	h.fill("")
	h.press("enter")
	svc, _ = os.ReadFile(h.svcPath)
	if !strings.HasSuffix(string(svc), "\n[tz-custom]\nport=6432\n") {
		t.Errorf("service file:\n%s", svc)
	}
}

func TestAddFormWithoutTimezone(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	h.tz = "" // /etc/localtime에서 시간대를 알 수 없는 경우
	h.press("ctrl+a")
	if f := h.m.form; f.inputs[fOptions].Value() != "" || f.inputs[fPort].Value() != "5432" {
		t.Errorf("options %q, port %q", f.inputs[fOptions].Value(), f.inputs[fPort].Value())
	}
}

func TestEditFormIsNotPrefilled(t *testing.T) {
	// dev에는 port와 options가 없다. 편집 폼은 서비스 값만 보여 주고 기본값을 넣지 않는다.
	h := newHarness(t, baseSvc, basePgpass)
	h.press("down", "down", "ctrl+e")
	if f := h.m.form; f.orig.Name != "dev" || f.inputs[fPort].Value() != "" || f.inputs[fOptions].Value() != "" {
		t.Errorf("edit form of %s: port %q, options %q", f.orig.Name, f.inputs[fPort].Value(), f.inputs[fOptions].Value())
	}
}

func TestDefaultTimezoneSource(t *testing.T) {
	// Timezone이 nil이면 /etc/localtime을 읽는다. 값은 시스템마다 다르므로 형식만 확인한다.
	h := newHarness(t, baseSvc, basePgpass)
	h.m.cfg.Timezone = nil
	h.press("ctrl+a")
	t.Logf("detected timezone on this system: %q", detectTimezone(localtimePath))
	if v := h.m.form.inputs[fOptions].Value(); v != timezoneOption(detectTimezone(localtimePath)) {
		t.Errorf("options = %q", v)
	}
}

func TestAddFormErrorsKeepFormOpen(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	h.press("ctrl+a", "enter")
	if h.m.mode != modeForm || !strings.Contains(h.m.View(), "이름을 입력하세요") {
		t.Fatal("empty name accepted")
	}
	h.fill("prod-ro")
	h.press("enter")
	if h.m.mode != modeForm || !strings.Contains(h.m.form.err, "already exists") {
		t.Errorf("duplicate accepted: %q", h.m.form.err)
	}
	h.press("shift+tab", "up") // 필드 이동은 양방향으로 순환한다
	if h.m.form.focus != fieldCount-1 {
		t.Errorf("focus = %d", h.m.form.focus)
	}
	h.press("esc")
	if h.m.mode != modeList || h.m.status != "취소했습니다" {
		t.Error("esc did not cancel the form")
	}
}

func TestEditFormKeepsPasswordWhenBlank(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	h.press("down", "ctrl+e") // prod-ro
	f := h.m.form
	if f.inputs[fName].Value() != "prod-ro" || f.inputs[fPort].Value() != "5432" || f.inputs[fTags].Value() != "prod, readonly" || f.inputs[fPassword].Value() != "" {
		t.Fatal("edit form not prefilled")
	}
	f.setFocus(fPort)
	h.fill("6432")
	h.press("enter")
	if h.m.mode != modeList {
		t.Fatalf("form error: %s", h.m.form.err)
	}
	pp, _ := os.ReadFile(h.ppPath)
	if string(pp) != "# psql-manager:prod-ro\ndb.example.com:6432:settledb:readonly:"+pwRO+"\n" {
		t.Error("blank password did not keep the existing one, or key not synced")
	}
	svc, _ := os.ReadFile(h.svcPath)
	if !strings.Contains(string(svc), "port=6432\ndbname=settledb\nuser=readonly\nsslmode=require\n") {
		t.Errorf("service file:\n%s", svc)
	}
}

func TestEditFormRenameUnsetAndPassword(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	h.press("down", "ctrl+e")
	f := h.m.form
	h.fill("settle-ro") // 이름
	f.setFocus(fPassword)
	h.fill(pwNew)
	f.setFocus(fDesc)
	h.fill("") // desc 삭제
	h.fill("") // tags 삭제
	h.press("enter")
	if h.m.mode != modeList || h.m.currentName() != "settle-ro" {
		t.Fatalf("mode %v current %q", h.m.mode, h.m.currentName())
	}
	svc, _ := os.ReadFile(h.svcPath)
	if !strings.HasPrefix(string(svc), "[settle-ro]\n") {
		t.Errorf("service file:\n%s", svc)
	}
	pp, _ := os.ReadFile(h.ppPath)
	if string(pp) != "# psql-manager:settle-ro\ndb.example.com:5432:settledb:readonly:"+pwNew+"\n" {
		t.Error("rename or password change not applied to pgpass")
	}

	// 아무것도 바꾸지 않고 저장하면 파일을 건드리지 않는다.
	h.press("ctrl+e", "enter")
	if h.m.status != "바뀐 내용이 없습니다" {
		t.Errorf("status = %q", h.m.status)
	}
	// host를 지우면 키를 지운다. 비밀번호가 있으면 host는 필수가 아니다(localhost로 저장).
	h.press("ctrl+e")
	h.m.form.setFocus(fHost)
	h.fill("")
	h.press("enter")
	if pp, _ := os.ReadFile(h.ppPath); !strings.Contains(string(pp), "localhost:5432:settledb:readonly:") {
		t.Error("unset host not synced to pgpass")
	}
	// 필수값(port)을 지우면 저장이 거부되고 폼이 남는다.
	h.press("ctrl+e")
	h.m.form.setFocus(fPort)
	h.fill("")
	h.press("enter")
	if h.m.mode != modeForm || !strings.Contains(h.m.form.err, "required") {
		t.Errorf("missing port accepted: %q", h.m.form.err)
	}
}

func TestWarningsGoToStatusLine(t *testing.T) {
	h := newHarness(t, baseSvc+"broken line\n", basePgpass)
	if !h.m.statusErr || !strings.Contains(h.m.status, "line 20") {
		t.Errorf("status = %q", h.m.status)
	}
	// 형식 오류가 있으면 쓰기가 거부되고 오류가 보인다.
	h.press("ctrl+d", "y")
	if !strings.Contains(h.m.status, "fix them before editing") {
		t.Errorf("status = %q", h.m.status)
	}
}

func TestOtherMessagesGoToFocusedInput(t *testing.T) {
	h := newHarness(t, baseSvc, basePgpass)
	type blink struct{}
	h.m.Update(blink{})
	h.press("ctrl+a")
	h.m.Update(blink{})
	if h.m.mode != modeForm {
		t.Error("unknown message changed mode")
	}
}

func TestListScrollsToCursor(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString("[svc-" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "]\nport=1\n\n")
	}
	h := newHarness(t, b.String(), "")
	h.m.Update(tea.WindowSizeMsg{Width: 120, Height: 12})
	for i := 0; i < 20; i++ {
		h.press("down")
	}
	if !strings.Contains(h.m.View(), "▶ "+h.m.currentName()) {
		t.Errorf("selected row not visible:\n%s", h.m.View())
	}
}
