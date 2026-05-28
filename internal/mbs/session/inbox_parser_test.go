package session

import (
	"testing"

	"mbs-native/client"
	"mbs-native/fb"
)

func TestParseInboxItem_NilItem(t *testing.T) {
	got := parseInboxItem(nil, 100)
	if got != nil {
		t.Errorf("nil item should yield nil deltas, got %+v", got)
	}
}

func TestParseInboxItem_NonLsRespItem_Skipped(t *testing.T) {
	// Item without LsResp + RawPayload (frame-only) is intentionally
	// skipped in chunk 3 — chunk 5 may revisit.
	item := &client.InboxItem{}
	got := parseInboxItem(item, 100)
	if got != nil {
		t.Errorf("frame-only item should produce no deltas, got %+v", got)
	}
}

func TestParseInboxItem_LsRespWithEmptyPayload_Empty(t *testing.T) {
	item := &client.InboxItem{LsResp: &fb.LsResp{}, RawPayload: nil}
	got := parseInboxItem(item, 100)
	if got != nil {
		t.Errorf("empty RawPayload should yield nil, got %+v", got)
	}
}

func TestParseSnapshotPoll_NilResp(t *testing.T) {
	got := parseSnapshotPoll(nil, 100)
	if got != nil {
		t.Errorf("nil resp should yield nil, got %+v", got)
	}
}

func TestParseSnapshotPoll_EmptyPayload(t *testing.T) {
	got := parseSnapshotPoll(&fb.LsResp{}, 100)
	if got != nil {
		t.Errorf("empty payload should yield nil, got %+v", got)
	}
}

func TestParseSnapshotPoll_FiltersEmptyRecords(t *testing.T) {
	// fb.ExtractMessages on a non-message payload returns nothing for
	// MID/Body/OTID — chunk 3 filters those out so subscribers don't
	// get empty noise records. Pin by passing payload that contains
	// no `mid.$` markers; we return zero-length deltas.
	resp := &fb.LsResp{Payload: []byte("opaque non-message bytes")}
	got := parseSnapshotPoll(resp, 100)
	if len(got) != 0 {
		t.Errorf("non-message payload should yield zero deltas, got %d: %+v", len(got), got)
	}
}