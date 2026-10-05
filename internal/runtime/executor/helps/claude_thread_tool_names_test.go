package helps

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestClaudeThreadToolNameCacheIsolationExpirationAndBounds(t *testing.T) {
	now := time.Unix(100, 0)
	cache := newClaudeThreadToolNameCache(2, time.Hour, func() time.Time { return now })
	names := map[string]string{"alias": "Bash"}
	cache.store("caller", "parent", names)
	names["alias"] = "changed"
	got, ok := cache.load("caller", "parent")
	if !ok || got["alias"] != "Bash" {
		t.Fatal("store retained caller-owned map")
	}
	got["alias"] = "changed"
	got, _ = cache.load("caller", "parent")
	if got["alias"] != "Bash" {
		t.Fatal("load returned shared map")
	}
	if _, ok = cache.load("other", "parent"); ok {
		t.Fatal("caller boundary crossed")
	}
	cache.store("caller", "branch", map[string]string{"alias": "Read"})
	got, _ = cache.load("caller", "parent")
	if got["alias"] != "Bash" {
		t.Fatal("branch changed parent")
	}
	cache.store("caller", "third", nil)
	if _, ok = cache.load("caller", "branch"); ok {
		t.Fatal("LRU entry not evicted")
	}
	if empty, ok := cache.load("caller", "third"); !ok || len(empty) != 0 {
		t.Fatal("empty snapshot not retained")
	}
	now = now.Add(time.Hour)
	if _, ok = cache.load("caller", "parent"); ok {
		t.Fatal("expired entry returned")
	}
	if _, ok = cache.load("caller", "third"); ok {
		t.Fatal("expired empty entry returned")
	}
}

func TestClaudeThreadToolNameCacheConcurrentSnapshots(t *testing.T) {
	cache := newClaudeThreadToolNameCache(32, time.Hour, time.Now)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Go(func() {
			for j := 0; j < 50; j++ {
				cache.store("caller", "message", map[string]string{"alias": "Bash"})
				names, ok := cache.load("caller", "message")
				if !ok || names["alias"] != "Bash" {
					t.Error("incomplete concurrent snapshot")
				}
				names["alias"] = "local edit"
			}
		})
	}
	wg.Wait()
}

// TestClaudeThreadToolNameCacheSurvivesRestart saves the cache, then loads it
// into a fresh one, as a proxy restart does. A thread continued after the
// restart must still find its tool names.
func TestClaudeThreadToolNameCacheSurvivesRestart(t *testing.T) {
	now := time.Unix(1000, 0)
	path := filepath.Join(t.TempDir(), "claude-thread-tools.json")
	clock := func() time.Time { return now }

	first := newClaudeThreadToolNameCache(8, time.Hour, clock)
	first.path = path
	first.store("caller", "msg_old", map[string]string{"mcp__x_Bash": "Bash"})
	now = now.Add(30 * time.Minute)
	first.store("caller", "msg_new", map[string]string{"mcp__x_Read": "Read"})
	first.store("other", "msg_new", map[string]string{"mcp__y_Bash": "Bash"})
	if err := first.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved file = %v, %v; want mode 0600", info, err)
	}

	now = now.Add(45 * time.Minute)
	second := newClaudeThreadToolNameCache(8, time.Hour, clock)
	second.configure(path)
	if names, ok := second.load("caller", "msg_new"); !ok || names["mcp__x_Read"] != "Read" {
		t.Fatalf("restored names = %v, %v", names, ok)
	}
	if names, ok := second.load("other", "msg_new"); !ok || names["mcp__y_Bash"] != "Bash" {
		t.Fatalf("callers mixed after restore: %v, %v", names, ok)
	}
	if _, ok := second.load("caller", "msg_old"); ok {
		t.Fatal("an entry that expired while the proxy was down came back")
	}
}
