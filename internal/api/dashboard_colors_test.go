package api

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Grey means muted or off on the dashboard, so an account drawn in grey looks
// switched off. Every account colour must resolve to a real hue in every theme.
func TestAccountColoursAreNeverGrey(t *testing.T) {
	js, errJS := dashboardFiles.ReadFile("dashboard/screens/common.js")
	if errJS != nil {
		t.Fatalf("read common.js: %v", errJS)
	}
	css, errCSS := dashboardFiles.ReadFile("dashboard/app.css")
	if errCSS != nil {
		t.Fatalf("read app.css: %v", errCSS)
	}
	list := regexp.MustCompile(`ACCOUNT_COLORS = \[([^\]]*)\]`).FindSubmatch(js)
	if list == nil {
		t.Fatal("ACCOUNT_COLORS not found in common.js")
	}
	colours := regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(list[1], -1)
	if len(colours) == 0 {
		t.Fatal("ACCOUNT_COLORS is empty")
	}
	// The light theme, the dark media query and the forced dark theme.
	themes := regexp.MustCompile(`\{([^{}]*--chart-1:[^{}]*)\}`).FindAllSubmatch(css, -1)
	if len(themes) != 3 {
		t.Fatalf("found %d theme blocks in app.css, want 3", len(themes))
	}
	decl := regexp.MustCompile(`(--[\w-]+):\s*(#[0-9A-Fa-f]{6})`)
	for i, theme := range themes {
		vars := map[string]string{}
		for _, m := range decl.FindAllSubmatch(theme[1], -1) {
			vars[string(m[1])] = string(m[2])
		}
		for _, c := range colours {
			value := string(c[1])
			if name, ok := strings.CutPrefix(value, "var("); ok {
				value = vars[strings.TrimSuffix(name, ")")]
				if value == "" {
					t.Errorf("theme %d: %s is not defined", i, c[1])
					continue
				}
			}
			if isGrey(t, value) {
				t.Errorf("theme %d: account colour %s resolves to grey %s", i, c[1], value)
			}
		}
	}
}

func isGrey(t *testing.T, hex string) bool {
	t.Helper()
	if len(hex) < 7 || hex[0] != '#' {
		t.Fatalf("not a hex colour: %q", hex)
	}
	channel := func(s string) int {
		v, errParse := strconv.ParseUint(s, 16, 8)
		if errParse != nil {
			t.Fatalf("bad hex colour %q: %v", hex, errParse)
		}
		return int(v)
	}
	r, g, b := channel(hex[1:3]), channel(hex[3:5]), channel(hex[5:7])
	return max(r, g, b)-min(r, g, b) < 24
}
