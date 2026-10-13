package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// link는 dir 안에 name 링크를 target으로 만든다.
func link(t *testing.T, target, name string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestDetectTimezone(t *testing.T) {
	dir := t.TempDir()
	// macOS: /etc/localtime -> /var/db/timezone/zoneinfo/Asia/Seoul 이고,
	// /var/db/timezone/zoneinfo 자체가 버전 디렉터리를 가리키는 링크다.
	real := filepath.Join(dir, "tz", "2026d.1.0", "zoneinfo")
	os.MkdirAll(filepath.Join(real, "Asia"), 0o700)
	os.WriteFile(filepath.Join(real, "Asia", "Seoul"), nil, 0o600)
	link(t, real, filepath.Join(dir, "db", "zoneinfo"))
	regular := filepath.Join(dir, "copied-localtime")
	os.WriteFile(regular, nil, 0o600)

	cases := []struct {
		name, path, want string
	}{
		{"linux absolute", link(t, "/usr/share/zoneinfo/Asia/Seoul", filepath.Join(dir, "a")), "Asia/Seoul"},
		{"linux relative", link(t, "../usr/share/zoneinfo/Europe/Berlin", filepath.Join(dir, "b")), "Europe/Berlin"},
		{"three levels", link(t, "/usr/share/zoneinfo/America/Argentina/Buenos_Aires", filepath.Join(dir, "c")), "America/Argentina/Buenos_Aires"},
		{"utc", link(t, "/usr/share/zoneinfo/UTC", filepath.Join(dir, "d")), "UTC"},
		{"etc offset", link(t, "/usr/share/zoneinfo/Etc/GMT+9", filepath.Join(dir, "e")), "Etc/GMT+9"},
		{"posix prefix", link(t, "/usr/share/zoneinfo/posix/Asia/Tokyo", filepath.Join(dir, "f")), "Asia/Tokyo"},
		{"macos", link(t, filepath.Join(dir, "db", "zoneinfo", "Asia", "Seoul"), filepath.Join(dir, "g")), "Asia/Seoul"},
		{"not a symlink", regular, ""},
		{"missing", filepath.Join(dir, "nope"), ""},
		{"not zoneinfo", link(t, "/opt/time/Seoul", filepath.Join(dir, "h")), ""},
		{"bad name", link(t, "/usr/share/zoneinfo/../../etc/passwd", filepath.Join(dir, "i")), ""},
		{"empty name", link(t, "/usr/share/zoneinfo/", filepath.Join(dir, "j")), ""},
	}
	for _, c := range cases {
		if got := detectTimezone(c.path); got != c.want {
			t.Errorf("%s: detectTimezone = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTimezoneOption(t *testing.T) {
	if got := timezoneOption("Asia/Seoul"); got != "-c timezone=Asia/Seoul" {
		t.Errorf("timezoneOption = %q", got)
	}
	if got := timezoneOption(""); got != "" {
		t.Errorf("timezoneOption(\"\") = %q", got)
	}
}
