package usagestats

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// ViewsFileName is the saved dashboard views file, kept next to the stats
	// file.
	ViewsFileName = "dashboard-views.json"
	// MaxViewsBody is the largest views document accepted, in bytes.
	MaxViewsBody = 64 << 10
	// MaxViews is the most views a document may hold.
	MaxViews = 50
	// MaxDeletedViews is the most deleted view records a document may hold.
	MaxDeletedViews = 200
	maxViewID       = 40
	maxViewName     = 60
	// maxUpdatedAt is the largest updated_at accepted: the largest integer a
	// JavaScript number holds exactly.
	maxUpdatedAt = 1<<53 - 1
	// MaxViewsRevision is the largest revision stored, for the same reason.
	MaxViewsRevision = 1<<53 - 1
	// maxViewsFile is the largest views file read back. A stored file can be
	// bigger than the request that made it: panels without options gain
	// "options":{} and encoding/json writes <, > and & as six byte escapes.
	maxViewsFile = 1 << 20
)

// viewPanelTypes are the panel types a view may hold.
var viewPanelTypes = map[string]struct{}{
	"allowance": {}, "available": {}, "tokens": {}, "cost": {}, "requests": {}, "output": {},
	"cache": {}, "ttft": {}, "latency": {}, "throughput": {}, "failures": {}, "activity": {},
}

// viewWindows are the default windows a view may name.
var viewWindows = map[string]struct{}{"last24h": {}, "last7d": {}, "around_now": {}}

// DashboardViews is the stored set of dashboard views: user views and
// overrides of the built-in ones, which the dashboard itself defines. Default
// is the id of the view the dashboard opens with, or empty. Deleted records
// views a client removed on purpose. Revision belongs to the server: it
// counts saves, and the value a client sends is ignored.
type DashboardViews struct {
	Views    []DashboardView `json:"views"`
	Default  string          `json:"default"`
	Deleted  []DeletedView   `json:"deleted"`
	Revision int64           `json:"revision"`
}

// DashboardView is one saved view. Accounts is null to follow the shared
// account selection, or the credential ids the view keeps. UpdatedAt is set
// by the client, in milliseconds since the epoch, and left out when 0.
type DashboardView struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Panels    []ViewPanel `json:"panels"`
	Columns   []string    `json:"columns"`
	Window    string      `json:"window"`
	Accounts  []string    `json:"accounts"`
	Builtin   bool        `json:"builtin"`
	UpdatedAt int64       `json:"updated_at,omitempty"`
}

// DeletedView records that a client deleted the view with this id, at
// UpdatedAt milliseconds since the epoch.
type DeletedView struct {
	ID        string `json:"id"`
	UpdatedAt int64  `json:"updated_at"`
}

// ViewPanel is one panel of a view. Options is a JSON object the dashboard
// owns, such as {"format":"lines"}.
type ViewPanel struct {
	Type    string          `json:"type"`
	Options json.RawMessage `json:"options"`
}

// viewsMu serializes saves of the views file. It holds the read, the
// revision check and the write together, and writeFileAtomic uses one
// temporary name per path.
var viewsMu sync.Mutex

// viewsTestHook, when a test sets it, is called with "contended" when a save
// finds viewsMu held and with "checked" when a save has passed the revision
// check and is about to write. It is nil in production.
var viewsTestHook func(event string)

// lockViews takes viewsMu, telling the test hook when it has to wait.
func lockViews() {
	if viewsMu.TryLock() {
		return
	}
	if viewsTestHook != nil {
		viewsTestHook("contended")
	}
	viewsMu.Lock()
}

// ViewsConflictError is returned by SaveViews when the stored revision is not
// the one the client loaded. Current is the stored document.
type ViewsConflictError struct {
	Current DashboardViews
}

func (e *ViewsConflictError) Error() string {
	return fmt.Sprintf("views changed since you loaded them (now revision %d)", e.Current.Revision)
}

// validViewID reports whether id is 1 to 40 lowercase letters, digits and
// hyphens.
func validViewID(id string) bool {
	if id == "" || len(id) > maxViewID {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// DecodeViews reads and checks a views document sent by a client. It rejects
// a body over MaxViewsBody bytes, unknown fields, trailing data and any value
// outside the rules below, with an error that names the problem.
func DecodeViews(body io.Reader) (DashboardViews, error) {
	return decodeViews(body, MaxViewsBody, "body is larger than 64 KB")
}

// decodeViews is DecodeViews with a size limit in bytes and the error for a
// document over it.
func decodeViews(body io.Reader, limit int64, tooLarge string) (DashboardViews, error) {
	data, errRead := io.ReadAll(io.LimitReader(body, limit+1))
	if errRead != nil {
		return DashboardViews{}, fmt.Errorf("read body: %w", errRead)
	}
	if int64(len(data)) > limit {
		return DashboardViews{}, errors.New(tooLarge)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var views DashboardViews
	if errDecode := decoder.Decode(&views); errDecode != nil {
		return DashboardViews{}, fmt.Errorf("invalid views JSON: %v", errDecode)
	}
	if _, errExtra := decoder.Token(); !errors.Is(errExtra, io.EOF) {
		return DashboardViews{}, errors.New("invalid views JSON: data after the document")
	}
	if errCheck := views.check(); errCheck != nil {
		return DashboardViews{}, errCheck
	}
	return views, nil
}

// check applies the views rules: an array of at most MaxViews views with
// distinct ids, a default that is empty or a well formed id (it may name a
// built-in view the server does not store), valid views, and at most
// MaxDeletedViews deleted records with distinct well formed ids and positive
// times. Revision is not checked, since the server sets it.
func (v DashboardViews) check() error {
	if v.Views == nil {
		return errors.New("views must be an array")
	}
	if len(v.Views) > MaxViews {
		return fmt.Errorf("at most %d views", MaxViews)
	}
	if v.Default != "" && !validViewID(v.Default) {
		return errors.New("default must be empty or a view id")
	}
	seen := make(map[string]struct{}, len(v.Views))
	for i, view := range v.Views {
		if errView := view.check(); errView != nil {
			return fmt.Errorf("views[%d]: %w", i, errView)
		}
		if _, dup := seen[view.ID]; dup {
			return fmt.Errorf("views[%d]: id %q is used twice", i, view.ID)
		}
		seen[view.ID] = struct{}{}
	}
	if len(v.Deleted) > MaxDeletedViews {
		return fmt.Errorf("at most %d deleted views", MaxDeletedViews)
	}
	seenDeleted := make(map[string]struct{}, len(v.Deleted))
	for i, deleted := range v.Deleted {
		if !validViewID(deleted.ID) {
			return fmt.Errorf("deleted[%d]: id must be 1 to 40 lowercase letters, digits or hyphens", i)
		}
		if deleted.UpdatedAt <= 0 || deleted.UpdatedAt > maxUpdatedAt {
			return fmt.Errorf("deleted[%d]: updated_at must be a positive integer of milliseconds", i)
		}
		if _, dup := seenDeleted[deleted.ID]; dup {
			return fmt.Errorf("deleted[%d]: id %q is used twice", i, deleted.ID)
		}
		seenDeleted[deleted.ID] = struct{}{}
	}
	return nil
}

func (view DashboardView) check() error {
	if !validViewID(view.ID) {
		return errors.New("id must be 1 to 40 lowercase letters, digits or hyphens")
	}
	if strings.TrimSpace(view.Name) == "" || utf8.RuneCountInString(view.Name) > maxViewName {
		return errors.New("name must be 1 to 60 characters")
	}
	if view.Panels == nil {
		return errors.New("panels must be an array")
	}
	for i, panel := range view.Panels {
		if _, ok := viewPanelTypes[panel.Type]; !ok {
			return fmt.Errorf("panels[%d]: unknown type %q", i, panel.Type)
		}
		if len(panel.Options) > 0 && !bytes.Equal(panel.Options, []byte("null")) && panel.Options[0] != '{' {
			return fmt.Errorf("panels[%d]: options must be an object", i)
		}
	}
	if view.Columns == nil {
		return errors.New("columns must be an array of strings")
	}
	if _, ok := viewWindows[view.Window]; !ok {
		return errors.New("window must be last24h, last7d or around_now")
	}
	if view.UpdatedAt < 0 || view.UpdatedAt > maxUpdatedAt {
		return errors.New("updated_at must be a positive integer of milliseconds")
	}
	return nil
}

// normalized gives every panel an options object and the document a deleted
// array, so a stored document reads back with "options": {} and
// "deleted": [] rather than null.
func (v DashboardViews) normalized() DashboardViews {
	if v.Deleted == nil {
		v.Deleted = []DeletedView{}
	}
	for i := range v.Views {
		for j := range v.Views[i].Panels {
			options := v.Views[i].Panels[j].Options
			if len(options) == 0 || bytes.Equal(options, []byte("null")) {
				v.Views[i].Panels[j].Options = json.RawMessage("{}")
			}
		}
	}
	return v
}

// emptyViews is a document with no views, no default and no revision.
func emptyViews() DashboardViews {
	return DashboardViews{Views: []DashboardView{}}.normalized()
}

// ViewsPath is the views file next to the configured stats file, or "" when
// no stats file is configured.
func (s *Store) ViewsPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), ViewsFileName)
}

// viewsBase is the revision handed out for a views file whose own revision
// is unknown, while the file stays in the state it was given for.
type viewsBase struct {
	state    string
	revision int64
}

var (
	// viewsBases maps a views path to its base revision while the file is
	// missing, damaged or has no revision. Guarded by viewsMu.
	viewsBases = map[string]viewsBase{}
	// viewsMaxSeen is the largest revision this process has read, handed
	// out or written. Guarded by viewsMu.
	viewsMaxSeen int64
	// viewsNow is the clock for base revisions, in milliseconds since the
	// epoch. Tests replace it.
	viewsNow = func() int64 { return time.Now().UnixMilli() }
)

// errViewsRevisionLimit is returned when a save would take the revision past
// MaxViewsRevision.
var errViewsRevisionLimit = errors.New("views revision is at its limit")

// viewsDamagedError is returned by loadViews for a file that cannot be
// decoded or holds a revision outside 0 to MaxViewsRevision.
type viewsDamagedError struct {
	err error
}

func (e *viewsDamagedError) Error() string { return e.err.Error() }
func (e *viewsDamagedError) Unwrap() error { return e.err }

// noteViewsRevision raises viewsMaxSeen to revision. The caller holds viewsMu.
func noteViewsRevision(revision int64) {
	if revision > viewsMaxSeen {
		viewsMaxSeen = revision
	}
}

// baseViewsRevision is the revision for path while its file is in state and
// its own revision is unknown. The first call for a state picks the current
// time in milliseconds, or one more than any revision this process has seen
// when that is larger, so the base cannot equal a revision handed out before:
// earlier bases were earlier times, and each save adds only 1. Later calls
// for the same state return the same base, so a GET and the PUT after it
// agree. The caller holds viewsMu.
func baseViewsRevision(path, state string) (int64, error) {
	if base, ok := viewsBases[path]; ok && base.state == state {
		return base.revision, nil
	}
	revision := viewsNow()
	if revision <= viewsMaxSeen {
		revision = viewsMaxSeen + 1
	}
	if revision <= 0 || revision > MaxViewsRevision {
		return 0, errViewsRevisionLimit
	}
	viewsBases[path] = viewsBase{state: state, revision: revision}
	noteViewsRevision(revision)
	return revision, nil
}

// loadViews reads the views file at path and settles its revision. A file
// with a revision from 1 to MaxViewsRevision keeps it. A missing file, a file
// saved before revisions existed (revision 0) and a damaged file get a base
// revision from baseViewsRevision. A damaged file also returns a
// *viewsDamagedError, with the empty set at the base revision. The caller
// holds viewsMu.
func loadViews(path string) (DashboardViews, error) {
	data, errRead := os.ReadFile(path)
	missing := errors.Is(errRead, os.ErrNotExist)
	if errRead != nil && !missing {
		return emptyViews(), errRead
	}
	var views DashboardViews
	var errDamaged error
	state := "missing"
	if !missing {
		sum := sha256.Sum256(data)
		state = hex.EncodeToString(sum[:])
		var errDecode error
		views, errDecode = decodeViews(bytes.NewReader(data), maxViewsFile, "file is larger than 1 MB")
		switch {
		case errDecode != nil:
			errDamaged = errDecode
		case views.Revision < 0 || views.Revision > MaxViewsRevision:
			errDamaged = fmt.Errorf("revision %d is outside 0 to %d", views.Revision, int64(MaxViewsRevision))
		case views.Revision > 0:
			delete(viewsBases, path)
			noteViewsRevision(views.Revision)
			return views.normalized(), nil
		}
	}
	base, errBase := baseViewsRevision(path, state)
	if errBase != nil {
		return emptyViews(), errBase
	}
	if missing || errDamaged != nil {
		views = emptyViews()
	}
	views = views.normalized()
	views.Revision = base
	if errDamaged != nil {
		return views, &viewsDamagedError{err: fmt.Errorf("%s: %w", filepath.Base(path), errDamaged)}
	}
	return views, nil
}

// ReadViews loads the views file at path. A missing file is an empty set and
// a file saved before revisions existed keeps its views. Both get a base
// revision that no client has seen before (see baseViewsRevision), never 0.
// A damaged file is an error.
func ReadViews(path string) (DashboardViews, error) {
	viewsMu.Lock()
	defer viewsMu.Unlock()
	views, errLoad := loadViews(path)
	if errLoad != nil {
		return emptyViews(), errLoad
	}
	return views, nil
}

// SaveViews checks views and, when the stored revision equals ifRevision,
// replaces the views file at path atomically, with mode 0600, as compact
// JSON, at revision ifRevision+1. It returns what was stored. When the stored
// revision differs it writes nothing and returns a *ViewsConflictError that
// holds the stored document. The read, the check and the write happen under
// one lock, so two saves from the same revision cannot both succeed. A
// damaged file counts as the empty set at a base revision, which the
// conflict reports, so a client can replace it. A save never takes the
// revision past MaxViewsRevision.
func SaveViews(path string, ifRevision int64, views DashboardViews) (DashboardViews, error) {
	if errCheck := views.check(); errCheck != nil {
		return DashboardViews{}, errCheck
	}
	views = views.normalized()
	lockViews()
	defer viewsMu.Unlock()
	current, errLoad := loadViews(path)
	var damaged *viewsDamagedError
	if errLoad != nil && !errors.As(errLoad, &damaged) {
		return DashboardViews{}, errLoad
	}
	if ifRevision <= 0 || current.Revision != ifRevision {
		return DashboardViews{}, &ViewsConflictError{Current: current}
	}
	if current.Revision >= MaxViewsRevision {
		return DashboardViews{}, errViewsRevisionLimit
	}
	if viewsTestHook != nil {
		viewsTestHook("checked")
	}
	views.Revision = current.Revision + 1
	data, errMarshal := json.Marshal(views)
	if errMarshal != nil {
		return DashboardViews{}, errMarshal
	}
	if errWrite := writeFileAtomic(path, data); errWrite != nil {
		return DashboardViews{}, errWrite
	}
	delete(viewsBases, path)
	noteViewsRevision(views.Revision)
	return views, nil
}
