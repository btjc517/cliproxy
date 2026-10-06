package api

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Grey means muted or off on the dashboard, so an account drawn in grey looks
// switched off. Every account colour must be a real hue in every theme.
func TestAccountColoursAreNeverGrey(t *testing.T) {
	js, errJS := dashboardFiles.ReadFile("dashboard/screens/common.js")
	if errJS != nil {
		t.Fatalf("read common.js: %v", errJS)
	}
	css, errCSS := dashboardFiles.ReadFile("dashboard/app.css")
	if errCSS != nil {
		t.Fatalf("read app.css: %v", errCSS)
	}
	list := regexp.MustCompile(`ACCOUNT_COLORS\s*=\s*\[([^\]]*)\]`).FindSubmatch(js)
	if list == nil {
		t.Fatal("ACCOUNT_COLORS not found in common.js")
	}
	// Every declaration of each custom property, in any block, so a theme or
	// later override cannot slip a grey past the check. Comments go first;
	// a declaration ends at a semicolon or at the end of its block.
	css = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAll(css, nil)
	decls := map[string][]string{}
	for _, m := range regexp.MustCompile(`(--[\w-]+)\s*:\s*([^;}]+)`).FindAllSubmatch(css, -1) {
		decls[string(m[1])] = append(decls[string(m[1])], strings.TrimSpace(string(m[2])))
	}
	entries := strings.Split(string(list[1]), ",")
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if len(entry) < 2 || !strings.ContainsRune(`"'`+"`", rune(entry[0])) || entry[len(entry)-1] != entry[0] {
			t.Fatalf("ACCOUNT_COLORS entry %s is not a plain string", entry)
		}
		colour := entry[1 : len(entry)-1]
		values := []string{colour}
		if name, ok := strings.CutPrefix(colour, "var("); ok {
			name = strings.TrimSuffix(name, ")")
			values = decls[name]
			if len(values) == 0 {
				t.Errorf("account colour %s is not defined in app.css", colour)
			}
		}
		for _, v := range values {
			if isGrey(t, v) {
				t.Errorf("account colour %s resolves to grey %s", colour, v)
			}
		}
	}
}

// isGrey fails the test on anything but opaque #RRGGBB, so a colour in another
// notation, or a transparent one, cannot pass unchecked.
func isGrey(t *testing.T, value string) bool {
	t.Helper()
	if !regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`).MatchString(value) {
		t.Fatalf("account colour %q is not an opaque #RRGGBB colour", value)
	}
	channel := func(s string) int {
		v, _ := strconv.ParseUint(s, 16, 8)
		return int(v)
	}
	r, g, b := channel(value[1:3]), channel(value[3:5]), channel(value[5:7])
	return max(r, g, b)-min(r, g, b) < 24
}
