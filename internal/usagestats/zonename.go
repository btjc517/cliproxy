package usagestats

import (
	"os"
	"strings"
	"time"
)

// zoneName returns an IANA name for loc that browsers accept. Go names the
// zone it reads from /etc/localtime "Local", which Intl rejects, so the
// dashboard would fall back to the viewer's own zone and shift every day.
func zoneName(loc *time.Location) string {
	return resolveZoneName(loc, os.Getenv, os.Readlink, os.ReadFile)
}

func resolveZoneName(loc *time.Location, getenv func(string) string, readlink func(string) (string, error), readFile func(string) ([]byte, error)) string {
	name := loc.String()
	if name != "Local" {
		return name
	}
	if tz := strings.TrimPrefix(getenv("TZ"), ":"); tz != "" && tz != "Local" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if target, err := readlink("/etc/localtime"); err == nil {
		if i := strings.LastIndex(target, "zoneinfo/"); i >= 0 {
			tz := target[i+len("zoneinfo/"):]
			if _, err := time.LoadLocation(tz); err == nil {
				return tz
			}
		}
	}
	// Debian and Ubuntu name the zone here when /etc/localtime is a copy.
	if raw, err := readFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(raw)); tz != "" && tz != "Local" {
			if _, err := time.LoadLocation(tz); err == nil {
				return tz
			}
		}
	}
	return name
}
