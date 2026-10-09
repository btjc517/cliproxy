package usagestats

import (
	"errors"
	"time"
)

const (
	// CustomRange is the Performance.Range of a window the caller chose.
	CustomRange = "custom"
	// MinPerfSpan and MaxPerfSpan bound the length of a custom window.
	MinPerfSpan = time.Hour
	MaxPerfSpan = 366 * 24 * time.Hour
	// maxPerfBuckets is the most buckets a custom window aims for. Snapping
	// the ends out to bucket boundaries can add one more, and windows longer
	// than maxPerfBuckets days stay in one day buckets, so they have more.
	maxPerfBuckets = 200
)

// perfStepHours are the bucket lengths, in local hours, a custom window picks
// from, finest first. Each one divides a local day, so every bucket is a run
// of whole stored hours.
var perfStepHours = []int{1, 3, 6, 12, 24}

// ErrPerfWindow reports a custom performance window that is out of order,
// shorter than MinPerfSpan or longer than MaxPerfSpan.
var ErrPerfWindow = errors.New("perf window must be ordered and between 1 hour and 366 days")

// ValidPerfWindow reports whether from and to make a custom window.
func ValidPerfWindow(from, to time.Time) error {
	span := to.Sub(from)
	if span < MinPerfSpan || span > MaxPerfSpan {
		return ErrPerfWindow
	}
	return nil
}

// perfStep picks the bucket length, in local hours, for a custom window: the
// finest step that gives at most maxPerfBuckets buckets over the whole window,
// and whole local days when the window starts before hourlySince, where only
// the per day rollups are kept.
func perfStep(from, to, hourlySince time.Time) int {
	span := to.Sub(from)
	for _, hours := range perfStepHours {
		if hours >= 24 {
			break
		}
		step := time.Duration(hours) * time.Hour
		if bucketStart(from, hourlySince.Location(), hours).Before(hourlySince) {
			break
		}
		if (span+step-1)/step <= maxPerfBuckets {
			return hours
		}
	}
	return 24
}

// perfWindowBounds returns the bucket bounds of a custom window: from the
// start of the bucket of hours local hours that holds from to the end of the
// bucket that holds the last instant before to, with to clamped to now. A
// window that starts at or after now has no buckets: the result is one bound.
func perfWindowBounds(now, from, to time.Time, hours int) []time.Time {
	loc := now.Location()
	end := to
	if end.After(now) {
		end = now
	}
	bounds := []time.Time{bucketStart(from, loc, hours).In(loc)}
	if !end.After(from) {
		return bounds
	}
	// The walk stops at end; the cap only guards against a broken zone.
	for limit := 0; bounds[len(bounds)-1].Before(end) && limit < 2*int(MaxPerfSpan/time.Hour); limit++ {
		bounds = append(bounds, nextBucketStart(bounds[len(bounds)-1], loc, hours).In(loc))
	}
	return bounds
}

// PerformanceBetween builds the performance view for a window the caller
// chose, with Range CustomRange. The bucket length comes from perfStep, the
// series stops at the bucket that holds now, and the totals, percentiles and
// histograms cover exactly the series buckets: whole stored hours or days,
// never a share of one. Selection works as in SummaryForSelection. A window
// that fails ValidPerfWindow gives empty scopes.
func (s *Store) PerformanceBetween(from, to time.Time, ids, known []string) Performance {
	now := s.nowFunc()
	loc := now.Location()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)

	since, hasData := s.perfSinceLocked(loc)
	performance := Performance{Range: CustomRange, Ranges: availableRanges(now, since)}
	if hasData {
		performance.Since = &since
	}
	if ValidPerfWindow(from, to) != nil {
		performance.Scopes = map[string]Perf{}
		return performance
	}
	from, to = from.In(loc), to.In(loc)
	hours := perfStep(from, to, now.Add(-perfRetention))
	bounds := perfWindowBounds(now, from, to, hours)
	performance.BucketSeconds = int64(time.Duration(hours) * time.Hour / time.Second)
	performance.Scopes = s.perfScopesLocked(loc, bounds, hours >= 24, s.knownSelectionLocked(ids, known))
	return performance
}
