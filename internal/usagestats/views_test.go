package usagestats

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		"updated_at negative":    replace(`"builtin":false`, `"builtin":false,"updated_at":-1`),
		"updated_at fraction":    replace(`"builtin":false`, `"builtin":false,"updated_at":1.5`),
		"updated_at a string":    replace(`"builtin":false`, `"builtin":false,"updated_at":"1700000000000"`),
		"updated_at past 2^53":   replace(`"builtin":false`, `"builtin":false,"updated_at":9007199254740992`),
		"revision a string":      `{"views":[],"default":"","revision":"3"}`,
		"deleted not an array":   `{"views":[],"default":"","deleted":{}}`,
		"deleted entry a string": `{"views":[],"default":"","deleted":["spend"]}`,
		"deleted id missing":     `{"views":[],"default":"","deleted":[{"updated_at":1}]}`,
		"deleted id capitals":    `{"views":[],"default":"","deleted":[{"id":"Spend","updated_at":1}]}`,
		"deleted id 41 chars":    `{"views":[],"default":"","deleted":[{"id":"` + strings.Repeat("a", 41) + `","updated_at":1}]}`,
		"deleted time missing":   `{"views":[],"default":"","deleted":[{"id":"spend"}]}`,
		"deleted time zero":      `{"views":[],"default":"","deleted":[{"id":"spend","updated_at":0}]}`,
		"deleted time negative":  `{"views":[],"default":"","deleted":[{"id":"spend","updated_at":-5}]}`,
		"deleted time fraction":  `{"views":[],"default":"","deleted":[{"id":"spend","updated_at":1.5}]}`,
		"deleted time a string":  `{"views":[],"default":"","deleted":[{"id":"spend","updated_at":"1"}]}`,
		"deleted time past 2^53": `{"views":[],"default":"","deleted":[{"id":"spend","updated_at":9007199254740992}]}`,
		"deleted unknown field":  `{"views":[],"default":"","deleted":[{"id":"spend","updated_at":1,"by":"me"}]}`,
		"deleted id twice":       `{"views":[],"default":"","deleted":[{"id":"spend","updated_at":1},{"id":"spend","updated_at":2}]}`,
		"too many deleted":       `{"views":[],"default":"","deleted":[` + deletedRecords(MaxDeletedViews+1) + `]}`,
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
	maxDeleted := `{"views":[],"default":"","deleted":[` + deletedRecords(MaxDeletedViews) + `],"revision":7}`
	if _, errDecode := DecodeViews(strings.NewReader(maxDeleted)); errDecode != nil {
		t.Fatalf("200 deleted views refused: %v", errDecode)
	}
	if _, errDecode := DecodeViews(strings.NewReader(view(base + `,"updated_at":9007199254740991`))); errDecode != nil {
		t.Fatalf("updated_at of 2^53-1 refused: %v", errDecode)
	}
}

// deletedRecords is n distinct deleted view records, comma separated.
func deletedRecords(n int) string {
	records := make([]string, n)
	for i := range records {
		records[i] = fmt.Sprintf(`{"id":"gone-%d","updated_at":%d}`, i, 1700000000000+i)
	}
	return strings.Join(records, ",")
}

// testBase is the base revision a store hands out under useViewsClock.
const testBase = 1000

// useViewsClock fixes the base revision clock at now and forgets every base
// and revision seen, until the test ends.
func useViewsClock(t *testing.T, now int64) {
	t.Helper()
	viewsMu.Lock()
	savedNow, savedSeen, savedBases := viewsNow, viewsMaxSeen, viewsBases
	viewsNow = func() int64 { return now }
	viewsMaxSeen = 0
	viewsBases = map[string]viewsBase{}
	viewsMu.Unlock()
	t.Cleanup(func() {
		viewsMu.Lock()
		viewsNow, viewsMaxSeen, viewsBases = savedNow, savedSeen, savedBases
		viewsMu.Unlock()
	})
}

// A file written before revisions existed keeps its views, gets a base
// revision instead of 0, and a save from that base replaces it.
func TestLegacyViewsFileGetsABaseRevision(t *testing.T) {
	useViewsClock(t, testBase)
	path := filepath.Join(t.TempDir(), ViewsFileName)
	legacy := `{"views":[{"id":"spend","name":"Spend","panels":[{"type":"cost","options":{}}],"columns":[],"window":"last24h","accounts":null,"builtin":false}],"default":"spend"}`
	if errWrite := os.WriteFile(path, []byte(legacy), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	views, errRead := ReadViews(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if views.Revision != testBase || views.Deleted == nil || len(views.Deleted) != 0 || len(views.Views) != 1 {
		t.Fatalf("legacy file read as %+v", views)
	}
	for _, stale := range []int64{0, 1} {
		if _, errSave := SaveViews(path, stale, views); errSave == nil {
			t.Fatalf("save from revision %d over a legacy file succeeded", stale)
		}
	}
	stored, errSave := SaveViews(path, testBase, views)
	if errSave != nil || stored.Revision != testBase+1 {
		t.Fatalf("save from the base: revision %d, %v", stored.Revision, errSave)
	}
}

// Each time the revision is lost, the new base is above every revision seen,
// even when the clock is behind, and stays the same while the file does.
func TestViewsBaseRevisionIsMonotonic(t *testing.T) {
	useViewsClock(t, 5)
	path := filepath.Join(t.TempDir(), ViewsFileName)
	empty := DashboardViews{Views: []DashboardView{}}
	load := func() int64 {
		t.Helper()
		views, errRead := ReadViews(path)
		if errRead == nil {
			return views.Revision
		}
		_, errSave := SaveViews(path, -1, empty)
		var conflict *ViewsConflictError
		if !errors.As(errSave, &conflict) {
			t.Fatalf("probe save: %v", errSave)
		}
		return conflict.Current.Revision
	}
	save := func(revision int64) int64 {
		t.Helper()
		stored, errSave := SaveViews(path, revision, empty)
		if errSave != nil {
			t.Fatalf("save from %d: %v", revision, errSave)
		}
		return stored.Revision
	}

	base := load()
	if base != 5 || load() != 5 {
		t.Fatalf("empty store base %d, want 5 twice", base)
	}
	seen := save(save(base))
	steps := []func() error{
		func() error { return os.WriteFile(path, []byte("{"), 0o600) },
		func() error { return os.Remove(path) },
		func() error { return os.WriteFile(path, []byte(`{"views":[],"default":""}`), 0o600) },
		func() error { return os.WriteFile(path, []byte("damaged again"), 0o600) },
	}
	for i, step := range steps {
		if errStep := step(); errStep != nil {
			t.Fatal(errStep)
		}
		next := load()
		if next != seen+1 || load() != next {
			t.Fatalf("step %d: base %d, want %d, stable", i, next, seen+1)
		}
		seen = next
	}
	// The damaged file changes without a save: it gets a new base, above the
	// one handed out for the old contents.
	if errWrite := os.WriteFile(path, []byte("damaged a third way"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if next := load(); next != seen+1 {
		t.Fatalf("changed damaged file: base %d, want %d", next, seen+1)
	}
}

// A save from a revision that is not the stored one writes nothing and
// returns the stored document.
func TestSaveViewsRefusesAStaleRevision(t *testing.T) {
	useViewsClock(t, testBase)
	path := filepath.Join(t.TempDir(), ViewsFileName)
	first, errSave := SaveViews(path, testBase, DashboardViews{Views: []DashboardView{}, Default: "spend"})
	if errSave != nil || first.Revision != testBase+1 {
		t.Fatalf("first save: revision %d, %v", first.Revision, errSave)
	}
	for _, stale := range []int64{testBase, testBase + 2, 0, -1} {
		_, errSave := SaveViews(path, stale, DashboardViews{Views: []DashboardView{}})
		var conflict *ViewsConflictError
		if !errors.As(errSave, &conflict) {
			t.Fatalf("save from revision %d: %v, want a conflict", stale, errSave)
		}
		if conflict.Current.Revision != testBase+1 || conflict.Current.Default != "spend" {
			t.Fatalf("conflict from revision %d holds %+v", stale, conflict.Current)
		}
	}
	read, errRead := ReadViews(path)
	if errRead != nil || read.Revision != testBase+1 || read.Default != "spend" {
		t.Fatalf("after refused saves the file holds %+v, %v", read, errRead)
	}
}

// Two saves from the same revision: exactly one succeeds. The test hook
// holds the first save that passes the revision check until the other save
// has either passed the check too (no lock: both succeed and the test fails)
// or found the lock held (it must then see the new revision and fail).
func TestSaveViewsConcurrentSavesFromOneRevision(t *testing.T) {
	useViewsClock(t, testBase)
	path := filepath.Join(t.TempDir(), ViewsFileName)
	if _, errSave := SaveViews(path, testBase, DashboardViews{Views: []DashboardView{}}); errSave != nil {
		t.Fatal(errSave)
	}

	var (
		mu       sync.Mutex
		checked  int
		released bool
		release  = make(chan struct{})
	)
	releaseOnce := func() {
		if !released {
			released = true
			close(release)
		}
	}
	viewsTestHook = func(event string) {
		mu.Lock()
		if event == "contended" {
			releaseOnce()
			mu.Unlock()
			return
		}
		checked++
		if checked > 1 {
			releaseOnce()
			mu.Unlock()
			return
		}
		mu.Unlock()
		<-release
	}
	defer func() { viewsTestHook = nil }()

	start := make(chan struct{})
	results := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = SaveViews(path, testBase+1, DashboardViews{Views: []DashboardView{}, Default: fmt.Sprintf("tab-%d", i)})
		}(i)
	}
	close(start)
	wg.Wait()

	saved, conflicts := 0, 0
	for _, errSave := range results {
		var conflict *ViewsConflictError
		switch {
		case errSave == nil:
			saved++
		case errors.As(errSave, &conflict) && conflict.Current.Revision == testBase+2:
			conflicts++
		default:
			t.Fatalf("unexpected result %v", errSave)
		}
	}
	if saved != 1 || conflicts != 1 {
		t.Fatalf("%d saves and %d conflicts, want 1 and 1", saved, conflicts)
	}
	read, errRead := ReadViews(path)
	if errRead != nil || read.Revision != testBase+2 {
		t.Fatalf("file at revision %d, %v, want %d", read.Revision, errRead, testBase+2)
	}
}

// updated_at on views and the deleted records are stored and read back.
func TestViewsUpdatedAtAndDeletedRoundTrip(t *testing.T) {
	useViewsClock(t, testBase)
	body := `{"views":[{"id":"spend","name":"Spend","panels":[{"type":"cost","options":{}}],"columns":[],"window":"last24h","accounts":null,"builtin":false,"updated_at":1760090000123}],` +
		`"default":"spend","deleted":[{"id":"old","updated_at":1760080000000}],"revision":42}`
	views, errDecode := DecodeViews(strings.NewReader(body))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	path := filepath.Join(t.TempDir(), ViewsFileName)
	stored, errSave := SaveViews(path, testBase, views)
	if errSave != nil {
		t.Fatal(errSave)
	}
	want := strings.Replace(body, `"revision":42`, `"revision":1001`, 1)
	if got, _ := json.Marshal(stored); string(got) != want {
		t.Fatalf("stored %s, want %s", got, want)
	}
	if data, _ := os.ReadFile(path); string(data) != want {
		t.Fatalf("file holds %s, want %s", data, want)
	}
	read, errRead := ReadViews(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if got, _ := json.Marshal(read); string(got) != want {
		t.Fatalf("read back %s, want %s", got, want)
	}
}

func TestViewsFileRoundTripModeAndAtomicity(t *testing.T) {
	useViewsClock(t, testBase)
	dir := t.TempDir()
	path := filepath.Join(dir, ViewsFileName)

	empty, errRead := ReadViews(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if data, _ := json.Marshal(empty); string(data) != `{"views":[],"default":"","deleted":[],"revision":1000}` {
		t.Fatalf("empty views = %s", data)
	}

	views, errDecode := DecodeViews(strings.NewReader(validViewsBody))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	stored, errWrite := SaveViews(path, testBase, views)
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
	_, errBlocked := SaveViews(path, testBase+1, DashboardViews{Views: []DashboardView{}})
	var conflict *ViewsConflictError
	if errBlocked == nil || errors.As(errBlocked, &conflict) {
		t.Fatalf("write through a blocked temporary file: %v, want a write error", errBlocked)
	}
	again, errRead := ReadViews(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if got, _ := json.Marshal(again); string(got) != string(want) {
		t.Fatalf("after a failed write the file holds %s", got)
	}

	// An invalid set is never written.
	if _, errWrite := SaveViews(filepath.Join(dir, "other.json"), 0, DashboardViews{}); errWrite == nil {
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
	views := DashboardViews{Default: strings.Repeat("v", 38) + "00", Deleted: []DeletedView{}, Revision: testBase + 1}
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
	useViewsClock(t, testBase)
	body := largestValidViews(t)
	views, errDecode := DecodeViews(strings.NewReader(string(body)))
	if errDecode != nil {
		t.Fatalf("largest valid body refused: %v", errDecode)
	}
	path := filepath.Join(t.TempDir(), ViewsFileName)
	stored, errWrite := SaveViews(path, testBase, views)
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
	if _, errWrite := SaveViews(path, testBase+1, escaped); errWrite != nil {
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
