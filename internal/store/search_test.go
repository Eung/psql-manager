package store

import (
	"slices"
	"testing"
)

func svc(name, desc, host, db string, tags ...string) Service {
	return Service{Name: name, Desc: desc, Tags: tags, Params: []Param{{Key: "host", Value: host}, {Key: "dbname", Value: db}}}
}

var searchSet = []Service{
	svc("stg-settle", "스테이징 정산", "stg.example.com", "settledb", "stg"),
	svc("prod-settle-ro", "운영 정산 읽기 전용", "db.example.com", "settledb", "prod", "readonly"),
	svc("settle", "정산 기본", "db.example.com", "settledb"),
	svc("dev-local", "로컬 개발", "", "devdb", "dev"),
	svc("analytics", "Settlement 리포트", "olap.example.com", "dw", "prod"),
}

func names(rs []Result) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Service.Name)
	}
	return out
}

func TestMatch(t *testing.T) {
	cases := []struct {
		query string
		want  []string
	}{
		// 이름 정확히 일치 > 이름 접두어 > 이름 포함 > desc 포함, 같은 점수는 입력 순서 유지
		{"settle", []string{"settle", "stg-settle", "prod-settle-ro", "analytics"}},
		{"SETTLE", []string{"settle", "stg-settle", "prod-settle-ro", "analytics"}},
		// 모든 단어가 들어 있어야 한다
		// analytics는 desc(Settlement)와 태그(prod)에 각각 들어 있어 일치하지만 점수가 낮다.
		{"settle prod", []string{"prod-settle-ro", "analytics"}},
		{"정산 읽기", []string{"prod-settle-ro"}},
		// 이름 접두어(prod-settle-ro)가 태그 일치(analytics)보다 높다
		{"prod", []string{"prod-settle-ro", "analytics"}},
		{"readonly", []string{"prod-settle-ro"}},
		{"read", []string{"prod-settle-ro"}},
		// host, dbname
		{"olap", []string{"analytics"}},
		{"devdb", []string{"dev-local"}},
		{"nothing-matches", nil},
		{"settle nothing", nil},
	}
	for _, c := range cases {
		if got := names(Match(searchSet, c.query)); !slices.Equal(got, c.want) {
			t.Errorf("Match(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

func TestMatchEmptyQueryKeepsOrder(t *testing.T) {
	rs := Match(searchSet, "   ")
	if got := names(rs); !slices.Equal(got, []string{"stg-settle", "prod-settle-ro", "settle", "dev-local", "analytics"}) {
		t.Errorf("order = %v", got)
	}
	for _, r := range rs {
		if r.Score != 0 {
			t.Errorf("%s score = %d", r.Service.Name, r.Score)
		}
	}
}

func TestMatchScores(t *testing.T) {
	score := func(q, name string) int {
		for _, r := range Match(searchSet, q) {
			if r.Service.Name == name {
				return r.Score
			}
		}
		return -1
	}
	if a, b := score("settle", "settle"), score("settle", "stg-settle"); a <= b {
		t.Errorf("exact name %d should beat substring %d", a, b)
	}
	if a, b := score("stg", "stg-settle"), score("prod", "prod-settle-ro"); a != b {
		t.Errorf("name prefix scores differ: %d vs %d", a, b)
	}
	if got := score("settle prod", "prod-settle-ro"); got != scoreNameSub+scoreNamePrefix {
		t.Errorf("multi-word score = %d", got)
	}
}

func TestSearchReadsStore(t *testing.T) {
	e := newEnv(t, baseSvc, basePgpass)
	rs, err := e.st.Search("readonly")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(rs); !slices.Equal(got, []string{"prod-ro"}) {
		t.Errorf("Search = %v", got)
	}
}
