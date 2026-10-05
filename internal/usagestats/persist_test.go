package usagestats

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	_ "time/tzdata"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// savedRequests adds up the hourly requests in a saved stats file.
func savedRequests(data []byte) (int64, error) {
	var state fileState
	if errUnmarshal := json.Unmarshal(data, &state); errUnmarshal != nil {
		return 0, errUnmarshal
	}
	var total int64
	for _, buckets := range state.Hourly {
		for _, bucket := range buckets {
			total += bucket.Requests
		}
	}
	return total, nil
}

func fileRequests(t *testing.T, path string) int64 {
	t.Helper()
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	total, errParse := savedRequests(data)
	if errParse != nil {
		t.Fatalf("saved file does not parse: %v", errParse)
	}
	return total
}

// Flushes run one at a time from snapshot to rename, so the file only ever
// moves forward and ends with every request.
func TestFlushesRunOneAtATime(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 5, 15, 30, 0, 0, time.UTC)}
	store := newClockStore(clock)
	store.path = filepath.Join(t.TempDir(), "usage-stats.json")
	var inFlight, maxInFlight, unlockedDuringSave atomic.Int64
	var mu sync.Mutex
	var written []int64
	store.saveFile = func(path string, data []byte) error {
		if n := inFlight.Add(1); n > maxInFlight.Load() {
			maxInFlight.Store(n)
		}
		defer inFlight.Add(-1)
		if store.flushMu.TryLock() {
			store.flushMu.Unlock()
			unlockedDuringSave.Add(1)
		}
		runtime.Gosched()
		total, errParse := savedRequests(data)
		if errParse != nil {
			return errParse
		}
		mu.Lock()
		written = append(written, total)
		mu.Unlock()
		return writeFileAtomic(path, data)
	}

	const workers, perWorker = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: clock.now})
				if errFlush := store.Flush(); errFlush != nil {
					errs <- errFlush
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for errFlush := range errs {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}

	if maxInFlight.Load() != 1 || unlockedDuringSave.Load() != 0 {
		t.Fatalf("saves overlapped: %d at once, %d saves without the flush lock", maxInFlight.Load(), unlockedDuringSave.Load())
	}
	for i := 1; i < len(written); i++ {
		if written[i] < written[i-1] {
			t.Fatalf("save %d wrote %d requests after save %d wrote %d: an older snapshot replaced a newer one", i, written[i], i-1, written[i-1])
		}
	}
	if got := fileRequests(t, store.path); got != workers*perWorker {
		t.Fatalf("file holds %d requests, want %d", got, workers*perWorker)
	}
	if entries, _ := os.ReadDir(filepath.Dir(store.path)); len(entries) != 1 {
		t.Fatalf("stats dir holds %d entries, want only the stats file", len(entries))
	}
}

// A stats file that exists but cannot be loaded is copied aside and never
// replaced by the near-empty tally in memory.
func TestCorruptStatsFileIsKept(t *testing.T) {
	valid := `{"version":3,"hourly":{"claude-a":{"1790000000":{"requests":9}}},"sessions":{}}`
	for _, tc := range []struct {
		name string
		// write creates what sits at path.
		write func(t *testing.T, path string)
		// copied is false when the path cannot be copied aside.
		copied bool
	}{
		{"garbage", func(t *testing.T, path string) { writeTestFile(t, path, "{not json") }, true},
		{"truncated", func(t *testing.T, path string) { writeTestFile(t, path, valid[:len(valid)/2]) }, true},
		{"wrong type", func(t *testing.T, path string) { writeTestFile(t, path, `{"version":3,"hourly":[]}`) }, true},
		{"empty", func(t *testing.T, path string) { writeTestFile(t, path, "") }, true},
		{"unreadable", func(t *testing.T, path string) {
			if errMkdir := os.Mkdir(path, 0o700); errMkdir != nil {
				t.Fatal(errMkdir)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &testClock{now: time.Date(2026, 10, 5, 15, 30, 0, 0, time.UTC)}
			dir := t.TempDir()
			path := filepath.Join(dir, "usage-stats.json")
			tc.write(t, path)
			original, _ := os.ReadFile(path)

			store := newClockStore(clock)
			store.configure(path)
			defer close(store.stop)
			if !store.persistOff {
				t.Fatal("saving still on after a failed load")
			}
			store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: clock.now})
			for i := 0; i < 3; i++ {
				if errFlush := store.Flush(); errFlush != nil {
					t.Fatalf("Flush() error = %v", errFlush)
				}
			}
			if got := store.Summary(10).Accounts["claude-a"].Today.Requests; got != 1 {
				t.Fatalf("in-memory tally = %d requests, want 1", got)
			}

			info, errStat := os.Stat(path)
			if errStat != nil {
				t.Fatal(errStat)
			}
			if tc.copied {
				if after, _ := os.ReadFile(path); info.IsDir() || !bytes.Equal(after, original) {
					t.Fatalf("stats file changed to %q, want %q", after, original)
				}
			} else if !info.IsDir() {
				t.Fatal("unreadable path was replaced")
			}
			backups, _ := filepath.Glob(path + ".corrupt-*")
			wantName := path + ".corrupt-20261005T153000.000000000Z"
			if !tc.copied {
				if len(backups) != 0 {
					t.Fatalf("backups = %v, want none for a path that cannot be copied", backups)
				}
				return
			}
			if len(backups) != 1 || backups[0] != wantName {
				t.Fatalf("backups = %v, want %s", backups, wantName)
			}
			if copied, _ := os.ReadFile(backups[0]); !bytes.Equal(copied, original) {
				t.Fatalf("backup holds %q, want %q", copied, original)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 2 {
				names := make([]string, 0, len(entries))
				for _, entry := range entries {
					names = append(names, entry.Name())
				}
				t.Fatalf("dir holds %s, want only the stats file and its backup", strings.Join(names, ", "))
			}
		})
	}
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if errWrite := os.WriteFile(path, []byte(data), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
}

// A failed save leaves the tally unsaved, so the next flush retries it, and
// records that arrive during a save are saved by the next flush.
func TestFailedSaveIsRetried(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 5, 15, 30, 0, 0, time.UTC)}
	store := newClockStore(clock)
	store.path = filepath.Join(t.TempDir(), "usage-stats.json")
	record := func() {
		store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: clock.now})
	}
	var saves int
	store.saveFile = func(path string, data []byte) error {
		saves++
		return writeFileAtomic(path, data)
	}

	// The temporary file's path is taken by a directory, so the write fails.
	record()
	tmp := store.path + ".tmp"
	if errMkdir := os.Mkdir(tmp, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	if errFlush := store.Flush(); errFlush == nil {
		t.Fatal("Flush() succeeded with the temporary path blocked")
	}
	if _, errStat := os.Stat(store.path); !os.IsNotExist(errStat) {
		t.Fatalf("stats file exists after a failed save: %v", errStat)
	}
	if errRemove := os.Remove(tmp); errRemove != nil {
		t.Fatal(errRemove)
	}
	// No new records: the retry must still save.
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("retry Flush() error = %v", errFlush)
	}
	if got := fileRequests(t, store.path); got != 1 {
		t.Fatalf("file holds %d requests after the retry, want 1", got)
	}
	saves = 0
	if errFlush := store.Flush(); errFlush != nil || saves != 0 {
		t.Fatalf("Flush() with nothing new: error %v, %d saves, want none", errFlush, saves)
	}

	// A record that arrives while the file is written is not in that
	// snapshot and stays unsaved until the next flush.
	record()
	store.saveFile = func(path string, data []byte) error {
		record()
		return writeFileAtomic(path, data)
	}
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	if got := fileRequests(t, store.path); got != 2 {
		t.Fatalf("file holds %d requests, want the 2 in the snapshot", got)
	}
	store.saveFile = writeFileAtomic
	if errFlush := store.Flush(); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	if got := fileRequests(t, store.path); got != 3 {
		t.Fatalf("file holds %d requests, want 3 with the one recorded during the save", got)
	}
	reloaded := newClockStore(clock)
	reloaded.path = store.path
	reloaded.loadLocked()
	if got := reloaded.Summary(10).Accounts["claude-a"].Today.Requests; got != 3 {
		t.Fatalf("reloaded tally = %d requests, want 3", got)
	}
}

// Hourly usage is kept back to the first bucket of every hourly window, which
// is more than 14 days back when the window crosses the change from summer
// time. usage_range then counts what the performance series counts.
func TestHourlyUsageKeepsFirstBucketAcrossDST(t *testing.T) {
	london := mustZone(t, "Europe/London")
	newYork := mustZone(t, "America/New_York")
	lordHowe := mustZone(t, "Australia/Lord_Howe")
	for _, tc := range []struct {
		name string
		now  time.Time
		// first is the expected start of the 14d window, or zero to skip.
		first time.Time
		// longer is true when the 14d window is longer than 14 days.
		longer bool
	}{
		{"london 2026-11-01", time.Date(2026, 11, 1, 11, 30, 0, 0, london), time.Date(2026, 10, 18, 11, 0, 0, 0, time.UTC), true},
		{"new york 2026-11-08", time.Date(2026, 11, 8, 11, 30, 0, 0, newYork), time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC), true},
		{"lord howe 2026-04-05", time.Date(2026, 4, 5, 11, 45, 0, 0, lordHowe), time.Time{}, true},
		{"london spring 2026-04-05", time.Date(2026, 4, 5, 11, 30, 0, 0, london), time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"24h", "7d", "14d"} {
				bounds, _ := windows[key].span(tc.now, time.Time{})
				first := bounds[0]
				if key == "14d" {
					if !tc.first.IsZero() && !first.Equal(tc.first) {
						t.Fatalf("14d starts %s, want %s", first, tc.first)
					}
					if longer := tc.now.Sub(first) > hourlyRetention; longer != tc.longer {
						t.Fatalf("14d spans %s, longer than %s = %v, want %v", tc.now.Sub(first), hourlyRetention, longer, tc.longer)
					}
				}
				store := newTestStore(tc.now)
				store.machineName = func(string) string { return "" }
				before := first.Add(-50 * time.Minute)
				for _, at := range []time.Time{before, first.Add(10 * time.Minute), tc.now} {
					store.record(coreusage.Record{AuthID: "claude-a", Provider: "claude", RequestedAt: at})
				}
				summary := store.SummaryFor(10, windows[key])
				label := fmt.Sprintf("%s at %s", key, tc.now)
				usage := summary.UsageRange.Accounts["claude-a"]
				if !summary.UsageRange.Starts[0].Equal(first) || len(usage) == 0 || usage[0].Requests != 1 {
					t.Fatalf("%s: first usage bucket %v = %+v, want the request at %s", label, summary.UsageRange.Starts[0], usage, first.Add(10*time.Minute))
				}
				if got, want := usageTotals(usage).Requests, summary.Performance.Scopes["claude-a"].Requests; got != 2 || want != 2 {
					t.Fatalf("%s: usage counts %d requests and performance %d, want 2 each", label, got, want)
				}
				if _, kept := store.hourly["claude-a"][bucketStart(before, tc.now.Location(), 1).Unix()]; kept && key == "14d" && tc.longer {
					t.Fatalf("%s: the hour before the 14d window was kept", label)
				}
			}
		})
	}
}
