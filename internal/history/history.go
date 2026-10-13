// Package history는 접속 이력을 기록하고 읽는다.
package history

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Eung/psql-manager/internal/fsutil"
)

// DefaultMax는 파일에 남기는 최대 줄 수다.
const DefaultMax = 1000

// Entry는 이력 한 줄이다.
type Entry struct {
	Time int64 // 유닉스 시각(초)
	Name string
}

// DefaultPath는 $XDG_STATE_HOME/psql-manager/history를, XDG_STATE_HOME이 없거나
// 절대 경로가 아니면 ~/.local/state/psql-manager/history를 돌려준다.
func DefaultPath(getenv func(string) string) (string, error) {
	if d := getenv("XDG_STATE_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "psql-manager", "history"), nil
	}
	home := getenv("HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("history: locate home directory: %w", err)
		}
		home = h
	}
	return filepath.Join(home, ".local", "state", "psql-manager", "history"), nil
}

// History는 이력 파일 하나다.
type History struct {
	Path string
	Max  int              // 0이면 DefaultMax
	Now  func() time.Time // nil이면 time.Now
}

// Load는 이력을 파일 순서(오래된 것부터)대로 읽는다. 파일이 없으면 빈 이력이고,
// 형식이 맞지 않는 줄은 건너뛴다.
func (h *History) Load() ([]Entry, error) {
	b, err := os.ReadFile(h.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, line := range strings.Split(string(b), "\n") {
		ts, name, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "\t")
		if !ok || name == "" {
			continue
		}
		t, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, Entry{Time: t, Name: name})
	}
	return out, nil
}

// Record는 name을 지금 시각으로 한 줄 추가하고 최근 Max줄만 남긴다. 파일은
// fsutil로 원자적으로 교체한다.
func (h *History) Record(name string) error {
	if name == "" || strings.ContainsAny(name, "\t\r\n") {
		return fmt.Errorf("history: invalid service name %q", name)
	}
	if err := os.MkdirAll(filepath.Dir(h.Path), 0o700); err != nil {
		return err
	}
	entries, err := h.Load()
	if err != nil {
		return err
	}
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	entries = append(entries, Entry{Time: now().Unix(), Name: name})
	max := h.Max
	if max <= 0 {
		max = DefaultMax
	}
	if len(entries) > max {
		entries = entries[len(entries)-max:]
	}
	var b bytes.Buffer
	for _, e := range entries {
		fmt.Fprintf(&b, "%d\t%s\n", e.Time, e.Name)
	}
	p, err := fsutil.Prepare(h.Path, b.Bytes())
	if err != nil {
		return err
	}
	return p.Commit()
}

// LastUsed는 서비스마다 마지막 접속 시각을 돌려준다. 같은 시각이면 파일에서
// 뒤에 있는 줄을 더 최근으로 보도록 순번도 함께 담는다.
func LastUsed(entries []Entry) map[string]Use {
	out := map[string]Use{}
	for i, e := range entries {
		out[e.Name] = Use{Time: e.Time, Seq: i}
	}
	return out
}

// Use는 서비스 하나의 마지막 접속이다.
type Use struct {
	Time int64
	Seq  int // 이력 파일에서의 순번
}

// Sort는 names를 최근 접속 순으로 정렬한다. 이력이 없는 이름은 이름순으로 뒤에 둔다.
func Sort(names []string, last map[string]Use) {
	sort.SliceStable(names, func(i, j int) bool {
		a, aok := last[names[i]]
		b, bok := last[names[j]]
		switch {
		case aok && bok:
			if a.Time != b.Time {
				return a.Time > b.Time
			}
			return a.Seq > b.Seq
		case aok != bok:
			return aok
		default:
			return names[i] < names[j]
		}
	})
}
