package session

import (
	"time"

	"mbs-native/client"
	"mbs-native/fb"
)

// parseInboxItem converts one server-pushed client.InboxItem into zero
// or more InboundDelta records. The Inbox channel carries:
//
//   - Non-/ls_resp PUBLISHes (raw Frame only). Chunk 3 ignores these —
//     they're typically analytics/presence that handler doesn't surface.
//   - /ls_resp PUBLISHes on topic 179 (LsResp + RawPayload set).
//     Unsolicited deltas: new messages, receipts, typing.
//
// We delegate the actual byte-level extraction to fb.ExtractMessages,
// which already exists in mbs-native/fb/lspayload.go.
//
// Empty result is valid — receipt/typing/presence deltas produce
// extractable messages with empty Body, which callers can filter on.
func parseInboxItem(item *client.InboxItem, uid int64) []*InboundDelta {
	if item == nil {
		return nil
	}
	if item.LsResp == nil || len(item.RawPayload) == 0 {
		// Non-/ls_resp publish (analytics, presence binary). Skip —
		// chunk 3 doesn't surface these. Frame-only items can be
		// reintroduced in chunk 5 if a use case appears.
		return nil
	}
	msgs := fb.ExtractMessages(item.RawPayload)
	now := time.Now()
	out := make([]*InboundDelta, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, &InboundDelta{
			UID:        uid,
			ThreadID:   m.OTID,
			MID:        m.MID,
			SenderName: m.Sender,
			SenderURL:  m.SenderURL,
			Text:       m.Body,
			Kind:       m.Kind,
			ReceivedAt: now,
			Raw:        item,
		})
	}
	return out
}

// parseSnapshotPoll extracts message deltas from a /ls_resp envelope
// returned by client.SnapshotPoll("130"). Same extraction logic as
// parseInboxItem but with a nil Raw (the envelope is large; we don't
// want each subscriber to retain a reference).
//
// Deduplication: callers (handler) can dedupe by MID. Chunk 3 doesn't
// track last-seen — every poll batch produces fresh records and the
// handler is responsible for "have I emitted this MID already" filtering.
// Reason: dedup state is an interesting concern (per-pod-restart? per-uid?
// since when?) and belongs at the handler layer where it can be sized
// against the inbox UI's window.
func parseSnapshotPoll(resp *fb.LsResp, uid int64) []*InboundDelta {
	if resp == nil || len(resp.Payload) == 0 {
		return nil
	}
	msgs := fb.ExtractMessages(resp.Payload)
	now := time.Now()
	out := make([]*InboundDelta, 0, len(msgs))
	for _, m := range msgs {
		// Filter out empty-everything records (artifacts of the
		// best-effort extractor on non-message deltas).
		if m.MID == "" && m.Body == "" && m.OTID == "" {
			continue
		}
		out = append(out, &InboundDelta{
			UID:        uid,
			ThreadID:   m.OTID,
			MID:        m.MID,
			SenderName: m.Sender,
			SenderURL:  m.SenderURL,
			Text:       m.Body,
			Kind:       m.Kind,
			ReceivedAt: now,
			Raw:        nil, // intentionally nil for poll batches
		})
	}
	return out
}
