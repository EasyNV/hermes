package handler

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
)

// ─────────────────────────────────────────────────────────────────────
// Chunk 8 — channel validation in CreateCampaign
//
// Verifies the four guard rails added to handler.CreateCampaign:
//   1. Empty channel defaults to 'wa' (wire-compat with pre-chunk-8 clients)
//   2. Invalid channel string → InvalidArgument
//   3. channel='wa' + mbs_session_uids non-empty → InvalidArgument
//   4. channel='mbs' + wa_number_ids non-empty → InvalidArgument
// And the happy path:
//   5. channel='mbs' + mbs_session_uids non-empty → store.AddCampaignMbsSessions called
// ─────────────────────────────────────────────────────────────────────

// channelTestStore wraps mockStore with hooks for the chunk-8 surface.
// The base mockStore (in handler_test.go) intentionally stubs the new
// MBS sender methods so the existing test corpus still compiles; this
// fixture overrides them to capture calls.
type channelTestStore struct {
	*mockStore
	tmpl           *TemplateRow
	mbsSessionAdds []int64
	lastChannel    atomic.Value // string
}

func newChannelTestStore() *channelTestStore {
	tmpl := &TemplateRow{
		ID: "tmpl-1", WorkspaceID: "ws-1", Name: "T", Body: "hello",
	}
	st := &channelTestStore{
		mockStore: &mockStore{},
		tmpl:      tmpl,
	}
	st.getTemplateFn = func(_ context.Context, id string) (*TemplateRow, error) {
		if id == tmpl.ID {
			return tmpl, nil
		}
		return nil, nil
	}
	st.createCampaignFn = func(_ context.Context, r *CampaignRow) (*CampaignRow, error) {
		r.ID = "test-campaign-id"
		st.lastChannel.Store(r.Channel)
		return r, nil
	}
	st.getCampaignFn = func(_ context.Context, id string) (*CampaignRow, error) {
		ch, _ := st.lastChannel.Load().(string)
		return &CampaignRow{ID: id, WorkspaceID: "ws-1", Channel: ch}, nil
	}
	return st
}

// Override the stubbed MBS-add method on the base mockStore to capture
// the call. mockStore.AddCampaignMbsSessions is a method on the embedded
// type; Go's promoted-method rules let us shadow it cleanly.
func (s *channelTestStore) AddCampaignMbsSessions(_ context.Context, _ string, uids []int64) error {
	s.mbsSessionAdds = append(s.mbsSessionAdds, uids...)
	return nil
}

func TestCreateCampaign_Channel_DefaultsToWa(t *testing.T) {
	st := newChannelTestStore()
	h := newTestHandler(st)

	resp, err := h.CreateCampaign(context.Background(), &hermesv1.CampaignCreateRequest{
		WorkspaceId: "ws-1",
		TemplateId:  st.tmpl.ID,
		Name:        "default channel",
		// Channel: "" → server defaults to 'wa'
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if got := resp.GetCampaign().GetChannel(); got != "wa" {
		t.Errorf("default channel: want %q, got %q", "wa", got)
	}
}

func TestCreateCampaign_Channel_ExplicitMbs(t *testing.T) {
	st := newChannelTestStore()
	h := newTestHandler(st)

	resp, err := h.CreateCampaign(context.Background(), &hermesv1.CampaignCreateRequest{
		WorkspaceId:    "ws-1",
		TemplateId:     st.tmpl.ID,
		Name:           "mbs campaign",
		Channel:        "mbs",
		MbsSessionUids: []int64{61590134170831},
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if got := resp.GetCampaign().GetChannel(); got != "mbs" {
		t.Errorf("explicit channel: want %q, got %q", "mbs", got)
	}
	if len(st.mbsSessionAdds) != 1 || st.mbsSessionAdds[0] != 61590134170831 {
		t.Errorf("expected AddCampaignMbsSessions called with [61590134170831], got %v", st.mbsSessionAdds)
	}
}

func TestCreateCampaign_Channel_InvalidValueRejected(t *testing.T) {
	st := newChannelTestStore()
	h := newTestHandler(st)

	_, err := h.CreateCampaign(context.Background(), &hermesv1.CampaignCreateRequest{
		WorkspaceId: "ws-1",
		TemplateId:  st.tmpl.ID,
		Name:        "bad channel",
		Channel:     "instagram",
	})
	if err == nil {
		t.Fatal("expected InvalidArgument, got nil")
	}
	if !strings.Contains(err.Error(), "channel must be 'wa' or 'mbs'") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCreateCampaign_Channel_WaWithMbsSessionsRejected(t *testing.T) {
	st := newChannelTestStore()
	h := newTestHandler(st)

	_, err := h.CreateCampaign(context.Background(), &hermesv1.CampaignCreateRequest{
		WorkspaceId:    "ws-1",
		TemplateId:     st.tmpl.ID,
		Name:           "mixed",
		Channel:        "wa",
		WaNumberIds:    []string{"wa-1"},
		MbsSessionUids: []int64{42},
	})
	if err == nil {
		t.Fatal("expected InvalidArgument, got nil")
	}
	if !strings.Contains(err.Error(), "mbs_session_uids must be empty when channel='wa'") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCreateCampaign_Channel_MbsWithWaNumbersRejected(t *testing.T) {
	st := newChannelTestStore()
	h := newTestHandler(st)

	_, err := h.CreateCampaign(context.Background(), &hermesv1.CampaignCreateRequest{
		WorkspaceId:    "ws-1",
		TemplateId:     st.tmpl.ID,
		Name:           "mixed",
		Channel:        "mbs",
		WaNumberIds:    []string{"wa-1"},
		MbsSessionUids: []int64{42},
	})
	if err == nil {
		t.Fatal("expected InvalidArgument, got nil")
	}
	if !strings.Contains(err.Error(), "wa_number_ids must be empty when channel='mbs'") {
		t.Errorf("unexpected error: %v", err)
	}
}
