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
	link := func(target string) func(string) (string, error) {
		return func(string) (string, error) { return target, nil }
	}
	env := func(v string) func(string) string { return func(string) string { return v } }

	cases := []struct {
		name     string
		loc      *time.Location
		getenv   func(string) string
		readlink func(string) (string, error)
		want     string
	}{
		{"named zone kept", london, noEnv, noLink, "Europe/London"},
		{"TZ wins", local, env("Europe/London"), link("/usr/share/zoneinfo/Asia/Tokyo"), "Europe/London"},
		{"TZ with colon", local, env(":Europe/London"), noLink, "Europe/London"},
		{"localtime link", local, noEnv, link("/usr/share/zoneinfo/Europe/London"), "Europe/London"},
		{"macOS link", local, noEnv, link("/var/db/timezone/zoneinfo/America/New_York"), "America/New_York"},
		{"bad TZ falls to link", local, env("Not/AZone"), link("../usr/share/zoneinfo/Europe/London"), "Europe/London"},
		{"nothing known", local, noEnv, noLink, "Local"},
	}
	for _, c := range cases {
		if got := resolveZoneName(c.loc, c.getenv, c.readlink); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
