package usagestats

import "time"

// UsageBetween returns whole stored buckets intersecting a visible time window.
// Recent windows have hourly detail. Older or wider windows use local days,
// including their real 23/25-hour lengths across daylight-saving changes.
// Counters are never prorated: a fraction of an hour cannot tell us which
// requests fell inside it. Starts and Ends expose that resolution to the UI.
func (s *Store) UsageBetween(from, to time.Time) UsageRange {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFunc()
	loc := now.Location()
	usage := UsageRange{Range: "viewport", Accounts: make(map[string][]Counters)}
	if !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		return usage
	}
	daily := from.Before(hourlyCutoff(now)) || to.Sub(from) > 7*24*time.Hour
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
