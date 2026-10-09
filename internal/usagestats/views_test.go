package usagestats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validViewsBody = `{"views":[{"id":"spend","name":"Spend","panels":[{"type":"tokens","options":{"format":"lines"}},{"type":"cost"}],"columns":["tokens","cost"],"window":"last7d","accounts":null,"builtin":false},{"id":"allowance","name":"Allowance","panels":[],"columns":[],"window":"around_now","accounts":["claude-a"],"builtin":true}],"default":"spend"}`

func TestDecodeViewsAcceptsTheContract(t *testing.T) {
	views, errDecode := DecodeViews(strings.NewReader(validViewsBody))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(views.Views) != 2 || views.Default != "spend" || views.Views[1].Accounts[0] != "claude-a" || !views.Views[1].Builtin {
		t.Fatalf("decoded %+v", views)
	}
	if views.Views[0].Accounts != nil {
		t.Fatal("null accounts decoded as a list")
	}
}

// Every rule in the contract rejects a body that breaks it.
func TestDecodeViewsRejectsEachRule(t *testing.T) {
	view := func(fields string) string {
		return `{"views":[{` + fields + `}],"default":""}`
	}
	base := `"id":"v","name":"V","panels":[],"columns":[],"window":"last24h","accounts":null,"builtin":false`
	replace := func(old, new string) string { return view(strings.Replace(base, old, new, 1)) }
	many := make([]string, MaxViews+1)
	for i := range many {
		many[i] = fmt.Sprintf(`{"id":"v%d","name":"V","panels":[],"columns":[],"window":"last24h","accounts":null,"builtin":false}`, i)
	}
	for name, body := range map[string]string{
		"not json":               `{"views":`,
		"null body":              `null`,
		"views missing":          `{"default":""}`,
		"views not an array":     `{"views":{},"default":""}`,
		"unknown top field":      `{"views":[],"default":"","extra":1}`,
		"trailing data":          `{"views":[],"default":""} {}`,
		"too many views":         `{"views":[` + strings.Join(many, ",") + `],"default":""}`,
		"bad default":            `{"views":[],"default":"Not An Id"}`,
		"empty id":               replace(`"id":"v"`, `"id":""`),
		"id with capitals":       replace(`"id":"v"`, `"id":"Spend"`),
		"id with underscore":     replace(`"id":"v"`, `"id":"my_view"`),
		"id with space":          replace(`"id":"v"`, `"id":"my view"`),
		"id 41 chars":            replace(`"id":"v"`, `"id":"`+strings.Repeat("a", 41)+`"`),
		"id not a string":        replace(`"id":"v"`, `"id":7`),
		"duplicate id":           `{"views":[{` + base + `},{` + base + `}],"default":""}`,
		"empty name":             replace(`"name":"V"`, `"name":""`),
		"blank name":             replace(`"name":"V"`, `"name":"   "`),
		"name 61 chars":          replace(`"name":"V"`, `"name":"`+strings.Repeat("é", 61)+`"`),
		"panels missing":         replace(`"panels":[],`, ``),
		"panels null":            replace(`"panels":[]`, `"panels":null`),
		"unknown panel type":     replace(`"panels":[]`, `"panels":[{"type":"latte","options":{}}]`),
		"panel without type":     replace(`"panels":[]`, `"panels":[{"options":{}}]`),
		"panel unknown field":    replace(`"panels":[]`, `"panels":[{"type":"tokens","options":{},"size":2}]`),
		"panel options array":    replace(`"panels":[]`, `"panels":[{"type":"tokens","options":[1]}]`),
		"panel options string":   replace(`"panels":[]`, `"panels":[{"type":"tokens","options":"lines"}]`),
		"columns missing":        replace(`"columns":[],`, ``),
		"columns not strings":    replace(`"columns":[]`, `"columns":[1]`),
		"window missing":         replace(`"window":"last24h",`, ``),
		"window unknown":         replace(`"window":"last24h"`, `"window":"last30d"`),
		"accounts not strings":   replace(`"accounts":null`, `"accounts":[1]`),
		"accounts a string":      replace(`"accounts":null`, `"accounts":"claude-a"`),
		"builtin not a bool":     replace(`"builtin":false`, `"builtin":"no"`),
		"unknown view field":     replace(`"builtin":false`, `"builtin":false,"colour":"red"`),
		"body over 64 KB":        replace(`"columns":[]`, `"columns":["`+strings.Repeat("a", MaxViewsBody)+`"]`),
		"default not a string":   `{"views":[],"default":3}`,
		"view not an object":     `{"views":["spend"],"default":""}`,
		"panels not an array":    replace(`"panels":[]`, `"panels":{}`),
		"columns null":           replace(`"columns":[]`, `"columns":null`),
		"panel options a number": replace(`"panels":[]`, `"panels":[{"type":"cost","options":5}]`),
		"panel type wrong case":  replace(`"panels":[]`, `"panels":[{"type":"Tokens"}]`),
		"window wrong case":      replace(`"window":"last24h"`, `"window":"Last24h"`),
		"id with newline":        replace(`"id":"v"`, `"id":"v\n"`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, errDecode := DecodeViews(strings.NewReader(body)); errDecode == nil {
				t.Fatal("accepted")
			}
		})
	}
	// The limits themselves are allowed.
	edge := replace(`"id":"v"`, `"id":"`+strings.Repeat("a", 40)+`"`)
	edge = strings.Replace(edge, `"name":"V"`, `"name":"`+strings.Repeat("é", 60)+`"`, 1)
	if _, errDecode := DecodeViews(strings.NewReader(edge)); errDecode != nil {
		t.Fatalf("40 char id and 60 char name refused: %v", errDecode)
	}
	if _, errDecode := DecodeViews(strings.NewReader(`{"views":[` + strings.Join(many[:MaxViews], ",") + `],"default":"v3"}`)); errDecode != nil {
		t.Fatalf("50 views refused: %v", errDecode)
	}
}

func TestViewsFileRoundTripModeAndAtomicity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ViewsFileName)

	empty, errRead := ReadViews(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if data, _ := json.Marshal(empty); string(data) != `{"views":[],"default":""}` {
		t.Fatalf("empty views = %s", data)
	}

	views, errDecode := DecodeViews(strings.NewReader(validViewsBody))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	stored, errWrite := WriteViews(path, views)
	if errWrite != nil {
		t.Fatal(errWrite)
	}
	if string(stored.Views[0].Panels[1].Options) != `{}` {
		t.Fatalf("missing options stored as %s, want {}", stored.Views[0].Panels[1].Options)
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatal(errStat)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode %o, want 600", mode)
	}
	if _, errTmp := os.Stat(path + ".tmp"); !os.IsNotExist(errTmp) {
		t.Fatal("temporary file left behind")
	}
	read, errRead := ReadViews(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	want, _ := json.Marshal(stored)
	got, _ := json.Marshal(read)
	if string(got) != string(want) {
		t.Fatalf("read back %s, want %s", got, want)
	}

	// A write that cannot finish leaves the old file whole.
	if errMkdir := os.Mkdir(path+".tmp", 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	if _, errWrite := WriteViews(path, DashboardViews{Views: []DashboardView{}}); errWrite == nil {
		t.Fatal("write through a blocked temporary file succeeded")
	}
	again, errRead := ReadViews(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if got, _ := json.Marshal(again); string(got) != string(want) {
		t.Fatalf("after a failed write the file holds %s", got)
	}

	// An invalid set is never written.
	if _, errWrite := WriteViews(filepath.Join(dir, "other.json"), DashboardViews{}); errWrite == nil {
		t.Fatal("nil views written")
	}
	if _, errStat := os.Stat(filepath.Join(dir, "other.json")); !os.IsNotExist(errStat) {
		t.Fatal("invalid views reached the disk")
	}

	// A damaged file is an error, not an empty set.
	if errWrite := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, errRead := ReadViews(filepath.Join(dir, "bad.json")); errRead == nil {
		t.Fatal("damaged file read as views")
	}
}

func TestViewsPathSitsNextToStatsFile(t *testing.T) {
	store := newStore()
	if store.ViewsPath() != "" {
		t.Fatal("views path without a stats file")
	}
	store.path = filepath.Join("/data", "usage-stats.json")
	if got := store.ViewsPath(); got != filepath.Join("/data", ViewsFileName) {
		t.Fatalf("views path %q", got)
	}
}

// largestValidViews builds the biggest views document the contract allows,
// within a few bytes of MaxViewsBody when sent compact: 50 views of 12
// panels with 40 character ids and 60 character names, padded with account
// ids.
func largestValidViews(t *testing.T) []byte {
	t.Helper()
	types := []string{"allowance", "available", "tokens", "cost", "requests", "output", "cache", "ttft", "latency", "throughput", "failures", "activity"}
	views := DashboardViews{Default: strings.Repeat("v", 38) + "00"}
	for i := 0; i < MaxViews; i++ {
		view := DashboardView{
			ID:       fmt.Sprintf("%s%02d", strings.Repeat("v", 38), i),
			Name:     strings.Repeat("é", 59) + "x",
			Columns:  []string{"account", "tokens", "requests", "input", "cache_write", "cache_read", "output", "cost", "cache_reuse"},
			Window:   "around_now",
			Accounts: []string{},
			Builtin:  i%2 == 0,
		}
		for _, panelType := range types {
			view.Panels = append(view.Panels, ViewPanel{Type: panelType, Options: json.RawMessage(`{"format":"bars","mode":"weekly"}`)})
		}
		views.Views = append(views.Views, view)
	}
	size := func() int {
		data, errMarshal := json.Marshal(views)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		return len(data)
	}
	for i := 0; ; i++ {
		view := &views.Views[i%MaxViews]
		view.Accounts = append(view.Accounts, fmt.Sprintf("claude-%040d", i))
		if size() > MaxViewsBody {
			view.Accounts = view.Accounts[:len(view.Accounts)-1]
			break
		}
	}
	data, _ := json.Marshal(views)
	if len(data) < MaxViewsBody-64 || len(data) > MaxViewsBody {
		t.Fatalf("largest body is %d bytes, want just under %d", len(data), MaxViewsBody)
	}
	return data
}

// The largest request the server accepts must read back after it is saved.
func TestLargestValidViewsSaveAndReadBack(t *testing.T) {
	body := largestValidViews(t)
	views, errDecode := DecodeViews(strings.NewReader(string(body)))
	if errDecode != nil {
		t.Fatalf("largest valid body refused: %v", errDecode)
	}
	path := filepath.Join(t.TempDir(), ViewsFileName)
	stored, errWrite := WriteViews(path, views)
	if errWrite != nil {
		t.Fatal(errWrite)
	}
	read, errRead := ReadViews(path)
	if errRead != nil {
		t.Fatalf("saved views do not read back: %v", errRead)
	}
	got, _ := json.Marshal(read)
	want, _ := json.Marshal(stored)
	if string(got) != string(want) || string(got) != string(body) {
		t.Fatalf("read back %d bytes, want the %d bytes sent", len(got), len(body))
	}
	if data, _ := os.ReadFile(path); len(data) != len(body) {
		t.Fatalf("file is %d bytes, want the %d compact bytes sent", len(data), len(body))
	}

	// A browser sends <, > and & raw, and encoding/json stores each as a six
	// byte escape, so the file can be several times the request.
	raw := `{"views":[{"id":"v","name":"<&>","panels":[{"type":"cost"}],"columns":[],"window":"last24h","accounts":["` +
		strings.Repeat("&", MaxViewsBody-200) + `"],"builtin":false}],"default":""}`
	escaped, errDecode := DecodeViews(strings.NewReader(raw))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if _, errWrite := WriteViews(path, escaped); errWrite != nil {
		t.Fatal(errWrite)
	}
	back, errRead := ReadViews(path)
	if errRead != nil {
		t.Fatalf("escaped views do not read back: %v", errRead)
	}
	if back.Views[0].Accounts[0] != escaped.Views[0].Accounts[0] || back.Views[0].Name != "<&>" {
		t.Fatal("escaped views read back changed")
	}
}
