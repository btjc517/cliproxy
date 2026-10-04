package usagestats

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	sessionMetaRetention = 7 * 24 * time.Hour
	maxSessionMeta       = 5000
	maxSessionIDLength   = 200
	maxTitleLength       = 300
	maxMachineLength     = 100
)

// sessionMeta is what a client told the proxy about one of its sessions.
type sessionMeta struct {
	Title     string    `json:"title,omitempty"`
	Machine   string    `json:"machine,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SessionMetaUpdate names one client session. ID is the client's own id,
// without the "claude:" or "codex:" prefix the proxy adds.
type SessionMetaUpdate struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// UpdateSessionMeta stores titles and the machine for client sessions, even
// ones the proxy has not seen yet. It returns how many sessions it stored.
func (s *Store) UpdateSessionMeta(machine string, sessions []SessionMetaUpdate) int {
	machine = clip(machine, maxMachineLength)
	now := s.nowFunc()
	s.mu.Lock()
	defer s.mu.Unlock()
	updated := 0
	for _, update := range sessions {
		id := sessionMetaKey(update.ID)
		if id == "" || len(id) > maxSessionIDLength {
			continue
		}
		id = strings.Clone(id)
		title := clip(update.Title, maxTitleLength)
		if title == "" && machine == "" {
			continue
		}
		meta := s.meta[id]
		if meta == nil {
			meta = &sessionMeta{}
			s.meta[id] = meta
		}
		if title != "" {
			meta.Title = title
		}
		if machine != "" {
			meta.Machine = machine
		}
		meta.UpdatedAt = now
		updated++
	}
	if updated > 0 {
		s.dirty = true
		s.pruneSessionMetaLocked(now)
	}
	return updated
}

// sessionMetaKey reduces a session id to the client's own id: it drops the
// "claude:" or "codex:" prefix and any ":agent:" thread suffix.
func sessionMetaKey(id string) string {
	base, _ := splitSessionID(id)
	return base
}

// splitSessionID returns the client's own id and whether id names an agent
// thread inside that session.
func splitSessionID(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if provider, rest, ok := strings.Cut(id, ":"); ok && (provider == "claude" || provider == "codex") {
		id = rest
	}
	base, _, agent := strings.Cut(id, ":agent:")
	return strings.TrimSpace(base), agent
}

// parentSessionID is the parent of an agent thread id
// ("<parent>:agent:<x>"), or "".
func parentSessionID(id string) string {
	if parent, _, ok := strings.Cut(id, ":agent:"); ok {
		return parent
	}
	return ""
}

func (s *Store) pruneSessionMetaLocked(now time.Time) {
	cutoff := now.Add(-sessionMetaRetention)
	for id, meta := range s.meta {
		if meta == nil || meta.UpdatedAt.Before(cutoff) {
			delete(s.meta, id)
			s.dirty = true
		}
	}
	if len(s.meta) <= maxSessionMeta {
		return
	}
	ids := make([]string, 0, len(s.meta))
	for id := range s.meta {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return s.meta[ids[i]].UpdatedAt.After(s.meta[ids[j]].UpdatedAt) })
	for _, id := range ids[maxSessionMeta:] {
		delete(s.meta, id)
	}
	s.dirty = true
}

// clip trims value and cuts it to at most limit bytes on a rune boundary. The
// result is a copy, so storing it does not keep a large request body alive.
func clip(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return strings.Clone(value)
}
