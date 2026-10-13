//go:build integration

package main

import (
	"crypto/rand"
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// 실제 PostgreSQL 컨테이너에 접속하는 선택 테스트다. make integration으로 실행한다.
// Docker와 psql이 없으면 건너뛴다.

const pgImage = "postgres:16-alpine"

func startPostgres(t *testing.T, password string) (port string) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker is not available")
	}
	// 이미지를 처음 받을 때의 진행 메시지는 stderr로 나오므로 컨테이너 ID는 stdout에서만 읽는다.
	var stderr strings.Builder
	run := exec.Command("docker", "run", "-d", "--rm",
		"-e", "POSTGRES_USER=app", "-e", "POSTGRES_PASSWORD="+password, "-e", "POSTGRES_DB=appdb",
		"-p", "127.0.0.1::5432", pgImage)
	run.Stderr = &stderr
	out, err := run.Output()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, stderr.String())
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })

	out, err = exec.Command("docker", "port", id, "5432/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	addr := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	port = addr[strings.LastIndex(addr, ":")+1:]

	// 컨테이너 안의 초기화가 끝나 TCP로 접속을 받을 때까지 기다린다.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("docker", "exec", id, "pg_isready", "-h", "127.0.0.1", "-U", "app").Run() == nil {
			return port
		}
		time.Sleep(time.Second)
	}
	t.Fatal("postgres did not become ready")
	return ""
}

func TestRealPostgres(t *testing.T) {
	psql, err := exec.LookPath("psql")
	if err != nil {
		t.Skip("psql is not installed")
	}
	// 이스케이프가 필요한 ':'와 '\'를 넣은 무작위 비밀번호. 어디에도 출력하지 않는다.
	b := make([]byte, 12)
	rand.Read(b)
	password := "p:" + hex.EncodeToString(b) + `\x`
	port := startPostgres(t, password)

	sb := newSandbox(t)
	sb.env = append(sb.env, "PSQL_MANAGER_PSQL="+psql)

	if code, _, stderr := sb.run(t, password+"\n", "add", "real", "--host", "127.0.0.1", "--port", port,
		"--db", "appdb", "--user", "app", "--password-stdin"); code != 0 {
		t.Fatalf("add: exit %d: %s", code, stderr)
	}
	if code, stdout, stderr := sb.run(t, "", "test", "real"); code != 0 || stdout != "ok  real\n" {
		t.Fatalf("test: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// 터미널 없이 접속하므로 비밀번호를 물어야 했다면 실패한다.
	code, stdout, stderr := sb.run(t, "", "connect", "real", "--", "-X", "-At", "-c", "select 42")
	if code != 0 || strings.TrimSpace(stdout) != "42" {
		t.Fatalf("connect: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, password) {
		t.Error("password leaked to output")
	}

	// 같은 host:port:dbname:user로 비밀번호 없이 추가한 서비스는 real의 줄을 함께 쓴다(shared).
	if code, _, stderr := sb.run(t, "", "add", "shared", "--host", "127.0.0.1", "--port", port,
		"--db", "appdb", "--user", "app"); code != 0 {
		t.Fatalf("add shared: %s", stderr)
	}
	if code, stdout, _ := sb.run(t, "", "show", "shared"); code != 0 || !strings.Contains(stdout, `(shared with "real", `) {
		t.Errorf("show shared: exit %d\n%s", code, stdout)
	}
	if code, stdout, _ := sb.run(t, "", "test", "shared"); code != 0 {
		t.Errorf("test shared: exit %d, stdout %q", code, stdout)
	}

	// 비교군: 맞는 .pgpass 줄이 없는 서비스는 비밀번호가 없어 접속에 실패해야 한다.
	// .pgpass는 host를 글자 그대로 비교하므로 localhost는 127.0.0.1 줄과 맞지 않는다.
	if code, _, stderr := sb.run(t, "", "add", "nopw", "--host", "localhost", "--port", port,
		"--db", "appdb", "--user", "app"); code != 0 {
		t.Fatalf("add nopw: %s", stderr)
	}
	if code, stdout, _ := sb.run(t, "", "test", "nopw"); code != 5 || !strings.Contains(stdout, "no password supplied") {
		t.Errorf("test nopw: exit %d, stdout %q", code, stdout)
	}
}
