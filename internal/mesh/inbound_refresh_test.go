package mesh

import (
	"strings"
	"testing"
)

// A hand-customised Inbound bullet keeps its own words; only the claim-the-oldest
// command becomes the queue-and-wait order.
func TestRefreshedCommsSection_CustomisedBulletSwapsOnlyTheClaimCommand(t *testing.T) {
	custom := "# Role\n\n## Agent Communication\n" +
		"- **Inbound:** the user targets you at a tracker issue directly, or drops a request in `intake/`. " +
		"At session start / on \"check intake\", run `meshctl inbox next --claim` (atomic claim) or scan `intake/*.md` for `status: pending`.\n" +
		"- **Read-back:** keep me.\n"

	fresh, stale := refreshedCommsSection(custom)
	if !stale {
		t.Fatal("a customised bullet that still claims the oldest request is stale")
	}
	if strings.Contains(fresh, "inbox next --claim") {
		t.Fatalf("claim-the-oldest command survived:\n%s", fresh)
	}
	for _, keep := range []string{"the user targets you at a tracker issue directly", "- **Read-back:** keep me.", "# Role"} {
		if !strings.Contains(fresh, keep) {
			t.Fatalf("human text %q lost:\n%s", keep, fresh)
		}
	}
	if !strings.Contains(fresh, "`meshctl inbox list`") || !strings.Contains(fresh, "wait for the human") {
		t.Fatalf("queue-and-wait order missing:\n%s", fresh)
	}
	if again, stale := refreshedCommsSection(fresh); stale || again != fresh {
		t.Fatal("refresh is not idempotent")
	}
}

// Only the Inbound bullet is touched: a mention elsewhere (a playbook quote) stays.
func TestRefreshedCommsSection_LeavesMentionsOutsideInboundBullet(t *testing.T) {
	body := "Notes: once we ran `meshctl inbox next --claim` by hand.\n"
	if _, stale := refreshedCommsSection(body); stale {
		t.Fatal("text outside an Inbound bullet must not be rewritten")
	}
}
