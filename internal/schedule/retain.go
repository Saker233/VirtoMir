package schedule

import (
	"fmt"
	"slices"
	"time"
)

// Policy is what a schedule keeps of its automatic snapshots or backups: the newest Last,
// plus the newest one of each of the last Daily days, Weekly (ISO) weeks, Monthly months and
// Yearly years that have one (grandfather-father-son). These are restic's --keep-* rules, so
// local and off-site copies age the same way.
type Policy struct {
	Last, Daily, Weekly, Monthly, Yearly int
}

// Policy is the schedule's retention.
func (sc Schedule) Policy() Policy {
	return Policy{Last: sc.Keep, Daily: sc.KeepDaily, Weekly: sc.KeepWeekly, Monthly: sc.KeepMonthly, Yearly: sc.KeepYearly}
}

// Keep reports for each of times (in any order) whether the policy keeps it. Buckets use
// local time, like the schedules themselves.
func (p Policy) Keep(times []time.Time) []bool {
	keep := make([]bool, len(times))
	order := make([]int, len(times))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return times[b].Compare(times[a]) }) // newest first
	for n, i := range order {
		if n < p.Last {
			keep[i] = true
		}
	}
	buckets := []struct {
		n   int
		key func(time.Time) string
	}{
		{p.Daily, func(t time.Time) string { return t.Format("2006-01-02") }},
		{p.Weekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-W%02d", y, w) }},
		{p.Monthly, func(t time.Time) string { return t.Format("2006-01") }},
		{p.Yearly, func(t time.Time) string { return t.Format("2006") }},
	}
	for _, b := range buckets {
		last, kept := "", 0
		for _, i := range order {
			if kept >= b.n {
				break
			}
			if k := b.key(times[i].Local()); k != last {
				keep[i], last, kept = true, k, kept+1
			}
		}
	}
	return keep
}
