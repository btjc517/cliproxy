package helps

import (
	"container/list"
	"crypto/sha256"
	"maps"
	"sync"
	"time"
)

// Thread snapshots are addressed by the exact previous response, so branches and
// subagents cannot replace each other's tool names. Store names only, never tool
// inputs, conversation text, or credentials. Missing snapshots fail closed.
type claudeThreadToolNameKey struct {
	caller    [32]byte
	messageID string
}

type claudeThreadToolNameEntry struct {
	key     claudeThreadToolNameKey
	names   map[string]string
	expires time.Time
}

type claudeThreadToolNameCache struct {
	mu       sync.Mutex
	entries  map[claudeThreadToolNameKey]*list.Element
	order    *list.List
	capacity int
	ttl      time.Duration
	now      func() time.Time
}

func newClaudeThreadToolNameCache(capacity int, ttl time.Duration, now func() time.Time) *claudeThreadToolNameCache {
	return &claudeThreadToolNameCache{entries: make(map[claudeThreadToolNameKey]*list.Element), order: list.New(), capacity: capacity, ttl: ttl, now: now}
}

var claudeThreadToolNames = newClaudeThreadToolNameCache(4096, 24*time.Hour, time.Now)

// LoadClaudeThreadToolNames returns an independent snapshot, including a valid
// empty map for conversations that deliberately declared no aliased tools.
func LoadClaudeThreadToolNames(caller, messageID string) (map[string]string, bool) {
	return claudeThreadToolNames.load(caller, messageID)
}

// StoreClaudeThreadToolNames publishes the mapping for a completed response.
func StoreClaudeThreadToolNames(caller, messageID string, names map[string]string) {
	claudeThreadToolNames.store(caller, messageID, names)
}

func (c *claudeThreadToolNameCache) load(caller, messageID string) (map[string]string, bool) {
	if caller == "" || messageID == "" {
		return nil, false
	}
	key := claudeThreadToolNameKey{sha256.Sum256([]byte(caller)), messageID}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	entry := element.Value.(claudeThreadToolNameEntry)
	if !c.now().Before(entry.expires) {
		delete(c.entries, key)
		c.order.Remove(element)
		return nil, false
	}
	c.order.MoveToFront(element)
	return maps.Clone(entry.names), true
}

func (c *claudeThreadToolNameCache) store(caller, messageID string, names map[string]string) {
	if caller == "" || messageID == "" || c.capacity <= 0 {
		return
	}
	key := claudeThreadToolNameKey{sha256.Sum256([]byte(caller)), messageID}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := claudeThreadToolNameEntry{key, maps.Clone(names), c.now().Add(c.ttl)}
	if element, ok := c.entries[key]; ok {
		element.Value = entry
		c.order.MoveToFront(element)
		return
	}
	c.entries[key] = c.order.PushFront(entry)
	for c.order.Len() > c.capacity {
		oldest := c.order.Back()
		delete(c.entries, oldest.Value.(claudeThreadToolNameEntry).key)
		c.order.Remove(oldest)
	}
}
