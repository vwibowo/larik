// Package schedule runs headless prompts on a cron schedule: entries are
// kept in the config directory, and a daemon (larik schedule run) starts
// each as a larik -p process when it falls due.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field cron expression: minute, hour, day of month,
// month, day of week (0 or 7 is Sunday), in local time.
type Cron struct {
	min, hour, dom, month, dow uint64 // bit i set: value i matches
	// domAny and dowAny record a "*" day field: when both day fields are
	// restricted, cron matches either, not both.
	domAny, dowAny bool
}

var shortcuts = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

// ParseCron reads a cron expression such as "*/15 9-17 * * 1-5" or
// "@daily". Each field takes *, numbers, ranges (a-b), steps (*/n, a-b/n)
// and comma-separated lists of those.
func ParseCron(expr string) (Cron, error) {
	expr = strings.TrimSpace(expr)
	if s, ok := shortcuts[strings.ToLower(expr)]; ok {
		expr = s
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return Cron{}, fmt.Errorf("cron %q: want 5 fields (minute hour day month weekday), got %d", expr, len(f))
	}
	var c Cron
	var err error
	bounds := []struct {
		dst      *uint64
		name     string
		min, max int
	}{{&c.min, "minute", 0, 59}, {&c.hour, "hour", 0, 23}, {&c.dom, "day", 1, 31}, {&c.month, "month", 1, 12}, {&c.dow, "weekday", 0, 7}}
	for i, b := range bounds {
		if *b.dst, err = parseField(f[i], b.min, b.max); err != nil {
			return Cron{}, fmt.Errorf("cron %q: %s: %w", expr, b.name, err)
		}
	}
	if c.dow&(1<<7) != 0 {
		c.dow |= 1 // 7 is Sunday too
	}
	c.domAny, c.dowAny = f[2] == "*", f[4] == "*"
	return c, nil
}

func parseField(s string, lo, hi int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(s, ",") {
		rng, step := part, 1
		if i := strings.IndexByte(part, '/'); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n < 1 {
				return 0, fmt.Errorf("bad step in %q", part)
			}
			rng, step = part[:i], n
		}
		from, to := lo, hi
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if from, err = strconv.Atoi(a); err != nil {
				return 0, fmt.Errorf("bad value %q", part)
			}
			to = from
			if isRange {
				if to, err = strconv.Atoi(b); err != nil {
					return 0, fmt.Errorf("bad range %q", part)
				}
			} else if step > 1 {
				to = hi // "5/15": from 5, every 15
			}
		}
		if from < lo || to > hi || from > to {
			return 0, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := from; v <= to; v += step {
			bits |= 1 << v
		}
	}
	return bits, nil
}

func (c Cron) matchDay(t time.Time) bool {
	dom := c.dom&(1<<t.Day()) != 0
	dow := c.dow&(1<<int(t.Weekday())) != 0
	if c.domAny || c.dowAny {
		return dom && dow
	}
	return dom || dow
}

// Next returns the first minute strictly after t that matches, or the zero
// time if none does within five years (as for "0 0 30 2 *").
func (c Cron) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	end := t.AddDate(5, 0, 0)
	for t.Before(end) {
		switch {
		case c.month&(1<<int(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
		case !c.matchDay(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
		case c.hour&(1<<t.Hour()) == 0:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
		case c.min&(1<<t.Minute()) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
