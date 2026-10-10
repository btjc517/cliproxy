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
	// the ends out to bucket boundaries can add one more.
	maxPerfBuckets = 200
)

// perfStepHours are the bucket lengths, in local hours, a custom window picks
// from, finest first: parts of a local day, which are runs of whole stored
// hours, then runs of 1, 2 and 7 whole local days. A 366 day window fits in
// 2 day buckets; 7 days is there for longer spans.
var perfStepHours = []int{1, 3, 6, 12, 24, 48, 168}

// dayGridAnchor is the local date multi-day buckets count from: Monday
// 5 January 1970, so 7 day buckets run Monday to Sunday and a bucket never
// moves when the window pans.
var dayGridAnchor = time.Date(1970, time.January, 5, 0, 0, 0, 0, time.UTC)

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
// the per day rollups are kept. Past the longest step it returns that step.
func perfStep(from, to, hourlySince time.Time) int {
	span := to.Sub(from)
	for _, hours := range perfStepHours {
		if hours < 24 && bucketStart(from, hourlySince.Location(), hours).Before(hourlySince) {
			continue
		}
		step := time.Duration(hours) * time.Hour
		if (span+step-1)/step <= maxPerfBuckets {
			return hours
		}
	}
	return perfStepHours[len(perfStepHours)-1]
}

// perfBucketStart is the start of the bucket of hours local hours that holds
// at. Up to a day it is bucketStart. Longer buckets are runs of hours/24
// whole local days counted from dayGridAnchor.
func perfBucketStart(at time.Time, loc *time.Location, hours int) time.Time {
	if hours <= 24 {
		return bucketStart(at, loc, hours)
	}
	days := hours / 24
	local := at.In(loc)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	offset := int(date.Sub(dayGridAnchor)/localDay) % days
	if offset < 0 {
		offset += days
	}
	return localDayStart(local.Year(), local.Month(), local.Day()-offset, loc)
}

// perfNextBucketStart is the start of the bucket after the one that starts at
// start.
func perfNextBucketStart(start time.Time, loc *time.Location, hours int) time.Time {
	if hours <= 24 {
		return nextBucketStart(start, loc, hours)
	}
	local := start.In(loc)
	return localDayStart(local.Year(), local.Month(), local.Day()+hours/24, loc)
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
	bounds := []time.Time{perfBucketStart(from, loc, hours).In(loc)}
	if !end.After(from) {
		return bounds
	}
	// The walk stops at end; the cap only guards against a broken zone.
	for limit := 0; bounds[len(bounds)-1].Before(end) && limit < 2*int(MaxPerfSpan/time.Hour); limit++ {
		bounds = append(bounds, perfNextBucketStart(bounds[len(bounds)-1], loc, hours).In(loc))
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
	return s.PerformanceBetweenPadded(from, to, time.Time{}, time.Time{}, ids, known)
}

// PerformanceBetweenPadded is PerformanceBetween with the series reaching
// over padFrom..padTo around the window, so a dashboard can pan and zoom a
// little without asking again. The bucket length is the one the window gets
// alone, and the totals, percentiles and histograms still cover exactly the
// window's buckets, whose edges FigureStart and FigureEnd give. The padding
// reaches at most one window length past each end and, for buckets shorter
// than a day, no further back than the hours still kept. Zero pads give
// PerformanceBetween's reply.
func (s *Store) PerformanceBetweenPadded(from, to, padFrom, padTo time.Time, ids, known []string) Performance {
	padded := !padFrom.IsZero() || !padTo.IsZero()
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
	hourlySince := now.Add(-perfRetention)
	hours := perfStep(from, to, hourlySince)
	bounds := perfWindowBounds(now, from, to, hours)
	performance.BucketSeconds = int64(time.Duration(hours) * time.Hour / time.Second)
	selection := s.knownSelectionLocked(ids, known)
	performance.Scopes = s.perfScopesLocked(loc, bounds, hours >= 24, selection)
	if !padded {
		return performance
	}
	figureStart, figureEnd := bounds[0], bounds[len(bounds)-1]
	performance.FigureStart, performance.FigureEnd = &figureStart, &figureEnd
	padFrom, padTo = clampPadding(from, to, padFrom, padTo)
	if hours < 24 {
		// Parts of a day exist only for the stored hours: the padding starts
		// at the first whole bucket inside them.
		first := perfBucketStart(hourlySince, loc, hours)
		if first.Before(hourlySince) {
			first = perfNextBucketStart(first, loc, hours)
		}
		if padFrom.Before(first) {
			padFrom = minTime(first, from)
		}
	}
	padBounds := perfWindowBounds(now, padFrom.In(loc), padTo.In(loc), hours)
	series := s.perfScopesLocked(loc, padBounds, hours >= 24, selection)
	for key, scope := range performance.Scopes {
		scope.Series = series[key].Series
		performance.Scopes[key] = scope
	}
	return performance
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
