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

func (b *perfBucket) add(other *perfBucket) {
	b.Requests += other.Requests
	b.Failed += other.Failed
	b.Failovers += other.Failovers
	b.TTFT.merge(other.TTFT)
	b.Latency.merge(other.Latency)
	b.Throughput.merge(other.Throughput)
}

// traceState follows the attempts of one inbound request to spot failovers.
type traceState struct {
	failed  map[string]struct{}
	seen    time.Time
	counted bool
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

// firstBucketStart is the start of the oldest bucket. Buckets line up with
// local midnight, and the newest one holds now.
func (w Window) firstBucketStart(now time.Time) time.Time {
	hours := int(w.Bucket / time.Hour)
	last := time.Date(now.Year(), now.Month(), now.Day(), now.Hour()/hours*hours, 0, 0, 0, now.Location())
	return last.Add(-time.Duration(w.Buckets-1) * w.Bucket)
}

// Performance is request timing over the selected range.
type Performance struct {
	Range         string          `json:"range"`
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

func (a *perfAccumulator) build(first time.Time, window Window) Perf {
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
			Start:         first.Add(time.Duration(i) * window.Bucket),
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
	first := window.firstBucketStart(now)
	end := first.Add(time.Duration(window.Buckets) * window.Bucket)
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
			start := time.Unix(hourUnix, 0)
			if start.Before(first) || !start.Before(end) {
				continue
			}
			index := int(start.Sub(first) / window.Bucket)
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
		performance.Scopes[key] = acc.build(first.In(now.Location()), window)
	}
	return performance
}

// recordPerfLocked adds one attempt's timing to its credential's hour and
// counts a failover when an inbound request that already failed on another
// credential succeeds here.
func (s *Store) recordPerfLocked(authID, provider string, at time.Time, record recordTiming) {
	if provider != "" {
		s.authProviders[authID] = provider
	}
	buckets := s.perf[authID]
	if buckets == nil {
		buckets = make(map[int64]*perfBucket)
		s.perf[authID] = buckets
	}
	hour := at.Truncate(time.Hour).Unix()
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
		if generation := record.latency - record.ttft; record.ttft > 0 && generation > 0 && record.output >= minThroughputTokens {
			bucket.Throughput.observe(float64(record.output)/generation.Seconds(), throughputBase)
		}
	}

	if record.traceID == "" {
		return
	}
	trace := s.traces[record.traceID]
	if record.failed {
		if trace == nil {
			s.pruneTracesLocked(at)
			trace = &traceState{failed: make(map[string]struct{})}
			s.traces[record.traceID] = trace
		}
		trace.failed[authID] = struct{}{}
		trace.seen = at
		return
	}
	if trace == nil || trace.counted {
		return
	}
	for failedAuth := range trace.failed {
		if failedAuth != authID {
			bucket.Failovers++
			trace.counted = true
			return
		}
	}
}

func (s *Store) pruneTracesLocked(now time.Time) {
	if len(s.traces) < maxTraces {
		return
	}
	cutoff := now.Add(-traceRetention)
	for id, trace := range s.traces {
		if trace.seen.Before(cutoff) {
			delete(s.traces, id)
		}
	}
	if len(s.traces) >= maxTraces {
		s.traces = make(map[string]*traceState)
	}
}

// recordTiming is the part of a usage record the performance view needs.
type recordTiming struct {
	traceID string
	failed  bool
	ttft    time.Duration
	latency time.Duration
	output  int64
}
