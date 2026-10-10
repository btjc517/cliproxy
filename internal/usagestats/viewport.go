package usagestats

import "time"

// UsageBetween returns whole stored buckets intersecting a visible time window.
// Recent windows have hourly detail. Older or wider windows use local days,
// including their real 23/25-hour lengths across daylight-saving changes.
// Counters are never prorated: a fraction of an hour cannot tell us which
// requests fell inside it. Starts and Ends expose that resolution to the UI.
func (s *Store) UsageBetween(from, to time.Time) UsageRange {
	return s.usageBetween(from, to, time.Time{}, time.Time{}, false)
}

// UsageBetweenPadded is UsageBetween for the window from..to with buckets
// also covering padFrom..padTo around it, so a dashboard can pan and zoom a
// little without asking again. The bucket length is the one the window
// itself gets, so the padding never makes the drawing coarser. The padding
// reaches at most one window length past each end and, for hourly buckets,
// no further back than the hours still kept. ViewStart and ViewEnd echo the
// window, which tells the caller the padding was understood.
func (s *Store) UsageBetweenPadded(from, to, padFrom, padTo time.Time) UsageRange {
	return s.usageBetween(from, to, padFrom, padTo, true)
}

// clampPadding keeps a padding around the window from..to within one window
// length past each end. A zero end means no padding on that side.
func clampPadding(from, to, padFrom, padTo time.Time) (time.Time, time.Time) {
	span := to.Sub(from)
	if padFrom.IsZero() || padFrom.After(from) {
		padFrom = from
	}
	if earliest := from.Add(-span); padFrom.Before(earliest) {
		padFrom = earliest
	}
	if padTo.IsZero() || padTo.Before(to) {
		padTo = to
	}
	if latest := to.Add(span); padTo.After(latest) {
		padTo = latest
	}
	return padFrom, padTo
}

func (s *Store) usageBetween(from, to, padFrom, padTo time.Time, padded bool) UsageRange {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFunc()
	loc := now.Location()
	usage := UsageRange{Range: "viewport", Accounts: make(map[string][]Counters)}
	if !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		return usage
	}
	cutoff := hourlyCutoff(now)
	daily := from.Before(cutoff) || to.Sub(from) > 7*24*time.Hour
	if padded {
		viewStart, viewEnd := from, to
		usage.ViewStart, usage.ViewEnd = &viewStart, &viewEnd
		padFrom, padTo = clampPadding(from, to, padFrom, padTo)
		if !daily && padFrom.Before(cutoff) {
			padFrom = cutoff
		}
		from, to = padFrom, padTo
	}
	from = from.In(loc)
	step := time.Hour
	start := from.Truncate(time.Hour)
	if daily {
		step = 24 * time.Hour
		start = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	}
	bounds := []time.Time{start}
	for at := start; at.Before(to); {
		if daily {
			at = at.AddDate(0, 0, 1)
		} else {
			at = at.Add(step)
		}
		bounds = append(bounds, at)
	}
	usage.BucketSeconds = int64(step / time.Second)
	usage.Starts = bounds[:len(bounds)-1]
	usage.Ends = bounds[1:]
	add := func(id string, at time.Time, c *Counters) {
		i, ok := boundsIndex(bounds, at)
		if !ok || c == nil {
			return
		}
		if usage.Accounts[id] == nil {
			usage.Accounts[id] = make([]Counters, len(usage.Starts))
		}
		usage.Accounts[id][i].add(*c)
	}
	if daily {
		for id, days := range s.accountDaily {
			for date, c := range days {
				if at, ok := dateNoon(date, loc); ok {
					add(id, at, c)
				}
			}
		}
	} else {
		for id, hours := range s.hourly {
			for unix, c := range hours {
				add(id, time.Unix(unix, 0), c)
			}
		}
	}
	return usage
}
