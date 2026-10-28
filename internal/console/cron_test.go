package console

import "testing"

func TestDescribeCron(t *testing.T) {
	for _, c := range []struct {
		expr      string
		recurring bool
		want      string
	}{
		{"7,37 6-18 * * 1-5", true, "twice an hour at :07 and :37, 06:00–18:59, weekdays"},
		{"*/15 * * * 1-5", true, "every 15 minutes, weekdays"},
		{"*/1 * * * *", true, "every minute"},
		{"* * * * *", true, "every minute"},
		{"0 9 * * 1-5", true, "weekdays at 09:00"},
		{"30 8,17 * * *", true, "daily at 08:30 and 17:30"},
		{"0 * * * *", true, "hourly at :00"},
		{"5 */2 * * *", true, "every 2 hours at :05"},
		{"0,20,40 9-17 * * *", true, "3 times an hour at :00, :20 and :40, 09:00–17:59"},
		{"*/10 9-17 * * 1-5", true, "every 10 minutes, 09:00–17:59, weekdays"},
		{"0 10 * * 1", true, "on Mondays at 10:00"},
		{"0 10 * * 1,3,5", true, "on Mon, Wed and Fri at 10:00"},
		{"0 10 * * 2-4", true, "Tue–Thu at 10:00"},
		{"0 10 * * 0,6", true, "weekends at 10:00"},
		{"0 6 1 * *", true, "on the 1st of each month at 06:00"},
		{"30 14 28 10 *", false, "once, on 28 Oct at 14:30"},
		{"@daily", true, "daily at 00:00"},
	} {
		got, ok := DescribeCron(c.expr, c.recurring)
		if !ok || got != c.want {
			t.Errorf("%q: got %q (%v), want %q", c.expr, got, ok, c.want)
		}
	}
	for _, raw := range []string{"", "0 9 * *", "0 9 1 * 1", "0-30 * * * *", "0 9 L * *", "0 9 * * MON", "*/5 */2 * * *", "61 * * * *"} {
		if got, ok := DescribeCron(raw, true); ok {
			t.Errorf("%q should be shown raw, got %q", raw, got)
		}
	}
}
