package auth

import (
	"math"
	"sort"
	"time"
)

const (
	// Weekly meters are drawn over the last week, 5-hour meters over the last day.
	longHistorySpan  = 7 * 24 * time.Hour
	longHistoryStep  = 30 * time.Minute
	shortHistorySpan = 24 * time.Hour
	shortHistoryStep = 10 * time.Minute
	burnedSpan       = 24 * time.Hour
)

// MeterSeries is one meter's used share on a fixed time grid, for the
// dashboard's allowance chart, with how fast it has been filling.
type MeterSeries struct {
	Name          string    `json:"name"`
	Long          bool      `json:"long"`
	Start         time.Time `json:"start"`
	StepSeconds   int64     `json:"step_seconds"`
	WindowSeconds int64     `json:"window_seconds,omitempty"`
	// Used is the used share in thousandths at the end of each step, or null
	// before the first reading. The last step contains now.
	Used []*int `json:"used"`
	// Utilization and ResetAt are the meter now, after any reset since the last reading.
	Utilization float64   `json:"utilization"`
	ResetAt     time.Time `json:"reset_at,omitempty"`
	FirstAt     time.Time `json:"first_at"`
	// BurnPerHour is the recent fill rate the scorer uses, as a share per hour.
	BurnPerHour *float64 `json:"burn_per_hour,omitempty"`
	// Burned is the share used between BurnedSince and now, counting usage
	// on both sides of a reset. BurnedSince is 24 hours ago, or the first
	// reading when history is shorter.
	Burned      float64   `json:"burned"`
	BurnedSince time.Time `json:"burned_since"`
}

// MeterHistory returns, per credential, the 5-hour and weekly meters as
// series for the dashboard. Credentials with no readings are left out.
func (s *RoutingState) MeterHistory(authIDs []string, now time.Time) map[string][]MeterSeries {
	out := make(map[string][]MeterSeries)
	for _, id := range authIDs {
		account := s.account(id)
		if account == nil {
			continue
		}
		var series []MeterSeries
		for name, meter := range account.Meters {
			if meter == nil || !meterIsMain(name) {
				continue
			}
			samples := account.History[name]
			if len(samples) == 0 {
				continue
			}
			series = append(series, meterSeries(*meter, samples, now))
		}
		if len(series) == 0 {
			continue
		}
		sort.Slice(series, func(i, j int) bool { return !series[i].Long && series[j].Long })
		out[id] = series
	}
	return out
}

// meterIsMain reports whether name is a provider's 5-hour or shared weekly window.
func meterIsMain(name string) bool {
	switch name {
	case "5h", "7d", "primary", "secondary":
		return true
	}
	return false
}

func meterSeries(meter Meter, samples []MeterSample, now time.Time) MeterSeries {
	long := meterIsLong(&meter)
	span, step := shortHistorySpan, shortHistoryStep
	if long {
		span, step = longHistorySpan, longHistoryStep
	}
	effective, wasReset := effectiveMeter(meter, now)
	series := MeterSeries{
		Name:          meter.Name,
		Long:          long,
		StepSeconds:   int64(step / time.Second),
		WindowSeconds: int64(meter.Window / time.Second),
		Utilization:   effective.Utilization,
		ResetAt:       effective.ResetAt,
		FirstAt:       samples[0].At,
	}
	if !wasReset {
		if rate, known := burnRate(samples, now, burnLookback); known {
			series.BurnPerHour = &rate
		}
	}
	series.Burned, series.BurnedSince = burnedSince(samples, now.Add(-burnedSpan))

	start := now.Add(-span).Truncate(step)
	series.Start = start
	steps := int(now.Sub(start)/step) + 1
	series.Used = make([]*int, steps)
	next := 0
	var last *MeterSample
	for i := 0; i < steps; i++ {
		end := start.Add(time.Duration(i+1) * step)
		if end.After(now) {
			end = now
		}
		for next < len(samples) && !samples[next].At.After(end) {
			last = &samples[next]
			next++
		}
		if last == nil {
			continue
		}
		used := last.Utilization
		if resetBetween(meter, last.At, end) {
			used = 0
		}
		value := int(math.Round(used * 1000))
		series.Used[i] = &value
	}
	return series
}

// resetBetween reports whether the meter's window certainly reset after a
// reading at from and by to. Weekly windows reset on a fixed weekly grid
// anchored at the last reported reset; a 5-hour window resets at its
// reported time, or at the latest one window after the reading.
func resetBetween(meter Meter, from, to time.Time) bool {
	if !to.After(from) {
		return false
	}
	if meter.Window <= 0 {
		return !meter.ResetAt.IsZero() && meter.ResetAt.After(from) && !meter.ResetAt.After(to)
	}
	if meterIsLong(&meter) {
		if meter.ResetAt.IsZero() {
			return false
		}
		// The first reset on the grid after from.
		k := math.Floor(float64(meter.ResetAt.Sub(from)) / float64(meter.Window))
		first := meter.ResetAt.Add(-time.Duration(k) * meter.Window)
		if !first.After(from) {
			first = first.Add(meter.Window)
		}
		return !first.After(to)
	}
	if !meter.ResetAt.IsZero() && meter.ResetAt.After(from) && !meter.ResetAt.After(to) {
		return true
	}
	return to.Sub(from) >= meter.Window
}

// resetDrop is how far a reading must fall below the highest one since the
// last reset to count as a reset. Replies that finish out of order report a
// point or two lower than the one before; that is not a reset.
const resetDrop = 0.03

// burnedSince sums how much of the meter was used from since to the last
// sample. After a reset the new reading counts in full. It returns the start
// actually covered: since, or the first reading.
func burnedSince(samples []MeterSample, since time.Time) (float64, time.Time) {
	begin := sort.Search(len(samples), func(i int) bool { return !samples[i].At.Before(since) })
	covered := since
	if begin > 0 {
		// The reading just before the span is its baseline.
		begin--
	} else if len(samples) > 0 {
		covered = samples[0].At
	}
	if begin >= len(samples) {
		return 0, covered
	}
	total, high := 0.0, samples[begin].Utilization
	for i := begin + 1; i < len(samples); i++ {
		u := samples[i].Utilization
		switch {
		case u < high-resetDrop:
			total += u
			high = u
		case u > high:
			total += u - high
			high = u
		}
	}
	return total, covered
}
