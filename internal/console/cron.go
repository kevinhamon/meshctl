package console

import (
	"fmt"
	"strconv"
	"strings"
)

// cron.go — scheduled jobs in plain words. The
// harness reports a job's schedule as a five-field cron expression in local
// time; DescribeCron turns the common shapes into a phrase and gives up (ok
// false) on anything else, which the page then shows raw.

// cronField is one parsed field: any ("*"), a step ("*/n"), or values.
type cronField struct {
	any    bool
	step   int
	values []int // sorted as written; ranges expanded
	ranges [][2]int
}

func parseCronField(s string, lo, hi int) (cronField, bool) {
	if s == "*" {
		return cronField{any: true}, true
	}
	if n, ok := strings.CutPrefix(s, "*/"); ok {
		v, err := strconv.Atoi(n)
		if err != nil || v < 1 || v > hi {
			return cronField{}, false
		}
		return cronField{step: v}, true
	}
	var f cronField
	for _, part := range strings.Split(s, ",") {
		a, b, isRange := strings.Cut(part, "-")
		x, err := strconv.Atoi(a)
		if err != nil || x < lo || x > hi {
			return cronField{}, false
		}
		y := x
		if isRange {
			if y, err = strconv.Atoi(b); err != nil || y < x || y > hi {
				return cronField{}, false
			}
			f.ranges = append(f.ranges, [2]int{x, y})
		}
		for v := x; v <= y; v++ {
			f.values = append(f.values, v)
		}
	}
	return f, true
}

// single is the field's one value, when it has exactly one.
func (f cronField) single() (int, bool) {
	if len(f.values) == 1 {
		return f.values[0], true
	}
	return 0, false
}

var weekdayNames = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
var weekdayPlural = []string{"Sundays", "Mondays", "Tuesdays", "Wednesdays", "Thursdays", "Fridays", "Saturdays", "Sundays"}
var monthNames = []string{"", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

var cronMacros = map[string]string{"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@midnight": "0 0 * * *",
	"@weekly": "0 0 * * 0", "@monthly": "0 0 1 * *", "@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *"}

// DescribeCron describes a five-field cron expression in plain words, e.g.
// "7,37 6-18 * * 1-5" → "twice an hour at :07 and :37, 06:00–18:59, weekdays".
// A job that runs once is prefixed "once, ".
func DescribeCron(expr string, recurring bool) (string, bool) {
	if m, ok := cronMacros[strings.TrimSpace(expr)]; ok {
		expr = m
	}
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return "", false
	}
	limits := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	var f [5]cronField
	for i, p := range parts {
		var ok bool
		if f[i], ok = parseCronField(p, limits[i][0], limits[i][1]); !ok {
			return "", false
		}
	}
	minute, hour, dom, month, dow := f[0], f[1], f[2], f[3], f[4]
	// Minutes are a step, any, or a few listed values; ranges of minutes are rare.
	if len(minute.ranges) > 0 || len(minute.values) > 6 {
		return "", false
	}

	days, ok := describeDays(dom, month, dow)
	if !ok {
		return "", false
	}
	var out string
	if m, isOne := minute.single(); isOne && len(hour.ranges) == 0 && len(hour.values) > 0 && len(hour.values) <= 6 {
		// Fixed times of day: "weekdays at 09:00", "daily at 09:00 and 17:30".
		times := make([]string, len(hour.values))
		for i, h := range hour.values {
			times[i] = fmt.Sprintf("%02d:%02d", h, m)
		}
		if days == "" {
			days = "daily"
		}
		out = days + " at " + joinAnd(times)
	} else {
		var freq string
		switch {
		case minute.any:
			freq = "every minute"
		case minute.step > 0:
			freq = plural(minute.step, "every minute", "every %d minutes")
		default:
			marks := make([]string, len(minute.values))
			for i, v := range minute.values {
				marks[i] = fmt.Sprintf(":%02d", v)
			}
			switch len(marks) {
			case 1:
				freq = "hourly at " + marks[0]
			case 2:
				freq = "twice an hour at " + joinAnd(marks)
			default:
				freq = fmt.Sprintf("%d times an hour at %s", len(marks), joinAnd(marks))
			}
		}
		segs := []string{freq}
		switch {
		case hour.any:
		case hour.step > 0:
			if minute.step > 0 || minute.any {
				return "", false // every n minutes within every n-th hour: too rare to phrase
			}
			segs[0] = strings.Replace(freq, "hourly", plural(hour.step, "hourly", "every %d hours"), 1)
			if !strings.Contains(freq, "hourly") {
				return "", false
			}
		case len(hour.ranges) == 1 && len(hour.values) == hour.ranges[0][1]-hour.ranges[0][0]+1:
			segs = append(segs, fmt.Sprintf("%02d:00–%02d:59", hour.ranges[0][0], hour.ranges[0][1]))
		default:
			hs := make([]string, len(hour.values))
			for i, h := range hour.values {
				hs[i] = fmt.Sprintf("%02d", h)
			}
			segs = append(segs, "in hours "+joinAnd(hs))
		}
		if days != "" {
			segs = append(segs, days)
		}
		out = strings.Join(segs, ", ")
	}
	if !recurring {
		out = "once, " + out
	}
	return out, true
}

// describeDays is the day part: "" for every day, "weekdays", "on Mondays",
// "on the 1st of each month", "on 28 Oct". Day-of-month and day-of-week
// both restricted (cron runs on either) is not phrased.
func describeDays(dom, month, dow cronField) (string, bool) {
	if !month.any && month.step == 0 {
		if d, ok := dom.single(); ok && dow.any {
			if mo, ok := month.single(); ok {
				return fmt.Sprintf("on %d %s", d, monthNames[mo]), true
			}
		}
		return "", false
	}
	if month.step > 0 {
		return "", false
	}
	switch {
	case dom.any && dow.any:
		return "", true
	case !dom.any && !dow.any, dom.step > 0, dow.step > 0:
		return "", false
	case !dom.any:
		ds := make([]string, len(dom.values))
		for i, d := range dom.values {
			ds[i] = ordinal(d)
		}
		if len(ds) > 4 {
			return "", false
		}
		return "on the " + joinAnd(ds) + " of each month", true
	}
	set := map[int]bool{}
	for _, d := range dow.values {
		set[d%7] = true
	}
	switch {
	case len(set) == 5 && !set[0] && !set[6]:
		return "weekdays", true
	case len(set) == 2 && set[0] && set[6]:
		return "weekends", true
	case len(set) == 7:
		return "", true
	case len(set) == 1:
		for d := range set {
			return "on " + weekdayPlural[d], true
		}
	}
	if len(dow.ranges) == 1 && len(dow.values) == dow.ranges[0][1]-dow.ranges[0][0]+1 {
		return weekdayNames[dow.ranges[0][0]] + "–" + weekdayNames[dow.ranges[0][1]], true
	}
	ns := make([]string, len(dow.values))
	for i, d := range dow.values {
		ns[i] = weekdayNames[d]
	}
	return "on " + joinAnd(ns), true
}

func joinAnd(xs []string) string {
	if len(xs) <= 1 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return fmt.Sprintf(many, n)
}

func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}
