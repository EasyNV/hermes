package handler

import (
	"context"
	"fmt"
	"testing"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Mock Store
// ---------------------------------------------------------------------------

type mockStore struct {
	listConversationsFn        func(ctx context.Context, workspaceID, status, assignedTo, waNumberID, search, channel string, sortOrder int32, page, pageSize int32) ([]*ConversationRow, int64, error)
	getConversationFn          func(ctx context.Context, id string) (*ConversationRow, error)
	getConversationContactFn   func(ctx context.Context, contactID string) (*ContactRow, error)
	getConversationWaNumberFn  func(ctx context.Context, waNumberID string) (*WaNumberRow, error)
	claimConversationFn        func(ctx context.Context, id, userID string) (*ConversationRow, error)
	transferConversationFn     func(ctx context.Context, id, toUserID string) (*ConversationRow, error)
	closeConversationFn        func(ctx context.Context, id string) (*ConversationRow, error)
	listMessagesFn             func(ctx context.Context, conversationID, beforeMessageID string, page, pageSize int32) ([]*MessageRow, bool, int64, error)
	createMessageFn            func(ctx context.Context, conversationID, direction, contentType string, body, mediaURL *string, waMessageID string) (*MessageRow, error)
	searchMessagesFn           func(ctx context.Context, workspaceID, query, conversationID string, fromDate, toDate *time.Time, page, pageSize int32) ([]*SearchHitRow, int64, error)
	updateMessageStatusFn      func(ctx context.Context, waMessageID, newStatus string) error
	getMessageByWaMessageIDFn  func(ctx context.Context, waMessageID string) (*MessageRow, error)
	findOrCreateConversationFn func(ctx context.Context, workspaceID, contactID, waNumberID string, campaignID *string) (*ConversationRow, bool, error)
	reopenConversationFn       func(ctx context.Context, id string) error
	updateLastMessageFn        func(ctx context.Context, conversationID, preview string) error
	setFirstResponseTimeFn     func(ctx context.Context, conversationID string, secs int32) error
	findContactByPhoneFn       func(ctx context.Context, phone string) (*ContactRow, string, error)
	getWorkspaceIDForWaNumberFn func(ctx context.Context, waNumberID string) (string, string, error)
	getWorkspaceIDForMbsUidFn  func(ctx context.Context, uid int64) (string, string, error)
	getPhoneByMbsThreadFn      func(ctx context.Context, uid int64, threadID string) (string, error)
	createCannedResponseFn     func(ctx context.Context, workspaceID, shortcut, body string, createdBy *string) (*CannedResponseRow, error)
	getCannedResponseFn        func(ctx context.Context, id string) (*CannedResponseRow, error)
	listCannedResponsesFn      func(ctx context.Context, workspaceID, search string, page, pageSize int32) ([]*CannedResponseRow, int64, error)
	updateCannedResponseFn     func(ctx context.Context, id, shortcut, body string) (*CannedResponseRow, error)
	deleteCannedResponseFn     func(ctx context.Context, id string) error
	getContactCampaignHistoryFn func(ctx context.Context, contactID string, page, pageSize int32) ([]*CampaignHistoryRow, int64, error)
	getAgentPerformanceFn      func(ctx context.Context, workspaceID, userID string, fromDate, toDate *time.Time) ([]*AgentPerfRow, error)
	autoCreateContactFn        func(ctx context.Context, tenantID, phone, name string) (*ContactRow, error)
	clearAllConversationsFn    func(ctx context.Context, workspaceID string) (int64, error)
	isPhoneAllowlistedFn       func(ctx context.Context, workspaceID, phone string) (bool, error)
	addToAllowlistFn           func(ctx context.Context, workspaceID, phone, source, sourceID string) error
	bulkAddToAllowlistFn       func(ctx context.Context, workspaceID string, phones []string, source, sourceID string) (int64, error)
	removeFromAllowlistFn      func(ctx context.Context, workspaceID, phone string) error
	listAllowlistFn            func(ctx context.Context, workspaceID string, page, pageSize int32) ([]AllowlistEntry, int64, error)
	// E3 chunk 2: MBS channel parallels.
	findOrCreateMbsConversationFn func(ctx context.Context, workspaceID, contactID, mbsSessionUID, mbsThreadID, mbsPageID string) (*ConversationRow, bool, error)
	createMbsMessageFn            func(ctx context.Context, conversationID, direction, body, mbsMID string) (*MessageRow, bool, error)
	getMessageByMbsMIDFn          func(ctx context.Context, mbsMID string) (*MessageRow, error)
	updateMbsMessageStatusFn      func(ctx context.Context, mbsMID, newStatus string) error
	setMbsMIDFn                   func(ctx context.Context, messageID, mbsMID string) error
	markOutboundFailedByIDFn      func(ctx context.Context, messageID string) error
}

func (m *mockStore) ListConversations(ctx context.Context, workspaceID, st, assignedTo, waNumberID, search, channel string, sortOrder int32, page, pageSize int32) ([]*ConversationRow, int64, error) {
	if m.listConversationsFn != nil {
		return m.listConversationsFn(ctx, workspaceID, st, assignedTo, waNumberID, search, channel, sortOrder, page, pageSize)
	}
	return nil, 0, fmt.Errorf("ListConversations not mocked")
}
func (m *mockStore) GetConversation(ctx context.Context, id string) (*ConversationRow, error) {
	if m.getConversationFn != nil {
		return m.getConversationFn(ctx, id)
	}
	return nil, fmt.Errorf("GetConversation not mocked")
}
func (m *mockStore) GetConversationContact(ctx context.Context, contactID string) (*ContactRow, error) {
	if m.getConversationContactFn != nil {
		return m.getConversationContactFn(ctx, contactID)
	}
	return nil, ErrNotFound
}
func (m *mockStore) GetConversationWaNumber(ctx context.Context, waNumberID string) (*WaNumberRow, error) {
	if m.getConversationWaNumberFn != nil {
		return m.getConversationWaNumberFn(ctx, waNumberID)
	}
	return nil, ErrNotFound
}
func (m *mockStore) ClaimConversation(ctx context.Context, id, userID string) (*ConversationRow, error) {
	if m.claimConversationFn != nil {
		return m.claimConversationFn(ctx, id, userID)
	}
	return nil, fmt.Errorf("ClaimConversation not mocked")
}
func (m *mockStore) TransferConversation(ctx context.Context, id, toUserID string) (*ConversationRow, error) {
	if m.transferConversationFn != nil {
		return m.transferConversationFn(ctx, id, toUserID)
	}
	return nil, fmt.Errorf("TransferConversation not mocked")
}
func (m *mockStore) CloseConversation(ctx context.Context, id string) (*ConversationRow, error) {
	if m.closeConversationFn != nil {
		return m.closeConversationFn(ctx, id)
	}
	return nil, fmt.Errorf("CloseConversation not mocked")
}
func (m *mockStore) ListMessages(ctx context.Context, conversationID, beforeMessageID string, page, pageSize int32) ([]*MessageRow, bool, int64, error) {
	if m.listMessagesFn != nil {
		return m.listMessagesFn(ctx, conversationID, beforeMessageID, page, pageSize)
	}
	return nil, false, 0, nil
}
func (m *mockStore) CreateMessage(ctx context.Context, conversationID, direction, contentType string, body, mediaURL *string, waMessageID string) (*MessageRow, error) {
	if m.createMessageFn != nil {
		return m.createMessageFn(ctx, conversationID, direction, contentType, body, mediaURL, waMessageID)
	}
	return nil, fmt.Errorf("CreateMessage not mocked")
}
func (m *mockStore) SearchMessages(ctx context.Context, workspaceID, query, conversationID string, fromDate, toDate *time.Time, page, pageSize int32) ([]*SearchHitRow, int64, error) {
	if m.searchMessagesFn != nil {
		return m.searchMessagesFn(ctx, workspaceID, query, conversationID, fromDate, toDate, page, pageSize)
	}
	return nil, 0, fmt.Errorf("SearchMessages not mocked")
}
func (m *mockStore) UpdateMessageStatus(ctx context.Context, waMessageID, newStatus string) error {
	if m.updateMessageStatusFn != nil {
		return m.updateMessageStatusFn(ctx, waMessageID, newStatus)
	}
	return fmt.Errorf("UpdateMessageStatus not mocked")
}
func (m *mockStore) GetMessageByWaMessageID(ctx context.Context, waMessageID string) (*MessageRow, error) {
	if m.getMessageByWaMessageIDFn != nil {
		return m.getMessageByWaMessageIDFn(ctx, waMessageID)
	}
	return nil, fmt.Errorf("GetMessageByWaMessageID not mocked")
}
func (m *mockStore) FindOrCreateConversation(ctx context.Context, workspaceID, contactID, waNumberID string, campaignID *string) (*ConversationRow, bool, error) {
	if m.findOrCreateConversationFn != nil {
		return m.findOrCreateConversationFn(ctx, workspaceID, contactID, waNumberID, campaignID)
	}
	return nil, false, fmt.Errorf("FindOrCreateConversation not mocked")
}
func (m *mockStore) ReopenConversation(ctx context.Context, id string) error {
	if m.reopenConversationFn != nil {
		return m.reopenConversationFn(ctx, id)
	}
	return nil
}
func (m *mockStore) UpdateLastMessage(ctx context.Context, conversationID, preview string) error {
	if m.updateLastMessageFn != nil {
		return m.updateLastMessageFn(ctx, conversationID, preview)
	}
	return nil
}
func (m *mockStore) SetFirstResponseTime(ctx context.Context, conversationID string, secs int32) error {
	if m.setFirstResponseTimeFn != nil {
		return m.setFirstResponseTimeFn(ctx, conversationID, secs)
	}
	return nil
}
func (m *mockStore) FindContactByPhone(ctx context.Context, phone string) (*ContactRow, string, error) {
	if m.findContactByPhoneFn != nil {
		return m.findContactByPhoneFn(ctx, phone)
	}
	return nil, "", ErrNotFound
}
func (m *mockStore) GetWorkspaceIDForWaNumber(ctx context.Context, waNumberID string) (string, string, error) {
	if m.getWorkspaceIDForWaNumberFn != nil {
		return m.getWorkspaceIDForWaNumberFn(ctx, waNumberID)
	}
	return "", "", ErrNotFound
}
func (m *mockStore) GetWorkspaceIDForMbsUid(ctx context.Context, uid int64) (string, string, error) {
	if m.getWorkspaceIDForMbsUidFn != nil {
		return m.getWorkspaceIDForMbsUidFn(ctx, uid)
	}
	return "", "", ErrNotFound
}
func (m *mockStore) GetPhoneByMbsThread(ctx context.Context, uid int64, threadID string) (string, error) {
	if m.getPhoneByMbsThreadFn != nil {
		return m.getPhoneByMbsThreadFn(ctx, uid, threadID)
	}
	return "", ErrNotFound
}
func (m *mockStore) CreateCannedResponse(ctx context.Context, workspaceID, shortcut, body string, createdBy *string) (*CannedResponseRow, error) {
	if m.createCannedResponseFn != nil {
		return m.createCannedResponseFn(ctx, workspaceID, shortcut, body, createdBy)
	}
	return nil, fmt.Errorf("CreateCannedResponse not mocked")
}
func (m *mockStore) GetCannedResponse(ctx context.Context, id string) (*CannedResponseRow, error) {
	if m.getCannedResponseFn != nil {
		return m.getCannedResponseFn(ctx, id)
	}
	return nil, fmt.Errorf("GetCannedResponse not mocked")
}
func (m *mockStore) ListCannedResponses(ctx context.Context, workspaceID, search string, page, pageSize int32) ([]*CannedResponseRow, int64, error) {
	if m.listCannedResponsesFn != nil {
		return m.listCannedResponsesFn(ctx, workspaceID, search, page, pageSize)
	}
	return nil, 0, fmt.Errorf("ListCannedResponses not mocked")
}
func (m *mockStore) UpdateCannedResponse(ctx context.Context, id, shortcut, body string) (*CannedResponseRow, error) {
	if m.updateCannedResponseFn != nil {
		return m.updateCannedResponseFn(ctx, id, shortcut, body)
	}
	return nil, fmt.Errorf("UpdateCannedResponse not mocked")
}
func (m *mockStore) DeleteCannedResponse(ctx context.Context, id string) error {
	if m.deleteCannedResponseFn != nil {
		return m.deleteCannedResponseFn(ctx, id)
	}
	return fmt.Errorf("DeleteCannedResponse not mocked")
}
func (m *mockStore) GetContactCampaignHistory(ctx context.Context, contactID string, page, pageSize int32) ([]*CampaignHistoryRow, int64, error) {
	if m.getContactCampaignHistoryFn != nil {
		return m.getContactCampaignHistoryFn(ctx, contactID, page, pageSize)
	}
	return nil, 0, fmt.Errorf("GetContactCampaignHistory not mocked")
}
func (m *mockStore) GetAgentPerformance(ctx context.Context, workspaceID, userID string, fromDate, toDate *time.Time) ([]*AgentPerfRow, error) {
	if m.getAgentPerformanceFn != nil {
		return m.getAgentPerformanceFn(ctx, workspaceID, userID, fromDate, toDate)
	}
	return nil, fmt.Errorf("GetAgentPerformance not mocked")
}

func (m *mockStore) AutoCreateContact(ctx context.Context, tenantID, phone, name string) (*ContactRow, error) {
	if m.autoCreateContactFn != nil {
		return m.autoCreateContactFn(ctx, tenantID, phone, name)
	}
	return &ContactRow{ID: "auto-" + phone, Phone: phone, Name: name}, nil
}

func (m *mockStore) ClearAllConversations(ctx context.Context, workspaceID string) (int64, error) {
	if m.clearAllConversationsFn != nil {
		return m.clearAllConversationsFn(ctx, workspaceID)
	}
	return 0, nil
}

func (m *mockStore) IsPhoneAllowlisted(ctx context.Context, workspaceID, phone string) (bool, error) {
	if m.isPhoneAllowlistedFn != nil {
		return m.isPhoneAllowlistedFn(ctx, workspaceID, phone)
	}
	return true, nil // default: allow all in tests
}

func (m *mockStore) AddToAllowlist(ctx context.Context, workspaceID, phone, source, sourceID string) error {
	if m.addToAllowlistFn != nil {
		return m.addToAllowlistFn(ctx, workspaceID, phone, source, sourceID)
	}
	return nil
}

func (m *mockStore) BulkAddToAllowlist(ctx context.Context, workspaceID string, phones []string, source, sourceID string) (int64, error) {
	if m.bulkAddToAllowlistFn != nil {
		return m.bulkAddToAllowlistFn(ctx, workspaceID, phones, source, sourceID)
	}
	return int64(len(phones)), nil
}

func (m *mockStore) RemoveFromAllowlist(ctx context.Context, workspaceID, phone string) error {
	if m.removeFromAllowlistFn != nil {
		return m.removeFromAllowlistFn(ctx, workspaceID, phone)
	}
	return nil
}

func (m *mockStore) ListAllowlist(ctx context.Context, workspaceID string, page, pageSize int32) ([]AllowlistEntry, int64, error) {
	if m.listAllowlistFn != nil {
		return m.listAllowlistFn(ctx, workspaceID, page, pageSize)
	}
	return nil, 0, nil
}

// ── E3 chunk 2: MBS channel parallels ─────────────────────────────

func (m *mockStore) FindOrCreateMbsConversation(ctx context.Context, workspaceID, contactID, mbsSessionUID, mbsThreadID, mbsPageID string) (*ConversationRow, bool, error) {
	if m.findOrCreateMbsConversationFn != nil {
		return m.findOrCreateMbsConversationFn(ctx, workspaceID, contactID, mbsSessionUID, mbsThreadID, mbsPageID)
	}
	return &ConversationRow{
		ID:            "mbs-conv-1",
		WorkspaceID:   workspaceID,
		ContactID:     contactID,
		Channel:       "mbs",
		MbsSessionUID: mbsSessionUID,
		MbsThreadID:   mbsThreadID,
		MbsPageID:     mbsPageID,
		Status:        "unassigned",
		CreatedAt:     time.Now(),
		LastMessageAt: time.Now(),
	}, true, nil
}

func (m *mockStore) CreateMbsMessage(ctx context.Context, conversationID, direction, body, mbsMID string) (*MessageRow, bool, error) {
	if m.createMbsMessageFn != nil {
		return m.createMbsMessageFn(ctx, conversationID, direction, body, mbsMID)
	}
	var bodyPtr *string
	if body != "" {
		bodyPtr = &body
	}
	initial := "pending"
	if direction == "inbound" {
		initial = "delivered"
	}
	return &MessageRow{
		ID:             "mbs-msg-1",
		ConversationID: conversationID,
		Direction:      direction,
		ContentType:    "text",
		Body:           bodyPtr,
		MbsMID:         mbsMID,
		Status:         initial,
		CreatedAt:      time.Now(),
	}, true, nil
}

func (m *mockStore) GetMessageByMbsMID(ctx context.Context, mbsMID string) (*MessageRow, error) {
	if m.getMessageByMbsMIDFn != nil {
		return m.getMessageByMbsMIDFn(ctx, mbsMID)
	}
	return nil, ErrNotFound
}

func (m *mockStore) UpdateMbsMessageStatus(ctx context.Context, mbsMID, newStatus string) error {
	if m.updateMbsMessageStatusFn != nil {
		return m.updateMbsMessageStatusFn(ctx, mbsMID, newStatus)
	}
	return nil
}
func (m *mockStore) SetMbsMID(ctx context.Context, messageID, mbsMID string) error {
	if m.setMbsMIDFn != nil {
		return m.setMbsMIDFn(ctx, messageID, mbsMID)
	}
	return nil
}
func (m *mockStore) MarkOutboundFailedByID(ctx context.Context, messageID string) error {
	if m.markOutboundFailedByIDFn != nil {
		return m.markOutboundFailedByIDFn(ctx, messageID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestHandler(s Store) *Handler {
	return New(s, nil, zerolog.Nop())
}

func assertCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != want {
		t.Fatalf("expected code %v, got %v: %s", want, st.Code(), st.Message())
	}
}

var fixedTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func testConversation(statusStr string) *ConversationRow {
	assigned := "user-1"
	return &ConversationRow{
		ID: "conv-1", WorkspaceID: "ws-1", ContactID: "ct-1", WaNumberID: "wn-1",
		AssignedTo: &assigned, Status: statusStr,
		LastMessageAt: fixedTime, FirstResponseTimeSecs: 0, CreatedAt: fixedTime,
		ContactName: "John", ContactPhone: "+628123",
	}
}

// ---------------------------------------------------------------------------
// ListConversations
// ---------------------------------------------------------------------------

func TestListConversations(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.InboxListConversationsRequest
		store    *mockStore
		wantCode codes.Code
		wantLen  int
	}{
		{
			name:     "missing workspace_id",
			req:      &hermesv1.InboxListConversationsRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "success returns conversations",
			req:  &hermesv1.InboxListConversationsRequest{WorkspaceId: "ws-1"},
			store: &mockStore{
				listConversationsFn: func(_ context.Context, _, _, _, _, _, _ string, _ int32, _, _ int32) ([]*ConversationRow, int64, error) {
					return []*ConversationRow{testConversation("unassigned")}, 1, nil
				},
			},
			wantCode: codes.OK,
			wantLen:  1,
		},
		{
			name: "empty result",
			req:  &hermesv1.InboxListConversationsRequest{WorkspaceId: "ws-1"},
			store: &mockStore{
				listConversationsFn: func(_ context.Context, _, _, _, _, _, _ string, _ int32, _, _ int32) ([]*ConversationRow, int64, error) {
					return nil, 0, nil
				},
			},
			wantCode: codes.OK,
			wantLen:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(tc.store)
			resp, err := h.ListConversations(context.Background(), tc.req)
			if tc.wantCode != codes.OK {
				assertCode(t, err, tc.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.Conversations) != tc.wantLen {
				t.Fatalf("expected %d conversations, got %d", tc.wantLen, len(resp.Conversations))
			}
		})
	}
}

// E3 chunk 5: channel filter must be threaded from proto enum to store
// string. Verifies inboxChannelToStr round-trip.
func TestListConversations_ChannelFilter(t *testing.T) {
	cases := []struct {
		name        string
		channel     hermesv1.InboxChannel
		wantStrArg  string
	}{
		{"unspecified maps to empty (no filter)", hermesv1.InboxChannel_INBOX_CHANNEL_UNSPECIFIED, ""},
		{"wa maps to wa", hermesv1.InboxChannel_INBOX_CHANNEL_WA, "wa"},
		{"mbs maps to mbs", hermesv1.InboxChannel_INBOX_CHANNEL_MBS, "mbs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var capturedChannel string
			store := &mockStore{
				listConversationsFn: func(_ context.Context, _, _, _, _, _, channelArg string, _ int32, _, _ int32) ([]*ConversationRow, int64, error) {
					capturedChannel = channelArg
					return nil, 0, nil
				},
			}
			h := newTestHandler(store)
			_, err := h.ListConversations(context.Background(), &hermesv1.InboxListConversationsRequest{
				WorkspaceId: "ws-1",
				Channel:     tc.channel,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if capturedChannel != tc.wantStrArg {
				t.Errorf("store channel arg: got %q, want %q", capturedChannel, tc.wantStrArg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetConversation
// ---------------------------------------------------------------------------

func TestGetConversation(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.InboxGetConversationRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name:     "missing id",
			req:      &hermesv1.InboxGetConversationRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "not found",
			req:  &hermesv1.InboxGetConversationRequest{Id: "conv-99"},
			store: &mockStore{
				getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					return nil, ErrNotFound
				},
			},
			wantCode: codes.NotFound,
		},
		{
			name: "success",
			req:  &hermesv1.InboxGetConversationRequest{Id: "conv-1"},
			store: &mockStore{
				getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					return testConversation("assigned"), nil
				},
			},
			wantCode: codes.OK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(tc.store)
			resp, err := h.GetConversation(context.Background(), tc.req)
			if tc.wantCode != codes.OK {
				assertCode(t, err, tc.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Conversation.Id != "conv-1" {
				t.Fatalf("expected conv-1, got %s", resp.Conversation.Id)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ClaimConversation
// ---------------------------------------------------------------------------

func TestClaimConversation(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.InboxClaimConversationRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name:     "missing id",
			req:      &hermesv1.InboxClaimConversationRequest{UserId: "u1"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "missing user_id",
			req:      &hermesv1.InboxClaimConversationRequest{Id: "conv-1"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "not found",
			req:  &hermesv1.InboxClaimConversationRequest{Id: "conv-99", UserId: "u1"},
			store: &mockStore{
				getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					return nil, ErrNotFound
				},
			},
			wantCode: codes.NotFound,
		},
		{
			name: "already assigned",
			req:  &hermesv1.InboxClaimConversationRequest{Id: "conv-1", UserId: "u1"},
			store: &mockStore{
				getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					return testConversation("assigned"), nil
				},
			},
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "success",
			req:  &hermesv1.InboxClaimConversationRequest{Id: "conv-1", UserId: "u1"},
			store: &mockStore{
				getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					c := testConversation("unassigned")
					c.AssignedTo = nil
					return c, nil
				},
				claimConversationFn: func(_ context.Context, _, userID string) (*ConversationRow, error) {
					c := testConversation("assigned")
					c.AssignedTo = &userID
					return c, nil
				},
			},
			wantCode: codes.OK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(tc.store)
			resp, err := h.ClaimConversation(context.Background(), tc.req)
			if tc.wantCode != codes.OK {
				assertCode(t, err, tc.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Conversation.Status != hermesv1.ConversationStatus_CONVERSATION_STATUS_ASSIGNED {
				t.Fatalf("expected ASSIGNED status, got %v", resp.Conversation.Status)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TransferConversation
// ---------------------------------------------------------------------------

func TestTransferConversation(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.InboxTransferConversationRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name:     "missing id",
			req:      &hermesv1.InboxTransferConversationRequest{ToUserId: "u2"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "not assigned",
			req:  &hermesv1.InboxTransferConversationRequest{Id: "conv-1", FromUserId: "u1", ToUserId: "u2"},
			store: &mockStore{
				getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					c := testConversation("unassigned")
					c.AssignedTo = nil
					return c, nil
				},
			},
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "wrong assignee",
			req:  &hermesv1.InboxTransferConversationRequest{Id: "conv-1", FromUserId: "u99", ToUserId: "u2"},
			store: &mockStore{
				getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					return testConversation("assigned"), nil
				},
			},
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "success by assignee",
			req:  &hermesv1.InboxTransferConversationRequest{Id: "conv-1", FromUserId: "user-1", ToUserId: "u2"},
			store: &mockStore{
				getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					return testConversation("assigned"), nil
				},
				transferConversationFn: func(_ context.Context, _, toUserID string) (*ConversationRow, error) {
					c := testConversation("assigned")
					c.AssignedTo = &toUserID
					return c, nil
				},
			},
			wantCode: codes.OK,
		},
		{
			name: "success by admin (empty from_user_id)",
			req:  &hermesv1.InboxTransferConversationRequest{Id: "conv-1", FromUserId: "", ToUserId: "u2"},
			store: &mockStore{
				getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					return testConversation("assigned"), nil
				},
				transferConversationFn: func(_ context.Context, _, toUserID string) (*ConversationRow, error) {
					c := testConversation("assigned")
					c.AssignedTo = &toUserID
					return c, nil
				},
			},
			wantCode: codes.OK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(tc.store)
			resp, err := h.TransferConversation(context.Background(), tc.req)
			if tc.wantCode != codes.OK {
				assertCode(t, err, tc.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Conversation.AssignedTo != "u2" {
				t.Fatalf("expected assigned_to=u2, got %s", resp.Conversation.AssignedTo)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CloseConversation
// ---------------------------------------------------------------------------

func TestCloseConversation(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.InboxCloseConversationRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name:     "missing id",
			req:      &hermesv1.InboxCloseConversationRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "already closed",
			req:  &hermesv1.InboxCloseConversationRequest{Id: "conv-1"},
			store: &mockStore{
				getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					return testConversation("closed"), nil
				},
			},
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "success",
			req:  &hermesv1.InboxCloseConversationRequest{Id: "conv-1"},
			store: &mockStore{
				getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					return testConversation("assigned"), nil
				},
				closeConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					c := testConversation("closed")
					c.AssignedTo = nil
					return c, nil
				},
			},
			wantCode: codes.OK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(tc.store)
			resp, err := h.CloseConversation(context.Background(), tc.req)
			if tc.wantCode != codes.OK {
				assertCode(t, err, tc.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Conversation.Status != hermesv1.ConversationStatus_CONVERSATION_STATUS_CLOSED {
				t.Fatalf("expected CLOSED, got %v", resp.Conversation.Status)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ListMessages
// ---------------------------------------------------------------------------

func TestListMessages(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.InboxListMessagesRequest
		store    *mockStore
		wantCode codes.Code
		wantLen  int
	}{
		{
			name:     "missing conversation_id",
			req:      &hermesv1.InboxListMessagesRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "success",
			req:  &hermesv1.InboxListMessagesRequest{ConversationId: "conv-1"},
			store: &mockStore{
				listMessagesFn: func(_ context.Context, _, _ string, _, _ int32) ([]*MessageRow, bool, int64, error) {
					body := "Hello"
					return []*MessageRow{{
						ID: "msg-1", ConversationID: "conv-1", Direction: "inbound",
						ContentType: "text", Body: &body, WaMessageID: "wa-1",
						Status: "delivered", CreatedAt: fixedTime,
					}}, false, 1, nil
				},
			},
			wantCode: codes.OK,
			wantLen:  1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(tc.store)
			resp, err := h.ListMessages(context.Background(), tc.req)
			if tc.wantCode != codes.OK {
				assertCode(t, err, tc.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.Messages) != tc.wantLen {
				t.Fatalf("expected %d messages, got %d", tc.wantLen, len(resp.Messages))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SendMessage
// ---------------------------------------------------------------------------

func TestSendMessage(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.InboxSendMessageRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name:     "missing conversation_id",
			req:      &hermesv1.InboxSendMessageRequest{SenderUserId: "u1"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "missing sender_user_id",
			req:      &hermesv1.InboxSendMessageRequest{ConversationId: "conv-1"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "success creates message",
			req: &hermesv1.InboxSendMessageRequest{
				ConversationId: "conv-1", SenderUserId: "u1",
				ContentType: hermesv1.ContentType_CONTENT_TYPE_TEXT, Body: "Hello",
			},
			store: &mockStore{
				createMessageFn: func(_ context.Context, _, _, _ string, _, _ *string, _ string) (*MessageRow, error) {
					body := "Hello"
					return &MessageRow{
						ID: "msg-1", ConversationID: "conv-1", Direction: "outbound",
						ContentType: "text", Body: &body, WaMessageID: "",
						Status: "pending", CreatedAt: fixedTime,
					}, nil
				},
				getConversationFn: func(_ context.Context, _ string) (*ConversationRow, error) {
					return testConversation("assigned"), nil
				},
			},
			wantCode: codes.OK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(tc.store)
			resp, err := h.SendMessage(context.Background(), tc.req)
			if tc.wantCode != codes.OK {
				assertCode(t, err, tc.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Message.Status != hermesv1.MessageStatus_MESSAGE_STATUS_PENDING {
				t.Fatalf("expected PENDING status, got %v", resp.Message.Status)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SearchMessages
// ---------------------------------------------------------------------------

func TestSearchMessages(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.InboxSearchMessagesRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name:     "missing workspace_id",
			req:      &hermesv1.InboxSearchMessagesRequest{Query: "hello"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "missing query",
			req:      &hermesv1.InboxSearchMessagesRequest{WorkspaceId: "ws-1"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "success",
			req:  &hermesv1.InboxSearchMessagesRequest{WorkspaceId: "ws-1", Query: "hello"},
			store: &mockStore{
				searchMessagesFn: func(_ context.Context, _, _, _ string, _, _ *time.Time, _, _ int32) ([]*SearchHitRow, int64, error) {
					body := "hello world"
					return []*SearchHitRow{{
						MessageRow: MessageRow{
							ID: "msg-1", ConversationID: "conv-1", Direction: "inbound",
							ContentType: "text", Body: &body, Status: "delivered", CreatedAt: fixedTime,
						},
						ConversationID: "conv-1",
						ContactName:    "John",
						ContactPhone:   "+628123",
						Highlight:      "<mark>hello</mark> world",
					}}, 1, nil
				},
			},
			wantCode: codes.OK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(tc.store)
			resp, err := h.SearchMessages(context.Background(), tc.req)
			if tc.wantCode != codes.OK {
				assertCode(t, err, tc.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.Hits) != 1 {
				t.Fatalf("expected 1 hit, got %d", len(resp.Hits))
			}
			if resp.Hits[0].Highlight != "<mark>hello</mark> world" {
				t.Fatalf("unexpected highlight: %s", resp.Hits[0].Highlight)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CannedResponse CRUD
// ---------------------------------------------------------------------------

func TestCreateCannedResponse(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.InboxCreateCannedResponseRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name:     "missing workspace_id",
			req:      &hermesv1.InboxCreateCannedResponseRequest{Shortcut: "/hi", Body: "Hi"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name:     "missing shortcut",
			req:      &hermesv1.InboxCreateCannedResponseRequest{WorkspaceId: "ws-1", Body: "Hi"},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "duplicate shortcut",
			req:  &hermesv1.InboxCreateCannedResponseRequest{WorkspaceId: "ws-1", Shortcut: "/hi", Body: "Hi"},
			store: &mockStore{
				createCannedResponseFn: func(_ context.Context, _, _, _ string, _ *string) (*CannedResponseRow, error) {
					return nil, ErrDuplicateShortcut
				},
			},
			wantCode: codes.AlreadyExists,
		},
		{
			name: "success",
			req:  &hermesv1.InboxCreateCannedResponseRequest{WorkspaceId: "ws-1", Shortcut: "/hi", Body: "Hello!"},
			store: &mockStore{
				createCannedResponseFn: func(_ context.Context, _, shortcut, body string, _ *string) (*CannedResponseRow, error) {
					return &CannedResponseRow{
						ID: "cr-1", WorkspaceID: "ws-1", Shortcut: shortcut, Body: body, CreatedAt: fixedTime,
					}, nil
				},
			},
			wantCode: codes.OK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(tc.store)
			resp, err := h.CreateCannedResponse(context.Background(), tc.req)
			if tc.wantCode != codes.OK {
				assertCode(t, err, tc.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.CannedResponse.Shortcut != "/hi" {
				t.Fatalf("expected shortcut /hi, got %s", resp.CannedResponse.Shortcut)
			}
		})
	}
}

func TestDeleteCannedResponse(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.InboxDeleteCannedResponseRequest
		store    *mockStore
		wantCode codes.Code
	}{
		{
			name:     "missing id",
			req:      &hermesv1.InboxDeleteCannedResponseRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "not found",
			req:  &hermesv1.InboxDeleteCannedResponseRequest{Id: "cr-99"},
			store: &mockStore{
				deleteCannedResponseFn: func(_ context.Context, _ string) error {
					return ErrNotFound
				},
			},
			wantCode: codes.NotFound,
		},
		{
			name: "success",
			req:  &hermesv1.InboxDeleteCannedResponseRequest{Id: "cr-1"},
			store: &mockStore{
				deleteCannedResponseFn: func(_ context.Context, _ string) error { return nil },
			},
			wantCode: codes.OK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(tc.store)
			_, err := h.DeleteCannedResponse(context.Background(), tc.req)
			if tc.wantCode != codes.OK {
				assertCode(t, err, tc.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetContactCampaignHistory
// ---------------------------------------------------------------------------

func TestGetContactCampaignHistory(t *testing.T) {
	tests := []struct {
		name     string
		req      *hermesv1.InboxGetContactCampaignHistoryRequest
		store    *mockStore
		wantCode codes.Code
		wantLen  int
	}{
		{
			name:     "missing contact_id",
			req:      &hermesv1.InboxGetContactCampaignHistoryRequest{},
			store:    &mockStore{},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "success",
			req:  &hermesv1.InboxGetContactCampaignHistoryRequest{ContactId: "ct-1"},
			store: &mockStore{
				getContactCampaignHistoryFn: func(_ context.Context, _ string, _, _ int32) ([]*CampaignHistoryRow, int64, error) {
					sent := fixedTime
					return []*CampaignHistoryRow{{
						CampaignID: "camp-1", CampaignName: "Test Campaign",
						TemplateName: "greeting", ResolvedBody: "Hi John",
						Status: "delivered", SentAt: &sent,
					}}, 1, nil
				},
			},
			wantCode: codes.OK,
			wantLen:  1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(tc.store)
			resp, err := h.GetContactCampaignHistory(context.Background(), tc.req)
			if tc.wantCode != codes.OK {
				assertCode(t, err, tc.wantCode)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.Campaigns) != tc.wantLen {
				t.Fatalf("expected %d campaigns, got %d", tc.wantLen, len(resp.Campaigns))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// E3 chunk 2: row-to-proto channel/MBS field projection + helper unit tests
// ---------------------------------------------------------------------------

func TestStrToInboxChannel(t *testing.T) {
	tests := []struct {
		in   string
		want hermesv1.InboxChannel
	}{
		{"wa", hermesv1.InboxChannel_INBOX_CHANNEL_WA},
		{"mbs", hermesv1.InboxChannel_INBOX_CHANNEL_MBS},
		{"", hermesv1.InboxChannel_INBOX_CHANNEL_UNSPECIFIED},
		{"unknown", hermesv1.InboxChannel_INBOX_CHANNEL_UNSPECIFIED},
	}
	for _, tc := range tests {
		got := strToInboxChannel(tc.in)
		if got != tc.want {
			t.Errorf("strToInboxChannel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestConversationRowToProto_MbsChannel(t *testing.T) {
	r := &ConversationRow{
		ID:            "conv-1",
		WorkspaceID:   "ws-1",
		ContactID:     "ct-1",
		WaNumberID:    "",
		Status:        "unassigned",
		LastMessageAt: time.Now(),
		CreatedAt:     time.Now(),
		Channel:       "mbs",
		MbsSessionUID: "1674772559",
		MbsThreadID:   "thr-abc12",
		MbsPageID:     "page-xy12",
	}
	p := conversationRowToProto(r)
	if p.Channel != hermesv1.InboxChannel_INBOX_CHANNEL_MBS {
		t.Errorf("Channel: got %v want MBS", p.Channel)
	}
	if p.MbsSessionUid != "1674772559" {
		t.Errorf("MbsSessionUid: got %q", p.MbsSessionUid)
	}
	if p.MbsThreadId != "thr-abc12" {
		t.Errorf("MbsThreadId: got %q", p.MbsThreadId)
	}
	if p.MbsPageId != "page-xy12" {
		t.Errorf("MbsPageId: got %q", p.MbsPageId)
	}
	if p.WaNumberId != "" {
		t.Errorf("WaNumberId should be empty for MBS channel: got %q", p.WaNumberId)
	}
}

func TestConversationRowToProto_WaChannel_LeavesMbsFieldsEmpty(t *testing.T) {
	r := &ConversationRow{
		ID:            "conv-2",
		WorkspaceID:   "ws-1",
		ContactID:     "ct-2",
		WaNumberID:    "wa-1",
		Status:        "assigned",
		LastMessageAt: time.Now(),
		CreatedAt:     time.Now(),
		Channel:       "wa",
	}
	p := conversationRowToProto(r)
	if p.Channel != hermesv1.InboxChannel_INBOX_CHANNEL_WA {
		t.Errorf("Channel: got %v want WA", p.Channel)
	}
	if p.MbsSessionUid != "" || p.MbsThreadId != "" || p.MbsPageId != "" {
		t.Errorf("MBS fields should be empty for WA: %+v / %+v / %+v",
			p.MbsSessionUid, p.MbsThreadId, p.MbsPageId)
	}
	if p.WaNumberId != "wa-1" {
		t.Errorf("WaNumberId: got %q", p.WaNumberId)
	}
}

func TestMessageRowToProto_CarriesMbsMid(t *testing.T) {
	m := &MessageRow{
		ID:             "msg-1",
		ConversationID: "conv-1",
		Direction:      "inbound",
		ContentType:    "text",
		Status:         "delivered",
		CreatedAt:      time.Now(),
		MbsMID:         "mid.$cAAAA_test",
	}
	p := messageRowToProto(m)
	if p.MbsMid != "mid.$cAAAA_test" {
		t.Errorf("MbsMid: got %q want mid.$cAAAA_test", p.MbsMid)
	}
}
