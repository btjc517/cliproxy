package helps

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
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
	// path is the file the cache is saved to, so a restart keeps open threads
	// working. Empty means memory only.
	path     string
	dirty    bool
	loopOnce sync.Once
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
	c.dirty = true
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

const claudeThreadToolNamesFlushEvery = 30 * time.Second

// claudeThreadToolNamesFile is the saved cache. Entries run oldest first. It
// holds tool names, response IDs and a hash of the caller, never credentials.
type claudeThreadToolNamesFile struct {
	Version int                           `json:"version"`
	Entries []claudeThreadToolNamesRecord `json:"entries"`
}

type claudeThreadToolNamesRecord struct {
	Caller    string            `json:"caller"`
	MessageID string            `json:"message_id"`
	Names     map[string]string `json:"names"`
	Expires   time.Time         `json:"expires"`
}

// ConfigureClaudeThreadToolNames saves the thread tool-name cache to path and
// loads what an earlier run saved there. A continuation whose mapping is
// missing fails closed, so without this every restart would break each open
// Claude thread.
func ConfigureClaudeThreadToolNames(path string) {
	claudeThreadToolNames.configure(path)
}

// FlushClaudeThreadToolNames writes the cache when it changed since the last write.
func FlushClaudeThreadToolNames() error {
	return claudeThreadToolNames.flush()
}

func (c *claudeThreadToolNameCache) configure(path string) {
	if path == "" {
		return
	}
	c.mu.Lock()
	if c.path == path {
		c.mu.Unlock()
		return
	}
	c.path = path
	c.loadLocked()
	c.mu.Unlock()
	c.loopOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(claudeThreadToolNamesFlushEvery)
			defer ticker.Stop()
			for range ticker.C {
				if errFlush := c.flush(); errFlush != nil {
					log.Warnf("claude thread tool names: save failed: %v", errFlush)
				}
			}
		}()
	})
}

func (c *claudeThreadToolNameCache) loadLocked() {
	data, errRead := os.ReadFile(c.path)
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			log.Warnf("claude thread tool names: read %s: %v", filepath.Base(c.path), errRead)
		}
		return
	}
	var state claudeThreadToolNamesFile
	if errUnmarshal := json.Unmarshal(data, &state); errUnmarshal != nil {
		log.Warnf("claude thread tool names: parse %s: %v", filepath.Base(c.path), errUnmarshal)
		return
	}
	now := c.now()
	for _, record := range state.Entries {
		raw, errDecode := hex.DecodeString(record.Caller)
		if errDecode != nil || len(raw) != sha256.Size || record.MessageID == "" || !now.Before(record.Expires) {
			continue
		}
		var key claudeThreadToolNameKey
		copy(key.caller[:], raw)
		key.messageID = record.MessageID
		if _, live := c.entries[key]; live {
			continue
		}
		c.entries[key] = c.order.PushFront(claudeThreadToolNameEntry{key, maps.Clone(record.Names), record.Expires})
	}
	for c.capacity > 0 && c.order.Len() > c.capacity {
		oldest := c.order.Back()
		delete(c.entries, oldest.Value.(claudeThreadToolNameEntry).key)
		c.order.Remove(oldest)
	}
}

func (c *claudeThreadToolNameCache) flush() error {
	c.mu.Lock()
	if c.path == "" || !c.dirty {
		c.mu.Unlock()
		return nil
	}
	now := c.now()
	state := claudeThreadToolNamesFile{Version: 1, Entries: make([]claudeThreadToolNamesRecord, 0, c.order.Len())}
	for element := c.order.Back(); element != nil; element = element.Prev() {
		entry := element.Value.(claudeThreadToolNameEntry)
		if !now.Before(entry.expires) {
			continue
		}
		state.Entries = append(state.Entries, claudeThreadToolNamesRecord{
			Caller:    hex.EncodeToString(entry.key.caller[:]),
			MessageID: entry.key.messageID,
			Names:     entry.names,
			Expires:   entry.expires,
		})
	}
	data, errMarshal := json.Marshal(state)
	path := c.path
	c.dirty = false
	c.mu.Unlock()
	if errMarshal != nil {
		return errMarshal
	}
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0o600); errWrite != nil {
		return errWrite
	}
	return os.Rename(tmp, path)
}
