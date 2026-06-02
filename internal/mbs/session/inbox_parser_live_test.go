package session

import (
	"os"
	"path/filepath"
	"testing"

	"mbs-native/fb"
)

// TestParseSnapshotPoll_LiveFixture drives the real captured db130 payload
// through parseSnapshotPoll and asserts the unification behavior:
//   - customer (Samuel) messages are emitted with the real thread_id
//     (customer_id 1127921160404565), so they unify with outbound.
//   - admin/self messages ("test", "Halo, apa kabar?") are dropped as
//     outbound echoes (not re-ingested as inbound).
func TestParseSnapshotPoll_LiveFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "re", "mbs", "mbs-native", "testdata", "live_poll130.bin")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("live fixture unavailable: %v", err)
	}

	deltas := parseSnapshotPoll(&fb.LsResp{Payload: raw}, 61590752691262)

	const customerThread = "1127921160404565"
	const customerFBID uint64 = 2255842320809130
	const adminFBID uint64 = 2253276134399082

	if len(deltas) == 0 {
		t.Fatal("expected at least one inbound delta from the customer")
	}

	gotBodies := map[string]bool{}
	for _, d := range deltas {
		// Every emitted delta must be a customer message (not our echo).
		if d.SenderFBID == adminFBID {
			t.Errorf("admin echo leaked as inbound: mid=%s body=%q", d.MID, d.Text)
		}
		if d.SenderFBID != customerFBID {
			t.Errorf("unexpected sender %d for body %q", d.SenderFBID, d.Text)
		}
		// And it must carry the real thread_id so it unifies with outbound.
		if d.ThreadID != customerThread {
			t.Errorf("delta %q thread_id=%q, want %q", d.Text, d.ThreadID, customerThread)
		}
		gotBodies[d.Text] = true
	}

	// Customer's two messages should be present.
	for _, want := range []string{"Halo gan", "Baik gan"} {
		if !gotBodies[want] {
			t.Errorf("expected customer message %q in deltas", want)
		}
	}
	// Admin messages must NOT be present.
	for _, notWant := range []string{"test", "Halo, apa kabar?"} {
		if gotBodies[notWant] {
			t.Errorf("admin message %q must not be emitted as inbound", notWant)
		}
	}
}
