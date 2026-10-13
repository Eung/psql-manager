package connect

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// TestTimeout은 접속 테스트의 접속 제한 시간(초)이다.
const TestTimeout = 3

// testDeadline은 접속 테스트 프로세스 전체에 주는 최대 시간이다. psql이 접속
// 제한 시간을 지키지 않는 경우를 대비한 안전장치다.
const testDeadline = 15 * time.Second

// Argv는 psql "service=<name>" [extra...]로 실행할 인자를 만든다.
// PGSERVICE 환경변수는 쓰지 않고 접속 문자열로 서비스를 넘긴다.
func Argv(psql, service string, extra []string) []string {
	return append([]string{psql, "service=" + service}, extra...)
}

// Exec는 현재 프로세스를 psql로 교체한다. 성공하면 돌아오지 않는다.
func Exec(psql, service string, extra []string) error {
	return syscall.Exec(psql, Argv(psql, service, extra), os.Environ())
}

// TestArgv는 접속 테스트에 쓰는 psql 인자다. -w로 비밀번호 프롬프트를 막고,
// -X로 사용자의 .psqlrc가 출력에 섞이지 않게 한다.
func TestArgv(service string) []string {
	return []string{
		"service=" + service + " connect_timeout=" + strconv.Itoa(TestTimeout),
		"-w", "-X", "-A", "-t", "-c", "select 1",
	}
}

// Result는 접속 테스트 결과다.
type Result struct {
	OK       bool
	Message  string // 실패 이유(psql 오류의 첫 줄 등)
	Duration time.Duration
}

// Test는 psql을 자식 프로세스로 실행해 select 1이 되는지 확인한다.
// 출력이 "1"이고 종료 코드가 0일 때만 성공으로 본다.
func Test(ctx context.Context, psql, service string) Result {
	ctx, cancel := context.WithTimeout(ctx, testDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, psql, TestArgv(service)...)
	cmd.Env = append(os.Environ(), "PGCONNECT_TIMEOUT="+strconv.Itoa(TestTimeout))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// 시간 초과로 psql을 종료한 뒤 자식 프로세스가 파이프를 쥐고 있어도 기다림이 끝나게 한다.
	cmd.WaitDelay = time.Second
	start := time.Now()
	err := cmd.Run()
	res := Result{Duration: time.Since(start)}
	if err == nil && strings.TrimSpace(stdout.String()) == "1" {
		res.OK = true
		return res
	}
	switch {
	case ctx.Err() != nil:
		res.Message = "timed out"
	case firstLine(stderr.String()) != "":
		res.Message = firstLine(stderr.String())
	case err != nil:
		res.Message = err.Error()
	default:
		res.Message = "unexpected output from psql"
	}
	return res
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}
