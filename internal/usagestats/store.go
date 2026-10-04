// Package usagestats keeps a small persisted tally of upstream usage per
// credential and per hour, a daily tally per provider that is never pruned,
// plus the credentials each client session used. It backs the /dashboard page
// and the usage summary management endpoint.
package usagestats

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tailnetname"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	hourlyRetention  = 14 * 24 * time.Hour
	sessionRetention = 48 * time.Hour
	maxSessions      = 500
	flushInterval    = time.Minute
	statsFileVersion = 2
	dayLayout        = "2006-01-02"

	// HistoryFileName is the read-only backfill that sits next to the stats
	// file. It holds daily totals rebuilt from local Claude Code and Codex logs
	// for the time before the proxy kept its own tally.
	HistoryFileName = "usage-history.json"
)

// Counters is one bucket of usage for a credential.
type Counters struct {
	Requests   int64 `json:"requests"`
	Failed     int64 `json:"failed"`
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_tokens"`
	CacheWrite int64 `json:"cache_write_tokens"`
}

func (c *Counters) add(other Counters) {
	c.Requests += other.Requests
	c.Failed += other.Failed
	c.Input += other.Input
	c.Output += other.Output
	c.CacheRead += other.CacheRead
	c.CacheWrite += other.CacheWrite
}

// Session records which credentials served one client session.
type Session struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Provider string `json:"provider"`
	// Title and Machine come from POST /dashboard/sessions; Machine falls back
	// to the tailnet name of the client address. Both are filled in by Summary.
	Title         string                `json:"title"`
	Machine       string                `json:"machine"`
	Model         string                `json:"model"`
	ServingAuthID string                `json:"serving_auth_id"`
	AuthIDs       []string              `json:"auth_ids"`
	ByAuth        map[string]*AuthCount `json:"by_auth"`
	FirstSeen     time.Time             `json:"first_seen"`
	LastSeen      time.Time             `json:"last_seen"`
	TTFTP50       int64                 `json:"ttft_ms_p50"`
	// ClientIP and TTFTHist are kept in the stats file and left out of
	// Summary. ServedAt is when the request that set ServingAuthID started.
	ClientIP string    `json:"client_ip,omitempty"`
	TTFTHist histogram `json:"ttft_hist,omitempty"`
	ServedAt time.Time `json:"served_at,omitzero"`
	Counters
}

// AuthCount is how many of a session's requests went to one credential.
type AuthCount struct {
	Requests int64 `json:"requests"`
	Failed   int64 `json:"failed"`
}

type fileState struct {
	Version  int                             `json:"version"`
	Hourly   map[string]map[int64]*Counters  `json:"hourly"`
	Daily    map[string]map[string]*Counters `json:"daily,omitempty"`
	Sessions map[string]*Session             `json:"sessions"`
	// Perf is hourly request timing per credential.
	Perf          map[string]map[int64]*perfBucket `json:"perf,omitempty"`
	AuthProviders map[string]string                `json:"auth_providers,omitempty"`
	SessionMeta   map[string]*sessionMeta          `json:"session_meta,omitempty"`
}

// historyFile is the backfill written by the log collector. Days and machines
// map to provider totals; requests there count model replies.
type historyFile struct {
	Cutoff   time.Time                      `json:"cutoff"`
	Days     map[string]map[string]Counters `json:"days"`
	Machines map[string]map[string]Counters `json:"machines,omitempty"`
}

// Store aggregates usage records in memory and flushes them to disk.
type Store struct {
	mu       sync.Mutex
	path     string
	hourly   map[string]map[int64]*Counters
	daily    map[string]map[string]*Counters
	sessions map[string]*Session
	// perf is hourly request timing per credential; authProviders remembers
	// each credential's provider for the provider scopes.
	perf          map[string]map[int64]*perfBucket
	authProviders map[string]string
	// traces follows recent inbound requests that had a failed attempt.
	traces   map[string]*traceState
	meta     map[string]*sessionMeta
	backfill *historyFile
	dirty    bool
	nowFunc  func() time.Time
	// machineName maps a client address to a short machine name. It must not
	// block.
	machineName func(ip string) string
	stopOnce    sync.Once
	stop        chan struct{}
}

var defaultStore = newStore()

func init() {
	coreusage.RegisterPlugin(defaultStore)
}

func newStore() *Store {
	return &Store{
		hourly:        make(map[string]map[int64]*Counters),
		daily:         make(map[string]map[string]*Counters),
		sessions:      make(map[string]*Session),
		perf:          make(map[string]map[int64]*perfBucket),
		authProviders: make(map[string]string),
		traces:        make(map[string]*traceState),
		meta:          make(map[string]*sessionMeta),
		nowFunc:       time.Now,
		machineName:   tailnetname.MachineName,
	}
}

// Default returns the process-wide store.
func Default() *Store { return defaultStore }

// Configure sets the persistence file, loads any saved state and starts the
// periodic flush. Calling it again with the same path is a no-op.
func Configure(path string) {
	defaultStore.configure(path)
}

func (s *Store) configure(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	s.mu.Lock()
	if s.path == path {
		s.mu.Unlock()
		return
	}
	s.path = path
	s.loadLocked()
	s.loadHistoryLocked(filepath.Join(filepath.Dir(path), HistoryFileName))
	s.mu.Unlock()

	s.stopOnce.Do(func() {
		s.stop = make(chan struct{})
		go s.flushLoop()
	})
}

func (s *Store) flushLoop() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if errFlush := s.Flush(); errFlush != nil {
				log.Warnf("usagestats: flush failed: %v", errFlush)
			}
		case <-s.stop:
			return
		}
	}
}

// HandleUsage implements coreusage.Plugin.
func (s *Store) HandleUsage(ctx context.Context, record coreusage.Record) {
	if s == nil || !redisqueue.UsageStatisticsEnabled() {
		return
	}
	meta := internallogging.GetClientRequestMetadata(ctx)
	clientIP := strings.TrimSpace(meta.ClientIP)
	if clientIP == "" {
		clientIP = strings.TrimSpace(meta.ResolvedClientIP)
	}
	s.recordFrom(record, clientIP)
}

func (s *Store) record(record coreusage.Record) {
	s.recordFrom(record, "")
}

// recordFrom adds one upstream attempt. clientIP is the address of the client
// that sent the inbound request, or "".
func (s *Store) recordFrom(record coreusage.Record, clientIP string) {
	authID := strings.TrimSpace(record.AuthID)
	if authID == "" {
		return
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = s.nowFunc()
	}
	delta := countersFromRecord(record)

	location := s.nowFunc().Location()
	hourStart := bucketStart(at, location, 1)

	s.mu.Lock()
	defer s.mu.Unlock()
	buckets := s.hourly[authID]
	if buckets == nil {
		buckets = make(map[int64]*Counters)
		s.hourly[authID] = buckets
	}
	hour := hourStart.Unix()
	bucket := buckets[hour]
	if bucket == nil {
		bucket = &Counters{}
		buckets[hour] = bucket
	}
	bucket.add(delta)

	provider := providerOf(record.Provider, authID)
	day := at.In(location).Format(dayLayout)
	s.dailyBucketLocked(day, provider).add(delta)
	s.recordPerfLocked(authID, provider, hourStart, recordTiming{
		traceID: strings.TrimSpace(record.TraceID),
		at:      at,
		stream:  record.UpstreamStream,
		failed:  record.Failed,
		ttft:    record.TTFT,
		latency: record.Latency,
		output:  delta.Output,
	})

	if sessionID := strings.TrimSpace(record.SessionID); sessionID != "" {
		session := s.sessions[sessionID]
		if session == nil {
			session = &Session{ID: sessionID, Provider: strings.TrimSpace(record.Provider), FirstSeen: at}
			s.sessions[sessionID] = session
		}
		// Only accounts that answered count as serving the session. A failed try
		// that fell back to another account did not move its prompt cache.
		if !record.Failed && !containsString(session.AuthIDs, authID) {
			session.AuthIDs = append(session.AuthIDs, authID)
		}
		if session.ByAuth == nil {
			session.ByAuth = make(map[string]*AuthCount)
		}
		count := session.ByAuth[authID]
		if count == nil {
			count = &AuthCount{}
			session.ByAuth[authID] = count
		}
		count.Requests++
		if record.Failed {
			count.Failed++
		} else {
			// Attempts finish out of order: the serving account is the one that
			// answered the latest request, not the last one to finish.
			if !at.Before(session.ServedAt) {
				session.ServingAuthID = authID
				session.ServedAt = at
			}
			if record.TTFT > 0 {
				session.TTFTHist.observe(float64(record.TTFT)/float64(time.Millisecond), ttftBase)
			}
		}
		// Model and client address describe the request at LastSeen.
		if !at.Before(session.LastSeen) {
			if model := strings.TrimSpace(record.Model); model != "" {
				session.Model = model
			}
			if clientIP != "" && clientIP != session.ClientIP {
				session.ClientIP = clientIP
				if s.machineName != nil {
					// Starts the background name lookup so Summary finds it cached.
					s.machineName(clientIP)
				}
			}
			session.LastSeen = at
		}
		session.Counters.add(delta)
	}
	s.dirty = true
}

func (s *Store) dailyBucketLocked(day, provider string) *Counters {
	providers := s.daily[day]
	if providers == nil {
		providers = make(map[string]*Counters)
		s.daily[day] = providers
	}
	bucket := providers[provider]
	if bucket == nil {
		bucket = &Counters{}
		providers[provider] = bucket
	}
	return bucket
}

// providerOf names the provider for the daily tally. Auth file names start
// with the provider ("claude-", "codex-"), which covers records that carry
// no provider and the hourly buckets seeded from an older stats file.
func providerOf(provider, authID string) string {
	if provider = strings.ToLower(strings.TrimSpace(provider)); provider != "" {
		return provider
	}
	if prefix, _, ok := strings.Cut(authID, "-"); ok && prefix != "" {
		return strings.ToLower(prefix)
	}
	return "unknown"
}

func countersFromRecord(record coreusage.Record) Counters {
	counters := Counters{Requests: 1}
	if record.Failed {
		counters.Failed = 1
	}
	detail := record.Detail
	if breakdown := detail.TokenBreakdown; breakdown.Valid() {
		counters.Input = breakdown.Input.UncachedTokens
		counters.CacheRead = breakdown.Input.CacheReadTokens
		counters.CacheWrite = breakdown.Input.CacheWriteTokens
		counters.Output = breakdown.Output.TotalTokens
		return counters
	}
	counters.CacheRead = detail.CacheReadTokens
	if counters.CacheRead == 0 {
		counters.CacheRead = detail.CachedTokens
	}
	counters.CacheWrite = detail.CacheCreationTokens
	counters.Input = detail.InputTokens
	counters.Output = detail.OutputTokens
	return counters
}

// Summary is the dashboard view of the tally.
type Summary struct {
	GeneratedAt time.Time                 `json:"generated_at"`
	Timezone    string                    `json:"timezone"`
	Accounts    map[string]AccountSummary `json:"accounts"`
	Sessions    []Session                 `json:"sessions"`
	Totals      map[string]Counters       `json:"totals"`
	History     History                   `json:"history"`
	Range       string                    `json:"range"`
	Performance Performance               `json:"performance"`
}

// History is the long view: one entry per day with usage, oldest first, from
// the log backfill before BackfillCutoff and the proxy's own tally after it.
type History struct {
	BackfillCutoff   *time.Time                     `json:"backfill_cutoff,omitempty"`
	BackfillMachines map[string]map[string]Counters `json:"backfill_machines,omitempty"`
	Days             []HistoryDay                   `json:"days"`
	Today            Counters                       `json:"today"`
	ThisWeek         Counters                       `json:"this_week"`
	ThisMonth        Counters                       `json:"this_month"`
	Lifetime         Counters                       `json:"lifetime"`
}

// HistoryDay is one local calendar day of usage split by provider.
type HistoryDay struct {
	Date      string              `json:"date"`
	Providers map[string]Counters `json:"providers"`
}

// AccountSummary holds one credential's totals, its last 48 hourly buckets
// and its last 14 local days.
type AccountSummary struct {
	Today   Counters     `json:"today"`
	Last24h Counters     `json:"last_24h"`
	Last7d  Counters     `json:"last_7d"`
	Hourly  []HourBucket `json:"hourly"`
	Daily   []DayBucket  `json:"daily"`
}

// DayBucket is one local calendar day of usage.
type DayBucket struct {
	Date string `json:"date"`
	Counters
}

// accountDays is the number of local days in AccountSummary.Daily.
const accountDays = 14

// HourBucket is one hour of usage, keyed by the hour's start time.
type HourBucket struct {
	Start time.Time `json:"start"`
	Counters
}

// Summary builds the dashboard view with the default performance range.
// sessionLimit caps the session list.
func (s *Store) Summary(sessionLimit int) Summary {
	window, _ := LookupWindow(DefaultWindow)
	return s.SummaryFor(sessionLimit, window)
}

// SummaryFor builds the dashboard view with performance over window.
func (s *Store) SummaryFor(sessionLimit int, window Window) Summary {
	now := s.nowFunc()
	startOfDay := bucketStart(now, now.Location(), 24)
	hourBounds := localBounds(now, 1, 48)
	firstDay := startOfDay.AddDate(0, 0, -(accountDays - 1))
	dayIndex := make(map[string]int, accountDays)
	for i := 0; i < accountDays; i++ {
		dayIndex[firstDay.AddDate(0, 0, i).Format(dayLayout)] = i
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)

	summary := Summary{
		GeneratedAt: now,
		Timezone:    zoneName(now.Location()),
		Accounts:    make(map[string]AccountSummary, len(s.hourly)),
		Totals:      map[string]Counters{},
	}
	var today, day, week Counters
	for authID, buckets := range s.hourly {
		var account AccountSummary
		hourly := make([]HourBucket, 48)
		for i := range hourly {
			hourly[i].Start = hourBounds[i]
		}
		daily := make([]DayBucket, accountDays)
		for i := range daily {
			daily[i].Date = firstDay.AddDate(0, 0, i).Format(dayLayout)
		}
		for hourUnix, bucket := range buckets {
			start := time.Unix(hourUnix, 0)
			if index, ok := dayIndex[start.In(now.Location()).Format(dayLayout)]; ok {
				daily[index].Counters.add(*bucket)
			}
			if !start.Before(startOfDay) {
				account.Today.add(*bucket)
			}
			if now.Sub(start) < 24*time.Hour {
				account.Last24h.add(*bucket)
			}
			if now.Sub(start) < 7*24*time.Hour {
				account.Last7d.add(*bucket)
			}
			// Stored hours from older files may sit on another grid. One that
			// starts before the first hour is out of range, not part of it.
			if index, ok := boundsIndex(hourBounds, start); ok {
				hourly[index].Counters.add(*bucket)
			}
		}
		account.Hourly = hourly
		account.Daily = daily
		summary.Accounts[authID] = account
		today.add(account.Today)
		day.add(account.Last24h)
		week.add(account.Last7d)
	}
	summary.Totals["today"] = today
	summary.Totals["last_24h"] = day
	summary.Totals["last_7d"] = week
	summary.History = s.historyLocked(now)
	summary.Range = window.Key
	summary.Performance = s.performanceLocked(now, window)

	sessions := make([]Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, s.sessionViewLocked(session))
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].LastSeen.After(sessions[j].LastSeen) })
	if sessionLimit > 0 && len(sessions) > sessionLimit {
		sessions = sessions[:sessionLimit]
	}
	summary.Sessions = sessions
	return summary
}

// sessionViewLocked copies a session for Summary and fills in the derived
// fields.
func (s *Store) sessionViewLocked(session *Session) Session {
	view := *session
	view.AuthIDs = append([]string{}, session.AuthIDs...)
	view.ByAuth = make(map[string]*AuthCount, len(session.ByAuth))
	for authID, count := range session.ByAuth {
		if count == nil {
			continue
		}
		copyCount := *count
		view.ByAuth[authID] = &copyCount
	}
	view.ParentID = parentSessionID(session.ID)
	view.TTFTP50 = roundMillis(session.TTFTHist.percentile(0.5, ttftBase))
	view.TTFTHist = nil
	view.ClientIP = ""
	base, agent := splitSessionID(session.ID)
	if meta := s.meta[base]; meta != nil {
		view.Machine = meta.Machine
		// Agent threads inherit the machine; the title names the main session.
		if !agent {
			view.Title = meta.Title
		}
	}
	if view.Machine == "" && session.ClientIP != "" && s.machineName != nil {
		view.Machine = s.machineName(session.ClientIP)
	}
	return view
}

func (s *Store) historyLocked(now time.Time) History {
	merged := make(map[string]map[string]Counters)
	addDay := func(day, provider string, counters Counters) {
		providers := merged[day]
		if providers == nil {
			providers = make(map[string]Counters)
			merged[day] = providers
		}
		total := providers[provider]
		total.add(counters)
		providers[provider] = total
	}
	var history History
	if s.backfill != nil {
		cutoff := s.backfill.Cutoff
		if !cutoff.IsZero() {
			history.BackfillCutoff = &cutoff
		}
		history.BackfillMachines = s.backfill.Machines
		for day, providers := range s.backfill.Days {
			for provider, counters := range providers {
				addDay(day, provider, counters)
			}
		}
	}
	for day, providers := range s.daily {
		for provider, counters := range providers {
			addDay(day, provider, *counters)
		}
	}

	today := now.Format(dayLayout)
	weekday := (int(now.Weekday()) + 6) % 7 // Monday is 0
	weekStart := time.Date(now.Year(), now.Month(), now.Day()-weekday, 0, 0, 0, 0, now.Location()).Format(dayLayout)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format(dayLayout)

	history.Days = make([]HistoryDay, 0, len(merged))
	for day, providers := range merged {
		history.Days = append(history.Days, HistoryDay{Date: day, Providers: providers})
		for _, counters := range providers {
			history.Lifetime.add(counters)
			if day > today {
				continue
			}
			if day == today {
				history.Today.add(counters)
			}
			if day >= weekStart {
				history.ThisWeek.add(counters)
			}
			if day >= monthStart {
				history.ThisMonth.add(counters)
			}
		}
	}
	sort.Slice(history.Days, func(i, j int) bool { return history.Days[i].Date < history.Days[j].Date })
	return history
}

func (s *Store) pruneLocked(now time.Time) {
	hourCutoff := now.Add(-hourlyRetention).Unix()
	for authID, buckets := range s.hourly {
		for hourUnix := range buckets {
			if hourUnix < hourCutoff {
				delete(buckets, hourUnix)
				s.dirty = true
			}
		}
		if len(buckets) == 0 {
			delete(s.hourly, authID)
		}
	}
	perfCutoff := now.Add(-perfRetention).Unix()
	for authID, buckets := range s.perf {
		for hourUnix := range buckets {
			if hourUnix < perfCutoff {
				delete(buckets, hourUnix)
				s.dirty = true
			}
		}
		if len(buckets) == 0 {
			delete(s.perf, authID)
		}
	}
	s.pruneSessionMetaLocked(now)
	sessionCutoff := now.Add(-sessionRetention)
	for id, session := range s.sessions {
		if session.LastSeen.Before(sessionCutoff) {
			delete(s.sessions, id)
			s.dirty = true
		}
	}
	if len(s.sessions) > maxSessions {
		sessions := make([]*Session, 0, len(s.sessions))
		for _, session := range s.sessions {
			sessions = append(sessions, session)
		}
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].LastSeen.After(sessions[j].LastSeen) })
		for _, session := range sessions[maxSessions:] {
			delete(s.sessions, session.ID)
		}
		s.dirty = true
	}
}

// Flush writes the tally to disk when it changed since the last write.
func (s *Store) Flush() error {
	s.mu.Lock()
	if s.path == "" || !s.dirty {
		s.mu.Unlock()
		return nil
	}
	s.pruneLocked(s.nowFunc())
	data, errMarshal := json.Marshal(fileState{
		Version:       statsFileVersion,
		Hourly:        s.hourly,
		Daily:         s.daily,
		Sessions:      s.sessions,
		Perf:          s.perf,
		AuthProviders: s.authProviders,
		SessionMeta:   s.meta,
	})
	path := s.path
	s.dirty = false
	s.mu.Unlock()
	if errMarshal != nil {
		return errMarshal
	}

	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0o600); errWrite != nil {
		return errWrite
	}
	return os.Rename(tmp, path)
}

func (s *Store) loadLocked() {
	data, errRead := os.ReadFile(s.path)
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			log.Warnf("usagestats: read %s: %v", filepath.Base(s.path), errRead)
		}
		return
	}
	var state fileState
	if errUnmarshal := json.Unmarshal(data, &state); errUnmarshal != nil {
		log.Warnf("usagestats: parse %s: %v", filepath.Base(s.path), errUnmarshal)
		return
	}
	for authID, buckets := range state.Hourly {
		target := s.hourly[authID]
		if target == nil {
			target = make(map[int64]*Counters, len(buckets))
			s.hourly[authID] = target
		}
		for hour, bucket := range buckets {
			if bucket == nil {
				continue
			}
			if existing := target[hour]; existing != nil {
				existing.add(*bucket)
			} else {
				copyBucket := *bucket
				target[hour] = &copyBucket
			}
		}
	}
	if state.Daily == nil {
		// Stats files from before the daily tally: rebuild it from the hourly
		// buckets so the days the proxy has already served are not lost.
		location := s.nowFunc().Location()
		for authID, buckets := range state.Hourly {
			for hour, bucket := range buckets {
				if bucket == nil {
					continue
				}
				day := time.Unix(hour, 0).In(location).Format(dayLayout)
				s.dailyBucketLocked(day, providerOf("", authID)).add(*bucket)
			}
		}
		if len(state.Hourly) > 0 {
			s.dirty = true
		}
	}
	for day, providers := range state.Daily {
		for provider, bucket := range providers {
			if bucket != nil {
				s.dailyBucketLocked(day, provider).add(*bucket)
			}
		}
	}
	for id, session := range state.Sessions {
		if session != nil && s.sessions[id] == nil {
			s.sessions[id] = loadedSession(id, session)
		}
	}
	for authID, buckets := range state.Perf {
		target := s.perf[authID]
		if target == nil {
			target = make(map[int64]*perfBucket, len(buckets))
			s.perf[authID] = target
		}
		for hour, bucket := range buckets {
			if bucket == nil {
				continue
			}
			bucket.validate()
			if existing := target[hour]; existing != nil {
				existing.add(bucket)
			} else {
				target[hour] = bucket
			}
		}
	}
	for authID, provider := range state.AuthProviders {
		if s.authProviders[authID] == "" {
			s.authProviders[authID] = provider
		}
	}
	for id, meta := range state.SessionMeta {
		if meta != nil && s.meta[id] == nil {
			s.meta[id] = meta
		}
	}
}

// loadedSession repairs a session read from the stats file: the map key is
// its id, and nil per-account counts and invalid histogram entries are
// dropped so Summary never meets them.
func loadedSession(id string, session *Session) *Session {
	session.ID = id
	session.TTFTHist = session.TTFTHist.valid()
	for authID, count := range session.ByAuth {
		if count == nil {
			delete(session.ByAuth, authID)
		}
	}
	return session
}

// loadHistoryLocked reads the log backfill. A missing file is normal.
func (s *Store) loadHistoryLocked(path string) {
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			log.Warnf("usagestats: read %s: %v", filepath.Base(path), errRead)
		}
		return
	}
	var history historyFile
	if errUnmarshal := json.Unmarshal(data, &history); errUnmarshal != nil {
		log.Warnf("usagestats: parse %s: %v", filepath.Base(path), errUnmarshal)
		return
	}
	s.backfill = &history
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
