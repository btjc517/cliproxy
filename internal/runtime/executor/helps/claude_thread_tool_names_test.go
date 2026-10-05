package helps

import (
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
