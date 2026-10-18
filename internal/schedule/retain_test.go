package schedule

import (
	"testing"
	"time"
)

func TestPolicyKeep(t *testing.T) {
	day := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.Local) }
	// Two a day for the last 10 days of 2026-10, plus one each on the 1st of earlier months
	// and one in 2025.
	var times []time.Time
	for d := 22; d <= 31; d++ {
		times = append(times, day(2026, 10, d, 3), day(2026, 10, d, 15))
	}
	for m := time.March; m <= time.September; m++ {
		times = append(times, day(2026, m, 1, 3))
	}
	times = append(times, day(2025, 6, 1, 3))

	keep := Policy{Last: 2, Daily: 7, Monthly: 6, Yearly: 2}.Keep(times)
	kept := map[time.Time]bool{}
	for i, k := range keep {
		if k {
			kept[times[i]] = true
		}
	}
	for _, want := range []time.Time{
		day(2026, 10, 31, 15), day(2026, 10, 31, 3), // last 2
		day(2026, 10, 25, 15), // 7th day back: the newest of that day
		day(2026, 5, 1, 3),    // 6th month: Oct, Sep, Aug, Jul, Jun, May
		day(2025, 6, 1, 3),    // 2nd year
	} {
		if !kept[want] {
			t.Errorf("%v not kept", want)
		}
	}
	for _, gone := range []time.Time{day(2026, 10, 25, 3), day(2026, 10, 24, 15), day(2026, 4, 1, 3)} {
		if kept[gone] {
			t.Errorf("%v kept", gone)
		}
	}
	// Last 2 + the 6 days before the 31st + months May..Sep (Oct is already kept) + 2025.
	if len(kept) != 2+6+5+1 {
		t.Errorf("kept %d", len(kept))
	}
}

func TestExpiredSnapshotsFollowThePolicy(t *testing.T) {
	s := &Scheduler{}
	names := []string{"auto-20261001-030000", "auto-20261002-030000", "auto-20261002-150000", "auto-manual-name"}
	got := s.expired(Policy{Last: 1, Daily: 2}, names)
	if len(got) != 1 || got[0] != "auto-20261002-030000" {
		t.Errorf("expired = %v", got)
	}
}
