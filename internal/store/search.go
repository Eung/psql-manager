package store

import (
	"sort"
	"strings"
)

// Result는 검색 결과 하나와 그 점수다.
type Result struct {
	Service Service
	Score   int
}

// Search는 서비스 파일을 읽어 Match로 검색한다.
func (s *Store) Search(query string) ([]Result, error) {
	svcs, err := s.List()
	if err != nil {
		return nil, err
	}
	return Match(svcs, query), nil
}

// 단어 하나가 각 필드에 들어 있을 때의 점수. 이름 일치를 가장 높게 친다.
const (
	scoreNameExact  = 100
	scoreNamePrefix = 50
	scoreNameSub    = 30
	scoreTagExact   = 20
	scoreTagSub     = 10
	scoreOther      = 5 // desc, host, dbname
)

// Match는 이름, desc, tags, host, dbname을 대상으로 대소문자를 무시하고
// 공백으로 나눈 단어가 모두 들어 있는 서비스를 고른다. 점수가 높은 순으로
// 정렬하고, 점수가 같으면 입력 순서를 유지한다. 검색어가 비어 있으면
// 모든 서비스를 점수 0으로 돌려준다.
func Match(svcs []Service, query string) []Result {
	words := strings.Fields(strings.ToLower(query))
	var out []Result
	for _, svc := range svcs {
		total := 0
		for _, w := range words {
			sc := wordScore(svc, w)
			if sc == 0 {
				total = -1
				break
			}
			total += sc
		}
		if total >= 0 {
			out = append(out, Result{Service: svc, Score: total})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// wordScore는 단어 w가 서비스의 어느 필드에 들어 있는지에 따라 가장 높은 점수를 준다.
func wordScore(svc Service, w string) int {
	best := 0
	up := func(sc int) {
		if sc > best {
			best = sc
		}
	}
	name := strings.ToLower(svc.Name)
	switch {
	case name == w:
		up(scoreNameExact)
	case strings.HasPrefix(name, w):
		up(scoreNamePrefix)
	case strings.Contains(name, w):
		up(scoreNameSub)
	}
	for _, t := range svc.Tags {
		t = strings.ToLower(t)
		switch {
		case t == w:
			up(scoreTagExact)
		case strings.Contains(t, w):
			up(scoreTagSub)
		}
	}
	for _, v := range []string{svc.Desc, svc.Get("host"), svc.Get("dbname")} {
		if strings.Contains(strings.ToLower(v), w) {
			up(scoreOther)
		}
	}
	return best
}
