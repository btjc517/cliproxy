package usagestats

import (
	"math"
	"sort"
	"strings"
	"time"
)

const (
	// histGrowth makes each histogram bucket about 10% wider than the last,
	// so a percentile read from it is within about 5% of the true value.
	histGrowth  = 1.1
	histBuckets = 160
	// ttftBase and throughputBase are the upper bounds of the first bucket:
	// 10 ms for times, 1 token per second for throughput.
	ttftBase       = 10.0
	throughputBase = 1.0
	// minThroughputTokens skips short replies whose generation time is mostly
	// noise.
	minThroughputTokens = 16
	// perfRetention is how long hourly timing is kept. Older hours are rolled
	// up into one bucket per credential per local day, kept forever.
	perfRetention  = 35 * 24 * time.Hour
	traceRetention = time.Hour
	maxTraces      = 4096
)

// histogram counts values by log-spaced bucket index.
type histogram map[int]int64

func histIndex(value, base float64) int {
	if value <= base {
		return 0
	}
	index := int(math.Ceil(math.Log(value/base)/math.Log(histGrowth) - 1e-9))
	if index >= histBuckets {
		return histBuckets - 1
	}
	return index
}

// histUpper is the upper bound of bucket index.
func histUpper(index int, base float64) float64 {
	return base * math.Pow(histGrowth, float64(index))
}

// histMid is the value a bucket stands for: the geometric middle of its range.
func histMid(index int, base float64) float64 {
	if index == 0 {
		return base
	}
	return base * math.Pow(histGrowth, float64(index)-0.5)
}

func (h *histogram) observe(value, base float64) {
	if *h == nil {
		*h = make(histogram)
	}
	(*h)[histIndex(value, base)]++
}

func (h *histogram) merge(other histogram) {
	if len(other) == 0 {
		return
	}
	if *h == nil {
		*h = make(histogram, len(other))
	}
	for index, count := range other {
		(*h)[index] += count
	}
}

// valid drops entries a damaged stats file could hold: indexes outside the
// histogram, which would turn into infinite bounds, and counts below one.
func (h histogram) valid() histogram {
	for index, count := range h {
		if index < 0 || index >= histBuckets || count <= 0 {
			delete(h, index)
		}
	}
	return h
}

func (h histogram) sortedIndexes() []int {
	indexes := make([]int, 0, len(h))
	for index, count := range h {
		if count > 0 {
			indexes = append(indexes, index)
		}
	}
	sort.Ints(indexes)
	return indexes
}

// percentile returns the approximate p-th quantile (0 < p <= 1), or 0 when
// the histogram is empty.
func (h histogram) percentile(p, base float64) float64 {
	var total int64
	for _, count := range h {
		total += count
	}
	if total == 0 {
		return 0
	}
	rank := int64(math.Ceil(p * float64(total)))
	if rank < 1 {
		rank = 1
	}
	var seen int64
	for _, index := range h.sortedIndexes() {
		seen += h[index]
		if seen >= rank {
			return histMid(index, base)
		}
	}
	return 0
}

func roundMillis(value float64) int64 { return int64(math.Round(value)) }

func roundTenth(value float64) float64 { return math.Round(value*10) / 10 }

// perfBucket is one hour, or one rolled up local day, of request timing for
// one credential, with the token sums of the same requests.
type perfBucket struct {
	Requests   int64     `json:"requests"`
	Failed     int64     `json:"failed"`
	Failovers  int64     `json:"failovers,omitempty"`
	Input      int64     `json:"input_tokens,omitempty"`
	Output     int64     `json:"output_tokens,omitempty"`
	CacheRead  int64     `json:"cache_read_tokens,omitempty"`
	CacheWrite int64     `json:"cache_write_tokens,omitempty"`
	TTFT       histogram `json:"ttft,omitempty"`
	Latency    histogram `json:"latency,omitempty"`
	Throughput histogram `json:"throughput,omitempty"`
}

func (b *perfBucket) addTokens(tokens Counters) {
	b.Input += tokens.Input
	b.Output += tokens.Output
	b.CacheRead += tokens.CacheRead
	b.CacheWrite += tokens.CacheWrite
}

func (b *perfBucket) hasTokens() bool {
	return b.Input != 0 || b.Output != 0 || b.CacheRead != 0 || b.CacheWrite != 0
}

// validate repairs a bucket read from the stats file.
func (b *perfBucket) validate() {
	b.TTFT = b.TTFT.valid()
	b.Latency = b.Latency.valid()
	b.Throughput = b.Throughput.valid()
}

func (b *perfBucket) add(other *perfBucket) {
	b.Requests += other.Requests
	b.Failed += other.Failed
	b.Failovers += other.Failovers
	b.Input += other.Input
	b.Output += other.Output
	b.CacheRead += other.CacheRead
	b.CacheWrite += other.CacheWrite
	b.TTFT.merge(other.TTFT)
	b.Latency.merge(other.Latency)
	b.Throughput.merge(other.Throughput)
}

// traceState follows the failed attempts of one inbound request to spot a
// failover. It is dropped once an attempt succeeds.
type traceState struct {
	failed map[string]struct{}
	seen   time.Time
}

// Window is a range the dashboard can show performance and usage for.
type Window struct {
	Key string
	// Bucket is the usual bucket length: whole local hours below a day, or one
	// local day. The "all" window switches to 7 local days on long histories.
	Bucket time.Duration
	// Buckets is the number of buckets. It is 0 for "all", which runs from the
	// first local day with data to today.
	Buckets int
}

const (
	localDay = 24 * time.Hour
	// allDailyDays is the longest history the "all" window shows in daily
	// buckets. Longer ones use 7 day buckets so the payload stays bounded.
	allDailyDays = 120
	allWeekDays  = 7
	// longRangeDays is the age the earliest data must reach before the
	// dashboard offers the 180d range.
	longRangeDays = 180
)

var windows = map[string]Window{
	"24h":  {Key: "24h", Bucket: time.Hour, Buckets: 24},
	"7d":   {Key: "7d", Bucket: 6 * time.Hour, Buckets: 28},
	"14d":  {Key: "14d", Bucket: 12 * time.Hour, Buckets: 28},
	"30d":  {Key: "30d", Bucket: localDay, Buckets: 30},
	"180d": {Key: "180d", Bucket: localDay, Buckets: longRangeDays},
	"all":  {Key: "all", Bucket: localDay},
}

// DefaultWindow is the range used when the caller names none.
const DefaultWindow = "24h"

// LookupWindow returns the window for a range key: "24h", "7d", "14d", "30d",
// "180d" or "all".
func LookupWindow(key string) (Window, bool) {
	window, ok := windows[key]
	return window, ok
}

// ResolveWindow returns the window for a range key, or the default window
// when the key is empty or unknown.
func ResolveWindow(key string) Window {
	if window, ok := windows[key]; ok {
		return window
	}
	return windows[DefaultWindow]
}

// availableRanges lists the range keys the dashboard offers, in order. 180d
// joins once earliest, the start of the oldest stored data, is at least 180
// local calendar days before now. A zero earliest means no data.
func availableRanges(now, earliest time.Time) []string {
	ranges := []string{"24h", "7d", "30d"}
	if !earliest.IsZero() && !earliest.After(now.AddDate(0, 0, -longRangeDays)) {
		ranges = append(ranges, "180d")
	}
	return append(ranges, "all")
}

// daily reports whether the window's buckets are whole local days.
func (w Window) daily() bool { return w.Bucket >= localDay }

// span returns the window's bucket bounds (see localBounds) ending with the
// bucket that holds now, and the usual bucket length. first is the start of
// the earliest data; only "all" uses it, and zero means none.
func (w Window) span(now, first time.Time) ([]time.Time, time.Duration) {
	if !w.daily() {
		return localBounds(now, int(w.Bucket/time.Hour), w.Buckets), w.Bucket
	}
	if w.Buckets > 0 {
		return dayBounds(now, w.Buckets, 1), localDay
	}
	days := 1
	if !first.IsZero() {
		days = max(1, localDaysBetween(first, now)+1)
	}
	if days <= allDailyDays {
		return dayBounds(now, days, 1), localDay
	}
	weeks := (days + allWeekDays - 1) / allWeekDays
	return dayBounds(now, weeks, allWeekDays), allWeekDays * localDay
}

// dayBounds returns count+1 instants in now's location: the starts of count
// runs of size local days, oldest first, the newest ending with today,
// followed by the start of tomorrow. With size 1 it matches
// localBounds(now, 24, count).
func dayBounds(now time.Time, count, size int) []time.Time {
	loc := now.Location()
	bounds := make([]time.Time, count+1)
	for i := range bounds {
		bounds[i] = localDayStart(now.Year(), now.Month(), now.Day()+1-(count-i)*size, loc)
	}
	return bounds
}

// localDayStart is the first instant of a local date; day may overflow the
// month as in time.Date. Noon is inside the date whatever the DST rules, and
// the walk back from it stops where the date begins, so a skipped or repeated
// midnight is handled the same way as in isBucketStart.
func localDayStart(year int, month time.Month, day int, loc *time.Location) time.Time {
	return bucketStart(time.Date(year, month, day, 12, 0, 0, 0, loc), loc, 24).In(loc)
}

// dateNoon returns noon on a "2006-01-02" date in loc: an instant inside that
// local day, for placing a daily tally in day sized buckets.
func dateNoon(date string, loc *time.Location) (time.Time, bool) {
	day, errParse := time.Parse(dayLayout, date)
	if errParse != nil {
		return time.Time{}, false
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, loc), true
}

// dateStart returns the first instant of a "2006-01-02" date in loc.
func dateStart(date string, loc *time.Location) (time.Time, bool) {
	day, errParse := time.Parse(dayLayout, date)
	if errParse != nil {
		return time.Time{}, false
	}
	return localDayStart(day.Year(), day.Month(), day.Day(), loc), true
}

// earliestDateStart returns the first instant of the earliest valid
// "2006-01-02" date in dates. Such dates sort as strings in calendar order, so
// only the earliest one is converted.
func earliestDateStart(dates []string, loc *time.Location) (time.Time, bool) {
	earliest := ""
	for _, date := range dates {
		if earliest != "" && date >= earliest {
			continue
		}
		if _, errParse := time.Parse(dayLayout, date); errParse == nil {
			earliest = date
		}
	}
	if earliest == "" {
		return time.Time{}, false
	}
	return dateStart(earliest, loc)
}

// localDaysBetween counts the local calendar days from from's date to to's
// date in to's location: 0 on the same date.
func localDaysBetween(from, to time.Time) int {
	from = from.In(to.Location())
	fromDate := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	toDate := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	return int(toDate.Sub(fromDate) / localDay)
}

// slotStep is the grid every bucket boundary sits on. Every time zone in use
// today is a whole number of quarter hours from UTC, and so is every DST
// change, so local hours, days and range buckets all start on this grid.
const slotStep = 15 * time.Minute

// maxBoundaryWalk bounds the search for a bucket start: a 12 hour bucket that
// gains a DST hour is 52 quarter hours, and a local day is at most 100.
const maxBoundaryWalk = 26 * 4

// isBucketStart reports whether a bucket of hours local hours starts at t, a
// point on the slotStep grid. A bucket starts where the local day or the
// bucket number (local hour / hours) changes from the quarter hour before, and
// also where the local clock reads a whole bucket hour again after a DST change
// turned it back. Buckets are therefore built from the same local clock for
// every size: each 6 or 12 hour bucket and each local day is a run of whole
// 1 hour buckets, and stats stored per 1 hour bucket never straddle a boundary.
// A local day (hours >= 24) starts only where the date changes, so a midnight
// repeated by a DST change does not start the day again.
func isBucketStart(t time.Time, loc *time.Location, hours int) bool {
	local := t.In(loc)
	if hours < 24 && local.Minute() == 0 && local.Second() == 0 && local.Hour()%hours == 0 {
		return true
	}
	prev := t.Add(-slotStep).In(loc)
	return local.YearDay() != prev.YearDay() || local.Year() != prev.Year() ||
		local.Hour()/hours != prev.Hour()/hours
}

// bucketStart is the start of the bucket of hours local hours that holds at.
func bucketStart(at time.Time, loc *time.Location, hours int) time.Time {
	start := at.Truncate(slotStep)
	for step := 0; step < maxBoundaryWalk && !isBucketStart(start, loc, hours); step++ {
		start = start.Add(-slotStep)
	}
	return start
}

// nextBucketStart is the start of the bucket after the one that holds at.
func nextBucketStart(at time.Time, loc *time.Location, hours int) time.Time {
	next := at.Truncate(slotStep).Add(slotStep)
	for step := 0; step < maxBoundaryWalk && !isBucketStart(next, loc, hours); step++ {
		next = next.Add(slotStep)
	}
	return next
}

// localBounds returns count+1 instants in now's location: the starts of count
// buckets of hours local hours, oldest first, ending with the bucket that
// holds now, followed by the end of that newest bucket. Buckets that cross a
// DST change are an hour longer or shorter than the rest.
func localBounds(now time.Time, hours, count int) []time.Time {
	loc := now.Location()
	bounds := make([]time.Time, count+1)
	bounds[count] = nextBucketStart(now, loc, hours).In(loc)
	bounds[count-1] = bucketStart(now, loc, hours).In(loc)
	for i := count - 2; i >= 0; i-- {
		bounds[i] = bucketStart(bounds[i+1].Add(-slotStep), loc, hours).In(loc)
	}
	return bounds
}

// boundsIndex returns the bucket of bounds that holds at, or false when at is
// before the first bucket or not before the end of the last.
func boundsIndex(bounds []time.Time, at time.Time) (int, bool) {
	index := sort.Search(len(bounds), func(i int) bool { return bounds[i].After(at) }) - 1
	return index, index >= 0 && index < len(bounds)-1
}

// Performance is request timing over the selected range.
type Performance struct {
	Range string `json:"range"`
	// BucketSeconds is the usual bucket length. A bucket that crosses a DST
	// change is longer or shorter, so each series point carries its start.
	BucketSeconds int64 `json:"bucket_seconds"`
	// Ranges are the range keys the dashboard offers, and Since is the start
	// of the earliest stored performance data.
	Ranges []string        `json:"ranges"`
	Since  *time.Time      `json:"since,omitempty"`
	Scopes map[string]Perf `json:"scopes"`
}

// Perf is request timing for one scope: everything, one provider or one
// credential. Percentiles are approximate and 0 means no data.
type Perf struct {
	Requests       int64             `json:"requests"`
	Failed         int64             `json:"failed"`
	Failovers      int64             `json:"failovers"`
	TTFT           TimePercentiles   `json:"ttft_ms"`
	Latency        TimePercentiles   `json:"latency_ms"`
	Throughput     ThroughputSummary `json:"throughput"`
	TTFTHist       []TimeBin         `json:"ttft_hist"`
	ThroughputHist []ThroughputBin   `json:"throughput_hist"`
	Series         []PerfPoint       `json:"series"`
}

// TimePercentiles are durations in milliseconds.
type TimePercentiles struct {
	P50 int64 `json:"p50"`
	P90 int64 `json:"p90"`
	P99 int64 `json:"p99"`
}

// ThroughputSummary is output tokens per second of generation time.
type ThroughputSummary struct {
	P50 float64 `json:"p50"`
	P10 float64 `json:"p10"`
}

// TimeBin is one non-empty histogram bucket of durations.
type TimeBin struct {
	LeMs  int64 `json:"le_ms"`
	Count int64 `json:"count"`
}

// ThroughputBin is one non-empty histogram bucket of throughput.
type ThroughputBin struct {
	Le    float64 `json:"le"`
	Count int64   `json:"count"`
}

// PerfPoint is one bucket of the series. Latency is the full response time,
// ThroughputP10 the slowest 10%, and the token fields are sums over the
// bucket's requests.
type PerfPoint struct {
	Start         time.Time `json:"start"`
	Requests      int64     `json:"requests"`
	Failed        int64     `json:"failed"`
	Failovers     int64     `json:"failovers"`
	TTFTP50       int64     `json:"ttft_p50_ms"`
	TTFTP90       int64     `json:"ttft_p90_ms"`
	LatencyP50    int64     `json:"latency_p50_ms"`
	LatencyP90    int64     `json:"latency_p90_ms"`
	ThroughputP50 float64   `json:"throughput_p50"`
	ThroughputP10 float64   `json:"throughput_p10"`
	Input         int64     `json:"input_tokens"`
	Output        int64     `json:"output_tokens"`
	CacheRead     int64     `json:"cache_read_tokens"`
	CacheWrite    int64     `json:"cache_write_tokens"`
}

// perfAccumulator gathers one scope's buckets before they become a Perf.
type perfAccumulator struct {
	total  perfBucket
	series []perfBucket
}

func (a *perfAccumulator) add(index int, bucket *perfBucket) {
	a.total.add(bucket)
	a.series[index].add(bucket)
}

func (a *perfAccumulator) build(bounds []time.Time) Perf {
	perf := Perf{
		Requests:  a.total.Requests,
		Failed:    a.total.Failed,
		Failovers: a.total.Failovers,
		TTFT: TimePercentiles{
			P50: roundMillis(a.total.TTFT.percentile(0.5, ttftBase)),
			P90: roundMillis(a.total.TTFT.percentile(0.9, ttftBase)),
			P99: roundMillis(a.total.TTFT.percentile(0.99, ttftBase)),
		},
		Latency: TimePercentiles{
			P50: roundMillis(a.total.Latency.percentile(0.5, ttftBase)),
			P90: roundMillis(a.total.Latency.percentile(0.9, ttftBase)),
			P99: roundMillis(a.total.Latency.percentile(0.99, ttftBase)),
		},
		Throughput: ThroughputSummary{
			P50: roundTenth(a.total.Throughput.percentile(0.5, throughputBase)),
			P10: roundTenth(a.total.Throughput.percentile(0.1, throughputBase)),
		},
		TTFTHist:       []TimeBin{},
		ThroughputHist: []ThroughputBin{},
		Series:         make([]PerfPoint, len(a.series)),
	}
	for _, index := range a.total.TTFT.sortedIndexes() {
		perf.TTFTHist = append(perf.TTFTHist, TimeBin{LeMs: roundMillis(histUpper(index, ttftBase)), Count: a.total.TTFT[index]})
	}
	for _, index := range a.total.Throughput.sortedIndexes() {
		perf.ThroughputHist = append(perf.ThroughputHist, ThroughputBin{Le: roundTenth(histUpper(index, throughputBase)), Count: a.total.Throughput[index]})
	}
	for i := range a.series {
		bucket := &a.series[i]
		perf.Series[i] = PerfPoint{
			Start:         bounds[i],
			Requests:      bucket.Requests,
			Failed:        bucket.Failed,
			Failovers:     bucket.Failovers,
			TTFTP50:       roundMillis(bucket.TTFT.percentile(0.5, ttftBase)),
			TTFTP90:       roundMillis(bucket.TTFT.percentile(0.9, ttftBase)),
			LatencyP50:    roundMillis(bucket.Latency.percentile(0.5, ttftBase)),
			LatencyP90:    roundMillis(bucket.Latency.percentile(0.9, ttftBase)),
			ThroughputP50: roundTenth(bucket.Throughput.percentile(0.5, throughputBase)),
			ThroughputP10: roundTenth(bucket.Throughput.percentile(0.1, throughputBase)),
			Input:         bucket.Input,
			Output:        bucket.Output,
			CacheRead:     bucket.CacheRead,
			CacheWrite:    bucket.CacheWrite,
		}
	}
	return perf
}

// perfSinceLocked returns the start of the earliest stored performance data in
// loc: the earliest hour, or the start of the earliest rolled up day.
func (s *Store) perfSinceLocked(loc *time.Location) (time.Time, bool) {
	var since time.Time
	found := false
	consider := func(at time.Time) {
		if !found || at.Before(since) {
			since, found = at, true
		}
	}
	for _, buckets := range s.perf {
		for hourUnix := range buckets {
			consider(time.Unix(hourUnix, 0))
		}
	}
	dates := make([]string, 0, len(s.perfDaily))
	for _, days := range s.perfDaily {
		for date := range days {
			dates = append(dates, date)
		}
	}
	if start, ok := earliestDateStart(dates, loc); ok {
		consider(start)
	}
	if !found {
		return time.Time{}, false
	}
	return since.In(loc), true
}

// SelectionScope is the performance scope that merges the credentials a
// caller selected, and MaxSelectionIDs is the most ids a selection takes.
const (
	SelectionScope  = "selection"
	MaxSelectionIDs = 64
)

// ParseSelection splits a comma separated list of credential ids, as sent in
// ?scope=. It trims each id, drops empty ones and repeats, and keeps the first
// MaxSelectionIDs distinct ids.
func ParseSelection(raw string) []string {
	var ids []string
	seen := make(map[string]struct{})
	for rest := raw; rest != "" && len(ids) < MaxSelectionIDs; {
		var id string
		id, rest, _ = strings.Cut(rest, ",")
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// knownSelectionLocked returns, as a set, the ids among the first
// MaxSelectionIDs of ids that the tally has seen or that known lists. It
// returns nil when none is left.
func (s *Store) knownSelectionLocked(ids, known []string) map[string]struct{} {
	listed := make(map[string]struct{}, len(known))
	for _, id := range known {
		listed[id] = struct{}{}
	}
	selection := make(map[string]struct{})
	for _, id := range ids[:min(len(ids), MaxSelectionIDs)] {
		_, inList := listed[id]
		_, inHourly := s.hourly[id]
		_, inDaily := s.accountDaily[id]
		_, inPerf := s.perf[id]
		_, inPerfDaily := s.perfDaily[id]
		_, inProviders := s.authProviders[id]
		if inList || inHourly || inDaily || inPerf || inPerfDaily || inProviders {
			selection[id] = struct{}{}
		}
	}
	if len(selection) == 0 {
		return nil
	}
	return selection
}

// performanceLocked builds the performance view for window ending at now.
// Hourly buckets and daily rollups never hold the same request, so windows of
// whole days read both without counting anything twice. Shorter buckets cannot
// split a day, so they read only the hourly buckets, which cover the last 35
// days. A non-empty selection adds the SelectionScope, built from the raw
// buckets of those credentials the same way as the provider scopes, so its
// percentiles come from the merged histograms.
func (s *Store) performanceLocked(now time.Time, window Window, selection map[string]struct{}) Performance {
	loc := now.Location()
	since, hasData := s.perfSinceLocked(loc)
	bounds, bucketLength := window.span(now, since)
	count := len(bounds) - 1
	scopes := make(map[string]*perfAccumulator)
	scope := func(key string) *perfAccumulator {
		acc := scopes[key]
		if acc == nil {
			acc = &perfAccumulator{series: make([]perfBucket, count)}
			scopes[key] = acc
		}
		return acc
	}
	scope("all")
	scope("claude")
	scope("codex")
	for authID := range s.hourly {
		scope(authID)
	}
	if len(selection) > 0 {
		scope(SelectionScope)
	}
	targetsFor := func(authID string) []*perfAccumulator {
		provider := s.authProviders[authID]
		if provider == "" {
			provider = providerOf("", authID)
		}
		targets := []*perfAccumulator{scope("all"), scope(provider), scope(authID)}
		if _, selected := selection[authID]; selected {
			targets = append(targets, scope(SelectionScope))
		}
		return targets
	}
	addTo := func(targets []*perfAccumulator, at time.Time, bucket *perfBucket) {
		index, ok := boundsIndex(bounds, at)
		if !ok {
			return
		}
		for _, target := range targets {
			target.add(index, bucket)
		}
	}
	for authID, buckets := range s.perf {
		targets := targetsFor(authID)
		for hourUnix, bucket := range buckets {
			addTo(targets, time.Unix(hourUnix, 0), bucket)
		}
	}
	if window.daily() {
		for authID, days := range s.perfDaily {
			targets := targetsFor(authID)
			for date, bucket := range days {
				if noon, ok := dateNoon(date, loc); ok {
					addTo(targets, noon, bucket)
				}
			}
		}
	}
	performance := Performance{
		Range:         window.Key,
		BucketSeconds: int64(bucketLength / time.Second),
		Ranges:        availableRanges(now, since),
		Scopes:        make(map[string]Perf, len(scopes)),
	}
	if hasData {
		performance.Since = &since
	}
	for key, acc := range scopes {
		performance.Scopes[key] = acc.build(bounds)
	}
	return performance
}

// rollUpPerfLocked moves hourly timing that started before cutoff into its
// credential's local day, in cutoff's location, and reports whether it moved
// any.
func (s *Store) rollUpPerfLocked(cutoff time.Time) bool {
	loc := cutoff.Location()
	cutoffUnix := cutoff.Unix()
	moved := false
	for authID, buckets := range s.perf {
		for hourUnix, bucket := range buckets {
			if hourUnix >= cutoffUnix {
				continue
			}
			date := time.Unix(hourUnix, 0).In(loc).Format(dayLayout)
			s.perfDayLocked(authID, date).add(bucket)
			delete(buckets, hourUnix)
			moved = true
		}
		if len(buckets) == 0 {
			delete(s.perf, authID)
		}
	}
	return moved
}

// perfDayLocked returns the rolled up day of timing for a credential.
func (s *Store) perfDayLocked(authID, date string) *perfBucket {
	days := s.perfDaily[authID]
	if days == nil {
		days = make(map[string]*perfBucket)
		s.perfDaily[authID] = days
	}
	bucket := days[date]
	if bucket == nil {
		bucket = &perfBucket{}
		days[date] = bucket
	}
	return bucket
}

// recordPerfLocked adds one attempt's timing and tokens to its credential's
// hour, keyed by the start of that local hour (bucketStart with 1 hour), and
// counts a failover when an inbound request that already failed on another
// credential succeeds here.
func (s *Store) recordPerfLocked(authID, provider string, hourStart time.Time, record recordTiming) {
	if provider != "" {
		s.authProviders[authID] = provider
	}
	buckets := s.perf[authID]
	if buckets == nil {
		buckets = make(map[int64]*perfBucket)
		s.perf[authID] = buckets
	}
	hour := hourStart.Unix()
	bucket := buckets[hour]
	if bucket == nil {
		bucket = &perfBucket{}
		buckets[hour] = bucket
	}
	bucket.Requests++
	bucket.addTokens(record.tokens)
	if record.failed {
		bucket.Failed++
	} else {
		if record.ttft > 0 {
			bucket.TTFT.observe(float64(record.ttft)/float64(time.Millisecond), ttftBase)
		}
		if record.latency > 0 {
			bucket.Latency.observe(float64(record.latency)/float64(time.Millisecond), ttftBase)
		}
		// Only a reply streamed from upstream has a first token time that splits
		// waiting from generating. A buffered reply's first byte comes after the
		// whole body is ready.
		if generation := record.latency - record.ttft; record.stream && record.ttft > 0 && generation > 0 && record.tokens.Output >= minThroughputTokens {
			bucket.Throughput.observe(float64(record.tokens.Output)/generation.Seconds(), throughputBase)
		}
	}

	if record.traceID == "" {
		return
	}
	trace := s.traces[record.traceID]
	if record.failed {
		if trace == nil {
			s.pruneTracesLocked(record.at)
			trace = &traceState{failed: make(map[string]struct{})}
			s.traces[record.traceID] = trace
		}
		trace.failed[authID] = struct{}{}
		if record.at.After(trace.seen) {
			trace.seen = record.at
		}
		return
	}
	if trace == nil {
		return
	}
	// The inbound request has an answer, so its retry chain is over.
	delete(s.traces, record.traceID)
	for failedAuth := range trace.failed {
		if failedAuth != authID {
			bucket.Failovers++
			return
		}
	}
}

// pruneTracesLocked makes room for one more trace: it drops traces older than
// traceRetention and, when that is not enough, the single oldest trace.
func (s *Store) pruneTracesLocked(now time.Time) {
	if len(s.traces) < maxTraces {
		return
	}
	cutoff := now.Add(-traceRetention)
	oldestID := ""
	var oldest time.Time
	for id, trace := range s.traces {
		if trace.seen.Before(cutoff) {
			delete(s.traces, id)
			continue
		}
		if oldestID == "" || trace.seen.Before(oldest) {
			oldestID, oldest = id, trace.seen
		}
	}
	if len(s.traces) >= maxTraces {
		delete(s.traces, oldestID)
	}
}

// recordTiming is the part of a usage record the performance view needs.
type recordTiming struct {
	traceID string
	at      time.Time
	// stream is true when the upstream reply arrived as a stream
	// (coreusage.Record.UpstreamStream), whatever the client asked for.
	stream  bool
	failed  bool
	ttft    time.Duration
	latency time.Duration
	// tokens are the attempt's token counts.
	tokens Counters
}
