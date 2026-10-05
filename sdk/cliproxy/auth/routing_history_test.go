package auth

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func setHistory(state *RoutingState, authID, name string, samples []MeterSample) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.accounts[authID].History[name] = samples
}

// at returns the series value at time t, in thousandths, or -1 when unknown.
func at(series MeterSeries, t time.Time) int {
	i := int(t.Sub(series.Start) / (time.Duration(series.StepSeconds) * time.Second))
	if i < 0 || i >= len(series.Used) || series.Used[i] == nil {
		return -1
	}
	return *series.Used[i]
}

func TestMeterHistoryResamplesTheWeekAndDropsAtTheWeeklyReset(t *testing.T) {
	now := routerTestNow // Sun 4 Oct 16:00 UTC
	state := newTestRoutingState(now)
	weekly := now.Add(-7 * time.Hour) // reset at 09:00 today
	setMeter(state, "a", "claude", Meter{Name: "7d", Utilization: 0.12, ResetAt: weekly.Add(7 * 24 * time.Hour)})
	setMeter(state, "a", "claude", Meter{Name: "5h", Utilization: 0.4, ResetAt: now.Add(2 * time.Hour)})
	setMeter(state, "a", "claude", Meter{Name: "7d_opus", Utilization: 0.3})
	setHistory(state, "a", "7d", []MeterSample{
		{At: now.Add(-9 * 24 * time.Hour), Utilization: 0.5}, // older than the grid and a reset
		{At: now.Add(-3 * 24 * time.Hour), Utilization: 0.80},
		{At: now.Add(-10 * time.Hour), Utilization: 0.95}, // before the 09:00 reset
		{At: now.Add(-3 * time.Hour), Utilization: 0.02},
		{At: now.Add(-30 * time.Minute), Utilization: 0.12},
	})
	setHistory(state, "a", "5h", []MeterSample{
		{At: now.Add(-20 * time.Hour), Utilization: 0.7},
		{At: now.Add(-60 * time.Minute), Utilization: 0.2},
		{At: now.Add(-1 * time.Minute), Utilization: 0.4},
	})
	setHistory(state, "a", "7d_opus", []MeterSample{{At: now, Utilization: 0.3}})

	got := state.MeterHistory([]string{"a", "missing"}, now)
	if len(got) != 1 || len(got["a"]) != 2 {
		t.Fatalf("history = %+v, want the 5h and 7d meters of a only", got)
	}
	short, week := got["a"][0], got["a"][1]
	if short.Name != "5h" || short.Long || week.Name != "7d" || !week.Long {
		t.Fatalf("order = %s, %s; want 5h then 7d", short.Name, week.Name)
	}

	if week.StepSeconds != 1800 || len(week.Used) != 7*48+1 {
		t.Fatalf("week grid = %ds x %d", week.StepSeconds, len(week.Used))
	}
	if !week.Start.Equal(now.Add(-7 * 24 * time.Hour)) {
		t.Fatalf("week starts %s", week.Start)
	}
	checks := []struct {
		when time.Time
		want int
	}{
		{now.Add(-6 * 24 * time.Hour), 0},        // the weekly reset 7d7h ago came after that reading
		{now.Add(-2 * 24 * time.Hour), 800},      // carried between readings
		{now.Add(-8 * time.Hour), 950},           // last reading before the reset
		{now.Add(-6*time.Hour - time.Minute), 0}, // after 09:00, before the next reading
		{now.Add(-2 * time.Hour), 20},
		{now, 120},
	}
	for _, c := range checks {
		if v := at(week, c.when); v != c.want {
			t.Errorf("week at %s = %d, want %d", c.when.Format("Mon 15:04"), v, c.want)
		}
	}

	if short.StepSeconds != 600 || len(short.Used) != 24*6+1 {
		t.Fatalf("5h grid = %ds x %d", short.StepSeconds, len(short.Used))
	}
	// A 5-hour window has reset at the latest five hours after a reading.
	if v := at(short, now.Add(-17*time.Hour)); v != 700 {
		t.Errorf("5h three hours after a reading = %d, want 700", v)
	}
	if v := at(short, now.Add(-14*time.Hour)); v != 0 {
		t.Errorf("5h six hours after a reading = %d, want 0", v)
	}
	if v := at(short, now); v != 400 {
		t.Errorf("5h now = %d, want 400", v)
	}
	if short.BurnPerHour == nil || math.Abs(*short.BurnPerHour-0.2034) > 0.001 {
		t.Errorf("5h burn per hour = %v, want about 0.2", short.BurnPerHour)
	}

	// 0.80 to 0.95 before the reset is 0.15 (only the part after 16:00
	// yesterday counts: the 0.80 reading is the baseline), then 0.02 after
	// it and 0.10 more: 0.27 in the last 24 hours.
	if math.Abs(week.Burned-0.27) > 1e-9 || !week.BurnedSince.Equal(now.Add(-24*time.Hour)) {
		t.Errorf("week burned %v since %s, want 0.27 since a day ago", week.Burned, week.BurnedSince)
	}
	if !week.FirstAt.Equal(now.Add(-9 * 24 * time.Hour)) {
		t.Errorf("first reading %s", week.FirstAt)
	}
	if _, errJSON := json.Marshal(got); errJSON != nil {
		t.Fatalf("marshal: %v", errJSON)
	}
}

func TestBurnedIgnoresRepliesThatFinishOutOfOrder(t *testing.T) {
	now := routerTestNow
	samples := []MeterSample{
		{At: now.Add(-4 * time.Hour), Utilization: 0.14},
		{At: now.Add(-3 * time.Hour), Utilization: 0.15},
		{At: now.Add(-3*time.Hour + time.Second), Utilization: 0.14}, // an older reply finishing late
		{At: now.Add(-3*time.Hour + 2*time.Second), Utilization: 0.15},
		{At: now.Add(-2 * time.Hour), Utilization: 0.16},
		{At: now.Add(-1 * time.Hour), Utilization: 0.01}, // a real reset
		{At: now, Utilization: 0.03},
	}
	burned, since := burnedSince(Meter{Name: "7d"}, samples, now.Add(-24*time.Hour))
	// 0.14 to 0.16 is 0.02, then 0.01 and 0.02 after the reset.
	if math.Abs(burned-0.05) > 1e-9 || !since.Equal(now.Add(-4*time.Hour)) {
		t.Fatalf("burned %v since %s, want 0.05 since the first reading", burned, since)
	}
}

func TestBurnedCountsAKnownResetAndAReadingAtTheStart(t *testing.T) {
	now := routerTestNow
	weekly := Meter{Name: "7d", Window: 7 * 24 * time.Hour, ResetAt: now.Add(-2*time.Hour + 7*24*time.Hour)}
	// 10% before the 14:00 reset, 30% at the next reading: 30 points used since the reset.
	samples := []MeterSample{
		{At: now.Add(-3 * time.Hour), Utilization: 0.10},
		{At: now.Add(-1 * time.Hour), Utilization: 0.30},
	}
	if burned, _ := burnedSince(weekly, samples, now.Add(-24*time.Hour)); math.Abs(burned-0.30) > 1e-9 {
		t.Errorf("across a known reset burned %v, want 0.30", burned)
	}
	// A reading exactly at the start of the span is the baseline, not the one before it.
	samples = []MeterSample{
		{At: now.Add(-25 * time.Hour), Utilization: 0.10},
		{At: now.Add(-24 * time.Hour), Utilization: 0.80},
		{At: now, Utilization: 0.90},
	}
	burned, since := burnedSince(Meter{Name: "7d"}, samples, now.Add(-24*time.Hour))
	if math.Abs(burned-0.10) > 1e-9 || !since.Equal(now.Add(-24*time.Hour)) {
		t.Errorf("burned %v since %s, want 0.10 since a day ago", burned, since)
	}
}

func TestMeterHistoryBeforeTheFirstReadingIsUnknown(t *testing.T) {
	now := routerTestNow
	state := newTestRoutingState(now)
	setMeter(state, "a", "claude", Meter{Name: "7d", Utilization: 0.1, ResetAt: now.Add(48 * time.Hour)})
	setHistory(state, "a", "7d", []MeterSample{
		{At: now.Add(-5 * time.Hour), Utilization: 0.05},
		{At: now.Add(-1 * time.Hour), Utilization: 0.1},
	})
	week := state.MeterHistory([]string{"a"}, now)["a"][0]
	if v := at(week, now.Add(-6*time.Hour)); v != -1 {
		t.Errorf("before the first reading = %d, want unknown", v)
	}
	if v := at(week, now.Add(-4*time.Hour)); v != 50 {
		t.Errorf("after the first reading = %d, want 50", v)
	}
	// History shorter than a day reports what it covers.
	if math.Abs(week.Burned-0.05) > 1e-9 || !week.BurnedSince.Equal(now.Add(-5*time.Hour)) {
		t.Errorf("burned %v since %s, want 0.05 since the first reading", week.Burned, week.BurnedSince)
	}
	// A weekly meter moves in whole points, so its rate looks back six hours:
	// an hour without a new point is not idle. 5 points in 4 hours.
	if week.BurnPerHour == nil || math.Abs(*week.BurnPerHour-0.0125) > 1e-9 {
		t.Errorf("burn per hour = %v, want 0.0125 over the six-hour window", week.BurnPerHour)
	}
}
