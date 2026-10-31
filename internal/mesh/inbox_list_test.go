package mesh

import (
	"strings"
	"testing"
	"time"
)

// inbox_list_test.go — `meshctl inbox list` is the clean queue:
// pending + in-progress only by default, ordered priority then age, one line
// per request with a one-line ask; --all shows every status.

func inboxListPool(t *testing.T) (*Agent, map[string]*Request, time.Time) {
	t.Helper()
	dir := meshRoot(t)
	a := newTestAgent(t, dir, "owner", true, nil)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.Local)
	reqs := map[string]*Request{}
	mk := func(key, pri, ask string, daysAgo int, status string) {
		r, err := WriteRequest(a, "guidance", ask, "requester", pri, nil, now.AddDate(0, 0, -daysAgo))
		if err != nil {
			t.Fatal(err)
		}
		if status != StatusPending {
			if _, err := RespondRequest(a, r.ID, RespondOptions{Status: status, Response: "done", Now: now}); err != nil {
				t.Fatal(err)
			}
		}
		reqs[key] = r
	}
	mk("oldP2", "P2", "Old leftover from last week", 4, StatusPending)
	mk("newP1", "P1", "Urgent fix needed today", 0, StatusPending)
	mk("newP2", "P2", "Fresh question", 1, StatusPending)
	mk("busy", "P3", "Long running review", 2, StatusInProgress)
	mk("answered", "P1", "Already answered thing", 3, StatusAnswered)
	mk("filed", "P2", "Filed thing", 5, StatusFiled)
	return a, reqs, now
}

func queueIDs(q []*Request) []string {
	var ids []string
	for _, r := range q {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestInboxQueue_DefaultIsOpenRequestsByPriorityThenAge(t *testing.T) {
	a, r, _ := inboxListPool(t)
	q, err := InboxQueue(a, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{r["newP1"].ID, r["oldP2"].ID, r["newP2"].ID, r["busy"].ID}
	if got := queueIDs(q); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("queue order:\n got %v\nwant %v", got, want)
	}
}

func TestInboxQueue_AllShowsEveryStatusInTheSameOrder(t *testing.T) {
	a, r, _ := inboxListPool(t)
	q, err := InboxQueue(a, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{r["answered"].ID, r["newP1"].ID, r["filed"].ID, r["oldP2"].ID, r["newP2"].ID, r["busy"].ID}
	if got := queueIDs(q); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("--all order:\n got %v\nwant %v", got, want)
	}
}

func TestInboxQueueMarkdown_NumberedLinesWithOneLineAsk(t *testing.T) {
	a, r, now := inboxListPool(t)
	q, _ := InboxQueue(a, false)
	out := InboxQueueMarkdown(q, now)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want 4 lines, got:\n%s", out)
	}
	first := lines[0]
	for _, want := range []string{"1. ", r["newP1"].ID, "from requester", "guidance", "P1", "0d", "Urgent fix needed today"} {
		if !strings.Contains(first, want) {
			t.Errorf("first line missing %q: %s", want, first)
		}
	}
	if !strings.Contains(lines[1], "4d") || !strings.HasPrefix(lines[1], "2. ") {
		t.Errorf("second line wants oldest P2 at 4d: %s", lines[1])
	}
	if !strings.Contains(lines[3], StatusInProgress) {
		t.Errorf("in-progress request must say so: %s", lines[3])
	}
	if strings.Contains(lines[0], StatusPending) {
		t.Errorf("pending is the default; the line need not repeat it: %s", lines[0])
	}
	if got := InboxQueueMarkdown(nil, now); !strings.Contains(got, "no open requests") {
		t.Errorf("empty queue: %q", got)
	}
}
