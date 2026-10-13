package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// prompt는 stderr에 질문을 쓰고 표준 입력에서 한 줄을 읽는다.
func (a *App) prompt(question string) (string, error) {
	fmt.Fprint(a.Stderr, question)
	line, err := a.in.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		if errors.Is(err, io.EOF) {
			return "", usagef("input ended before %q was answered", strings.TrimSpace(question))
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// promptPassword는 화면에 표시하지 않고 비밀번호를 두 번 받아 같은지 확인한다.
// allowEmpty면 처음에 빈 값을 입력했을 때 확인 없이 ""를 돌려준다.
func (a *App) promptPassword(question string, allowEmpty bool) (string, error) {
	read := func(q string) (string, error) {
		fmt.Fprint(a.Stderr, q)
		pw, err := a.ReadPassword()
		fmt.Fprintln(a.Stderr)
		return pw, err
	}
	pw, err := read(question)
	if err != nil {
		return "", err
	}
	if pw == "" {
		if allowEmpty {
			return "", nil
		}
		return "", usagef("empty password")
	}
	again, err := read("Retype password: ")
	if err != nil {
		return "", err
	}
	if again != pw {
		return "", usagef("passwords do not match")
	}
	return pw, nil
}

// readPasswordStdin은 표준 입력의 첫 줄을 비밀번호로 읽는다. 끝의 개행은 값에
// 넣지 않고, 두 줄 이상이면 거부한다.
func (a *App) readPasswordStdin() (string, error) {
	b, err := io.ReadAll(a.in)
	if err != nil {
		return "", err
	}
	pw := strings.TrimSuffix(string(b), "\n")
	pw = strings.TrimSuffix(pw, "\r")
	if strings.ContainsAny(pw, "\r\n") {
		return "", usagef("--password-stdin expects a single line")
	}
	if pw == "" {
		return "", usagef("--password-stdin got an empty password")
	}
	return pw, nil
}

// confirm은 y/N 질문을 하고 y 또는 yes일 때만 true를 돌려준다.
func (a *App) confirm(question string) (bool, error) {
	ans, err := a.prompt(question + " [y/N]: ")
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
