package usagestats

import (
	"errors"
	"testing"
	"time"
)

func TestResolveZoneName(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skip("no zoneinfo")
	}
	local := time.FixedZone("Local", 0)
	noEnv := func(string) string { return "" }
	noLink := func(string) (string, error) { return "", errors.New("none") }
	noFile := func(string) ([]byte, error) { return nil, errors.New("none") }
	file := func(v string) func(string) ([]byte, error) {
		return func(string) ([]byte, error) { return []byte(v), nil }
	}
	link := func(target string) func(string) (string, error) {
		return func(string) (string, error) { return target, nil }
	}
	env := func(v string) func(string) string { return func(string) string { return v } }

	cases := []struct {
		name     string
		loc      *time.Location
		getenv   func(string) string
		readlink func(string) (string, error)
		readFile func(string) ([]byte, error)
		want     string
	}{
		{"named zone kept", london, noEnv, noLink, noFile, "Europe/London"},
		{"TZ wins", local, env("Europe/London"), link("/usr/share/zoneinfo/Asia/Tokyo"), noFile, "Europe/London"},
		{"TZ with colon", local, env(":Europe/London"), noLink, noFile, "Europe/London"},
		{"localtime link", local, noEnv, link("/usr/share/zoneinfo/Europe/London"), noFile, "Europe/London"},
		{"macOS link", local, noEnv, link("/var/db/timezone/zoneinfo/America/New_York"), noFile, "America/New_York"},
		{"bad TZ falls to link", local, env("Not/AZone"), link("../usr/share/zoneinfo/Europe/London"), noFile, "Europe/London"},
		{"etc timezone when localtime is a copy", local, noEnv, noLink, file("Europe/London\n"), "Europe/London"},
		{"bad etc timezone", local, noEnv, noLink, file("Mars/Base\n"), "Local"},
		{"nothing known", local, noEnv, noLink, noFile, "Local"},
	}
	for _, c := range cases {
		if got := resolveZoneName(c.loc, c.getenv, c.readlink, c.readFile); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
