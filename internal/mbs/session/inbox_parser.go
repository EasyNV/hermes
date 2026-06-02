package session

import (
	"strings"
	"time"

	"mbs-native/client"
	"mbs-native/fb"
)

// deriveThreadID picks the stable conversation key for an extracted
// message. OTID (optimistic threading id) is only present on OUTBOUND
// echoes — the client generated it when WE sent. A genuine customer
// INBOUND message has no OTID, so we fall back to the sender's profile
// id parsed from SenderURL (fb://profile/<id>). For a 1:1 MBS↔WhatsApp
// thread the customer profile id IS the thread identity, and the inbox
// keys conversations on (workspace, uid, thread_id) — so a non-empty
// value here is what makes an inbound reply land at all.
//
// Returns "" only when neither source is available (non-message delta);
// callers/consumer treat empty thread_id as un-keyable and drop.
func deriveThreadID(m fb.Message) string {
	if m.OTID != "" {
		return m.OTID
	}
	return profileIDFromURL(m.SenderURL)
}

// profileIDFromURL extracts <id> from "fb://profile/<id>[?query]".
// Returns "" if the prefix is absent.
func profileIDFromURL(u string) string {
	const pfx = "fb://profile/"
	if !strings.HasPrefix(u, pfx) {
		return ""
	}
	id := u[len(pfx):]
	if i := strings.IndexAny(id, "?#/"); i >= 0 {
		id = id[:i]
	}
	return id
}

// parseInboxItem handles server-pushed client.InboxItem deltas. It
// intentionally emits NO message InboundDeltas.
//
// History: chunk 3 extracted messages here via fb.ExtractMessages on the
// delta-push format. That extractor cannot reliably key INBOUND messages:
// it reads thread_id from m.OTID (the sender's OUTBOUND optimistic id,
// empty on genuine customer inbound) and, lacking the threads block, mis-
// parses message bodies. On real customer replies this produced junk —
// conversations keyed on per-message snowflakes (mbs:thread:<snowflake>),
// corrupted bodies, and an "un-keyable, dropping" flood — AND, because the
// messages table has a GLOBAL unique index on mbs_mid, the junk push-path
// row won the insert race and BLOCKED the correct poll-path row for the
// same MID (ON CONFLICT DO NOTHING).
//
// The authoritative inbound source is parseSnapshotPoll (SnapshotPoll
// "130"), which recovers BOTH senderFBID AND the threads block and JOINs
// them to the real customer_id. It captures every inbound message and is
// idempotent on mbs_mid. The push path is therefore pure pollution for
// messages and is disabled. Receipts/typing/presence carried no message
// body and were never surfaced anyway, so nothing of value is lost.
//
// The function is retained (rather than deleted) so the listener call site
// stays stable and the rationale lives next to the code; if a non-message
// use for the push channel appears (delivery receipts, typing), it can be
// reintroduced here WITHOUT re-enabling message-delta emission.
func parseInboxItem(item *client.InboxItem, uid int64) []*InboundDelta {
	return nil
}

// parseSnapshotPoll extracts message deltas from a /ls_resp envelope
// returned by client.SnapshotPoll("130"). Same extraction logic as
// parseInboxItem but with a nil Raw (the envelope is large; we don't
// want each subscriber to retain a reference).
//
// parseSnapshotPoll extracts INBOUND message deltas from a /ls_resp
// envelope returned by client.SnapshotPoll("130").
//
// Unlike parseInboxItem (which handles the delta-push format via
// fb.ExtractMessages), the snapshot payload is a SQLite-replication stream
// where message records carry only the author's messaging FBID — not the
// conversation's customer_id. We use fb.ParseSnapshot to recover BOTH the
// per-message senderFBID AND the threads block (customer_id + participant
// FBIDs), then JOIN: senderFBID → owning thread's customer_id == thread_id.
//
// Direction: the admin/self FBID is the participant common to every thread
// block. Messages authored by self are our OWN outbound echoes and are
// DROPPED here (they must not be re-ingested as inbound; the outbound
// consumer owns them). Only customer-authored messages are emitted.
//
// thread_id is the inbox's conversation key, and it equals the customer_id
// the send path already resolved (BizInboxWhatsAppCustomerMutation) and
// stored in mbs_phone_threads — so inbound UNIFIES with outbound into one
// conversation.
//
// Deduplication remains the handler's job (idempotent CreateMbsMessage ON
// CONFLICT mbs_mid), so the snapshot replaying old history is harmless.
func parseSnapshotPoll(resp *fb.LsResp, uid int64) []*InboundDelta {
	if resp == nil || len(resp.Payload) == 0 {
		return nil
	}
	ps := fb.ParseSnapshot(resp.Payload)
	now := time.Now()
	out := make([]*InboundDelta, 0, len(ps.Messages))
	for _, m := range ps.Messages {
		// Skip non-message records (receipts/presence with no body).
		if m.MID == "" || m.Body == "" {
			continue
		}
		// Drop our own outbound echoes. When SelfFBID is known (≥2 thread
		// blocks → unambiguous intersection), a message authored by self is
		// outbound and must not be published as inbound.
		if ps.SelfFBID != 0 && m.SenderFBID == ps.SelfFBID {
			continue
		}
		// Join: resolve the author's FBID to its thread's customer_id.
		threadID := ps.ThreadIDForSender(m.SenderFBID)
		// Single-thread fallback: SelfFBID couldn't be derived (only one
		// thread block). We can't positively distinguish self from customer
		// by intersection. Assign the lone thread's customer_id so the
		// message still lands; the handler's mbs_mid idempotency + the
		// outbound consumer owning outbound rows guard against a self-echo
		// being double-counted. (Documented gap G-A/single-thread.)
		if threadID == "" && ps.SelfFBID == 0 && len(ps.Threads) == 1 {
			threadID = ps.Threads[0].CustomerID
		}
		out = append(out, &InboundDelta{
			UID:        uid,
			ThreadID:   threadID,
			MID:        m.MID,
			SenderFBID: m.SenderFBID,
			Text:       m.Body,
			ReceivedAt: now,
			Raw:        nil, // intentionally nil for poll batches
		})
	}
	return out
}
