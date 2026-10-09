package usagestats

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	// ViewsFileName is the saved dashboard views file, kept next to the stats
	// file.
	ViewsFileName = "dashboard-views.json"
	// MaxViewsBody is the largest views document accepted, in bytes.
	MaxViewsBody = 64 << 10
	// MaxViews is the most views a document may hold.
	MaxViews    = 50
	maxViewID   = 40
	maxViewName = 60
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
// is the id of the view the dashboard opens with, or empty.
type DashboardViews struct {
	Views   []DashboardView `json:"views"`
	Default string          `json:"default"`
}

// DashboardView is one saved view. Accounts is null to follow the shared
// account selection, or the credential ids the view keeps.
type DashboardView struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Panels   []ViewPanel `json:"panels"`
	Columns  []string    `json:"columns"`
	Window   string      `json:"window"`
	Accounts []string    `json:"accounts"`
	Builtin  bool        `json:"builtin"`
}

// ViewPanel is one panel of a view. Options is a JSON object the dashboard
// owns, such as {"format":"lines"}.
type ViewPanel struct {
	Type    string          `json:"type"`
	Options json.RawMessage `json:"options"`
}

// viewsMu serializes writes of the views file, since writeFileAtomic uses
// one temporary name per path.
var viewsMu sync.Mutex

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

// DecodeViews reads and checks a views document. It rejects a body over
// MaxViewsBody bytes, unknown fields, trailing data and any value outside the
// rules below, with an error that names the problem.
func DecodeViews(body io.Reader) (DashboardViews, error) {
	data, errRead := io.ReadAll(io.LimitReader(body, MaxViewsBody+1))
	if errRead != nil {
		return DashboardViews{}, fmt.Errorf("read body: %w", errRead)
	}
	if len(data) > MaxViewsBody {
		return DashboardViews{}, errors.New("body is larger than 64 KB")
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
// built-in view the server does not store), and valid views.
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
	return nil
}

// normalized gives every panel an options object, so a stored view reads back
// with "options": {} rather than null.
func (v DashboardViews) normalized() DashboardViews {
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

// ReadViews loads the views file at path. A missing file is an empty set.
func ReadViews(path string) (DashboardViews, error) {
	empty := DashboardViews{Views: []DashboardView{}}
	data, errRead := os.ReadFile(path)
	if errors.Is(errRead, os.ErrNotExist) {
		return empty, nil
	}
	if errRead != nil {
		return empty, errRead
	}
	views, errDecode := DecodeViews(bytes.NewReader(data))
	if errDecode != nil {
		return empty, fmt.Errorf("%s: %w", filepath.Base(path), errDecode)
	}
	return views.normalized(), nil
}

// WriteViews checks views and replaces the views file at path atomically,
// with mode 0600. It returns what was stored.
func WriteViews(path string, views DashboardViews) (DashboardViews, error) {
	if errCheck := views.check(); errCheck != nil {
		return DashboardViews{}, errCheck
	}
	views = views.normalized()
	data, errMarshal := json.MarshalIndent(views, "", "  ")
	if errMarshal != nil {
		return DashboardViews{}, errMarshal
	}
	viewsMu.Lock()
	defer viewsMu.Unlock()
	if errWrite := writeFileAtomic(path, data); errWrite != nil {
		return DashboardViews{}, errWrite
	}
	return views, nil
}
