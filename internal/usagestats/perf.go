package usagestats

import (
	"math"
	"sort"
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
	perfRetention       = 15 * 24 * time.Hour
	traceRetention      = time.Hour
	maxTraces           = 4096
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

// perfBucket is one hour of request timing for one credential.
type perfBucket struct {
	Requests   int64     `json:"requests"`
	Failed     int64     `json:"failed"`
	Failovers  int64     `json:"failovers,omitempty"`
	TTFT       histogram `json:"ttft,omitempty"`
	Latency    histogram `json:"latency,omitempty"`
	Throughput histogram `json:"throughput,omitempty"`
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

// Window is a range the dashboard can show performance for.
type Window struct {
	Key     string
	Bucket  time.Duration
	Buckets int
}

var windows = map[string]Window{
	"24h": {Key: "24h", Bucket: time.Hour, Buckets: 24},
	"7d":  {Key: "7d", Bucket: 6 * time.Hour, Buckets: 28},
	"14d": {Key: "14d", Bucket: 12 * time.Hour, Buckets: 28},
}

// DefaultWindow is the range used when the caller names none.
const DefaultWindow = "24h"

// LookupWindow returns the window for a range key such as "24h", "7d" or "14d".
func LookupWindow(key string) (Window, bool) {
	window, ok := windows[key]
	return window, ok
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
func isBucketStart(t time.Time, loc *time.Location, hours int) bool {
	local := t.In(loc)
	if local.Minute() == 0 && local.Second() == 0 && local.Hour()%hours == 0 {
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

// bounds returns the window's bucket starts and the end of its newest bucket.
// Buckets start on local hours that are a multiple of the bucket size, so they
// line up with local midnight.
func (w Window) bounds(now time.Time) []time.Time {
	return localBounds(now, int(w.Bucket/time.Hour), w.Buckets)
}

// Performance is request timing over the selected range.
type Performance struct {
	Range string `json:"range"`
	// BucketSeconds is the usual bucket length. A bucket that crosses a DST
	// change is longer or shorter, so each series point carries its start.
	BucketSeconds int64           `json:"bucket_seconds"`
	Scopes        map[string]Perf `json:"scopes"`
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

// PerfPoint is one bucket of the series.
type PerfPoint struct {
	Start         time.Time `json:"start"`
	Requests      int64     `json:"requests"`
	Failed        int64     `json:"failed"`
	TTFTP50       int64     `json:"ttft_p50_ms"`
	TTFTP90       int64     `json:"ttft_p90_ms"`
	ThroughputP50 float64   `json:"throughput_p50"`
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
			TTFTP50:       roundMillis(bucket.TTFT.percentile(0.5, ttftBase)),
			TTFTP90:       roundMillis(bucket.TTFT.percentile(0.9, ttftBase)),
			ThroughputP50: roundTenth(bucket.Throughput.percentile(0.5, throughputBase)),
		}
	}
	return perf
}

// performanceLocked builds the performance view for window ending at now.
func (s *Store) performanceLocked(now time.Time, window Window) Performance {
	bounds := window.bounds(now)
	scopes := make(map[string]*perfAccumulator)
	scope := func(key string) *perfAccumulator {
		acc := scopes[key]
		if acc == nil {
			acc = &perfAccumulator{series: make([]perfBucket, window.Buckets)}
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
	for authID, buckets := range s.perf {
		provider := s.authProviders[authID]
		if provider == "" {
			provider = providerOf("", authID)
		}
		targets := []*perfAccumulator{scope("all"), scope(provider), scope(authID)}
		for hourUnix, bucket := range buckets {
			index, ok := boundsIndex(bounds, time.Unix(hourUnix, 0))
			if !ok {
				continue
			}
			for _, target := range targets {
				target.add(index, bucket)
			}
		}
	}
	performance := Performance{
		Range:         window.Key,
		BucketSeconds: int64(window.Bucket / time.Second),
		Scopes:        make(map[string]Perf, len(scopes)),
	}
	for key, acc := range scopes {
		performance.Scopes[key] = acc.build(bounds)
	}
	return performance
}

// recordPerfLocked adds one attempt's timing to its credential's hour, keyed
// by the start of that local hour (bucketStart with 1 hour), and counts a
// failover when an inbound request that already failed on another credential
// succeeds here.
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
		if generation := record.latency - record.ttft; record.stream && record.ttft > 0 && generation > 0 && record.output >= minThroughputTokens {
			bucket.Throughput.observe(float64(record.output)/generation.Seconds(), throughputBase)
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
	output  int64
}
