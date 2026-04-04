package handler

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/inbox/conversation"
	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler implements the HermesInboxServer gRPC interface.
type Handler struct {
	hermesv1.UnimplementedHermesInboxServer
	store Store
	js    natsgo.JetStreamContext
	log   zerolog.Logger
}

func New(store Store, js natsgo.JetStreamContext, log zerolog.Logger) *Handler {
	return &Handler{store: store, js: js, log: log}
}

// ---------------------------------------------------------------------------
// Proto ↔ DB conversion helpers
// ---------------------------------------------------------------------------

func conversationStatusToStr(s hermesv1.ConversationStatus) string {
	switch s {
	case hermesv1.ConversationStatus_CONVERSATION_STATUS_UNASSIGNED:
		return "unassigned"
	case hermesv1.ConversationStatus_CONVERSATION_STATUS_ASSIGNED:
		return "assigned"
	case hermesv1.ConversationStatus_CONVERSATION_STATUS_CLOSED:
		return "closed"
	default:
		return ""
	}
}

func strToConversationStatus(s string) hermesv1.ConversationStatus {
	switch s {
	case "unassigned":
		return hermesv1.ConversationStatus_CONVERSATION_STATUS_UNASSIGNED
	case "assigned":
		return hermesv1.ConversationStatus_CONVERSATION_STATUS_ASSIGNED
	case "closed":
		return hermesv1.ConversationStatus_CONVERSATION_STATUS_CLOSED
	default:
		return hermesv1.ConversationStatus_CONVERSATION_STATUS_UNSPECIFIED
	}
}

func contentTypeToStr(ct hermesv1.ContentType) string {
	switch ct {
	case hermesv1.ContentType_CONTENT_TYPE_TEXT:
		return "text"
	case hermesv1.ContentType_CONTENT_TYPE_IMAGE:
		return "image"
	case hermesv1.ContentType_CONTENT_TYPE_DOCUMENT:
		return "document"
	case hermesv1.ContentType_CONTENT_TYPE_AUDIO:
		return "audio"
	case hermesv1.ContentType_CONTENT_TYPE_VIDEO:
		return "video"
	default:
		return "text"
	}
}

func strToContentType(s string) hermesv1.ContentType {
	switch s {
	case "text":
		return hermesv1.ContentType_CONTENT_TYPE_TEXT
	case "image":
		return hermesv1.ContentType_CONTENT_TYPE_IMAGE
	case "document":
		return hermesv1.ContentType_CONTENT_TYPE_DOCUMENT
	case "audio":
		return hermesv1.ContentType_CONTENT_TYPE_AUDIO
	case "video":
		return hermesv1.ContentType_CONTENT_TYPE_VIDEO
	default:
		return hermesv1.ContentType_CONTENT_TYPE_UNSPECIFIED
	}
}

func strToMessageDirection(s string) hermesv1.MessageDirection {
	switch s {
	case "inbound":
		return hermesv1.MessageDirection_MESSAGE_DIRECTION_INBOUND
	case "outbound":
		return hermesv1.MessageDirection_MESSAGE_DIRECTION_OUTBOUND
	default:
		return hermesv1.MessageDirection_MESSAGE_DIRECTION_UNSPECIFIED
	}
}

func strToMessageStatus(s string) hermesv1.MessageStatus {
	switch s {
	case "pending":
		return hermesv1.MessageStatus_MESSAGE_STATUS_PENDING
	case "sent":
		return hermesv1.MessageStatus_MESSAGE_STATUS_SENT
	case "delivered":
		return hermesv1.MessageStatus_MESSAGE_STATUS_DELIVERED
	case "read":
		return hermesv1.MessageStatus_MESSAGE_STATUS_READ
	case "failed":
		return hermesv1.MessageStatus_MESSAGE_STATUS_FAILED
	default:
		return hermesv1.MessageStatus_MESSAGE_STATUS_UNSPECIFIED
	}
}

func messageStatusToStr(s hermesv1.MessageStatus) string {
	switch s {
	case hermesv1.MessageStatus_MESSAGE_STATUS_PENDING:
		return "pending"
	case hermesv1.MessageStatus_MESSAGE_STATUS_SENT:
		return "sent"
	case hermesv1.MessageStatus_MESSAGE_STATUS_DELIVERED:
		return "delivered"
	case hermesv1.MessageStatus_MESSAGE_STATUS_READ:
		return "read"
	case hermesv1.MessageStatus_MESSAGE_STATUS_FAILED:
		return "failed"
	default:
		return ""
	}
}

func strToContactSendStatus(s string) hermesv1.ContactSendStatus {
	switch s {
	case "pending":
		return hermesv1.ContactSendStatus_CONTACT_SEND_STATUS_PENDING
	case "sent":
		return hermesv1.ContactSendStatus_CONTACT_SEND_STATUS_SENT
	case "delivered":
		return hermesv1.ContactSendStatus_CONTACT_SEND_STATUS_DELIVERED
	case "failed":
		return hermesv1.ContactSendStatus_CONTACT_SEND_STATUS_FAILED
	case "skipped":
		return hermesv1.ContactSendStatus_CONTACT_SEND_STATUS_SKIPPED
	default:
		return hermesv1.ContactSendStatus_CONTACT_SEND_STATUS_UNSPECIFIED
	}
}

func conversationRowToProto(r *ConversationRow) *hermesv1.Conversation {
	c := &hermesv1.Conversation{
		Id:                    r.ID,
		WorkspaceId:           r.WorkspaceID,
		ContactId:             r.ContactID,
		WaNumberId:            r.WaNumberID,
		Status:                strToConversationStatus(r.Status),
		LastMessageAt:         timestamppb.New(r.LastMessageAt),
		FirstResponseTimeSecs: r.FirstResponseTimeSecs,
		CreatedAt:             timestamppb.New(r.CreatedAt),
		ContactName:           r.ContactName,
		ContactPhone:          r.ContactPhone,
		LastMessagePreview:    r.LastMessagePreview,
		UnreadCount:           r.UnreadCount,
	}
	if r.AssignedTo != nil {
		c.AssignedTo = *r.AssignedTo
	}
	if r.CampaignID != nil {
		c.CampaignId = *r.CampaignID
	}
	return c
}

func messageRowToProto(m *MessageRow) *hermesv1.Message {
	msg := &hermesv1.Message{
		Id:             m.ID,
		ConversationId: m.ConversationID,
		Direction:      strToMessageDirection(m.Direction),
		ContentType:    strToContentType(m.ContentType),
		WaMessageId:    m.WaMessageID,
		Status:         strToMessageStatus(m.Status),
		CreatedAt:      timestamppb.New(m.CreatedAt),
	}
	if m.Body != nil {
		msg.Body = *m.Body
	}
	if m.MediaURL != nil {
		msg.MediaUrl = *m.MediaURL
	}
	if m.TemplateID != nil {
		msg.TemplateId = *m.TemplateID
	}
	if m.ResolvedVarsJSON != nil {
		msg.ResolvedVarsJson = *m.ResolvedVarsJSON
	}
	return msg
}

func cannedRowToProto(r *CannedResponseRow) *hermesv1.CannedResponse {
	cr := &hermesv1.CannedResponse{
		Id:          r.ID,
		WorkspaceId: r.WorkspaceID,
		Shortcut:    r.Shortcut,
		Body:        r.Body,
		CreatedAt:   timestamppb.New(r.CreatedAt),
	}
	if r.CreatedBy != nil {
		cr.CreatedBy = *r.CreatedBy
	}
	return cr
}

// normalizePage clamps pagination values.
func normalizePage(pr *hermesv1.PageRequest) (page, pageSize int32) {
	page = 1
	pageSize = 50
	if pr != nil {
		if pr.Page > 0 {
			page = pr.Page
		}
		if pr.PageSize > 0 {
			pageSize = pr.PageSize
		}
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return
}

func pageResponse(total int64, page, pageSize int32) *hermesv1.PageResponse {
	totalPages := int32(0)
	if total > 0 {
		totalPages = int32((total + int64(pageSize) - 1) / int64(pageSize))
	}
	return &hermesv1.PageResponse{
		Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages,
	}
}

// ---------------------------------------------------------------------------
// RPC 1: ListConversations
// ---------------------------------------------------------------------------
// RBAC NOTE: CS agents should only see (status=UNASSIGNED) OR (assigned_to=self).
// The gateway injects these filters via the request fields before calling this RPC.
// This handler applies whatever filters it receives without enforcing role checks.

func (h *Handler) ListConversations(ctx context.Context, req *hermesv1.InboxListConversationsRequest) (*hermesv1.InboxListConversationsResponse, error) {
	if req.WorkspaceId == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}

	page, pageSize := normalizePage(req.Pagination)
	rows, total, err := h.store.ListConversations(ctx,
		req.WorkspaceId,
		conversationStatusToStr(req.Status),
		req.AssignedTo,
		req.WaNumberId,
		req.Search,
		int32(req.SortOrder),
		page, pageSize,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing conversations: %v", err)
	}

	conversations := make([]*hermesv1.Conversation, 0, len(rows))
	for _, r := range rows {
		conversations = append(conversations, conversationRowToProto(r))
	}

	return &hermesv1.InboxListConversationsResponse{
		Conversations: conversations,
		Pagination:    pageResponse(total, page, pageSize),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 2: GetConversation
// ---------------------------------------------------------------------------

func (h *Handler) GetConversation(ctx context.Context, req *hermesv1.InboxGetConversationRequest) (*hermesv1.InboxGetConversationResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	conv, err := h.store.GetConversation(ctx, req.Id)
	if err != nil {
		if err == ErrNotFound {
			return nil, status.Error(codes.NotFound, "conversation not found")
		}
		return nil, status.Errorf(codes.Internal, "getting conversation: %v", err)
	}

	resp := &hermesv1.InboxGetConversationResponse{
		Conversation: conversationRowToProto(conv),
	}

	// Fetch contact.
	if contact, cerr := h.store.GetConversationContact(ctx, conv.ContactID); cerr == nil {
		resp.Contact = &hermesv1.Contact{
			Id: contact.ID, Phone: contact.Phone, Name: contact.Name,
		}
	}

	// Fetch WA number.
	if waNum, werr := h.store.GetConversationWaNumber(ctx, conv.WaNumberID); werr == nil {
		resp.WaNumber = &hermesv1.WaNumber{
			Id: waNum.ID, Phone: waNum.Phone, DisplayName: waNum.DisplayName, Jid: waNum.JID,
			TenantId: waNum.TenantID,
		}
	}

	// Fetch last 20 messages.
	msgs, _, _, _ := h.store.ListMessages(ctx, req.Id, "", 1, 20)
	for _, m := range msgs {
		resp.RecentMessages = append(resp.RecentMessages, messageRowToProto(m))
	}

	return resp, nil
}

// ---------------------------------------------------------------------------
// RPC 3: ClaimConversation
// ---------------------------------------------------------------------------

func (h *Handler) ClaimConversation(ctx context.Context, req *hermesv1.InboxClaimConversationRequest) (*hermesv1.InboxClaimConversationResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	// Validate state transition.
	conv, err := h.store.GetConversation(ctx, req.Id)
	if err != nil {
		if err == ErrNotFound {
			return nil, status.Error(codes.NotFound, "conversation not found")
		}
		return nil, status.Errorf(codes.Internal, "getting conversation: %v", err)
	}
	if err := conversation.CanClaim(conv.Status); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "cannot claim: %v", err)
	}

	row, err := h.store.ClaimConversation(ctx, req.Id, req.UserId)
	if err != nil {
		if err == ErrAlreadyAssigned {
			return nil, status.Error(codes.FailedPrecondition, "conversation is already assigned")
		}
		if err == ErrNotFound {
			return nil, status.Error(codes.NotFound, "conversation not found")
		}
		return nil, status.Errorf(codes.Internal, "claiming conversation: %v", err)
	}

	return &hermesv1.InboxClaimConversationResponse{
		Conversation: conversationRowToProto(row),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 4: TransferConversation
// ---------------------------------------------------------------------------

func (h *Handler) TransferConversation(ctx context.Context, req *hermesv1.InboxTransferConversationRequest) (*hermesv1.InboxTransferConversationResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if req.ToUserId == "" {
		return nil, status.Error(codes.InvalidArgument, "to_user_id is required")
	}

	// Validate state: must be assigned, caller must be current assignee (or admin).
	// The gateway handles admin role check. For non-admin, from_user_id must match assigned_to.
	conv, err := h.store.GetConversation(ctx, req.Id)
	if err != nil {
		if err == ErrNotFound {
			return nil, status.Error(codes.NotFound, "conversation not found")
		}
		return nil, status.Errorf(codes.Internal, "getting conversation: %v", err)
	}

	// isAdmin is signaled by from_user_id being empty (gateway omits it for admins).
	isAdmin := req.FromUserId == ""
	if err := conversation.CanTransfer(conv.Status, ptrToStr(conv.AssignedTo), req.FromUserId, isAdmin); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "cannot transfer: %v", err)
	}

	row, err := h.store.TransferConversation(ctx, req.Id, req.ToUserId)
	if err != nil {
		if err == ErrNotFound {
			return nil, status.Error(codes.NotFound, "conversation not found")
		}
		return nil, status.Errorf(codes.Internal, "transferring conversation: %v", err)
	}

	return &hermesv1.InboxTransferConversationResponse{
		Conversation: conversationRowToProto(row),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 5: CloseConversation
// ---------------------------------------------------------------------------

func (h *Handler) CloseConversation(ctx context.Context, req *hermesv1.InboxCloseConversationRequest) (*hermesv1.InboxCloseConversationResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	conv, err := h.store.GetConversation(ctx, req.Id)
	if err != nil {
		if err == ErrNotFound {
			return nil, status.Error(codes.NotFound, "conversation not found")
		}
		return nil, status.Errorf(codes.Internal, "getting conversation: %v", err)
	}

	if err := conversation.CanClose(conv.Status); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "cannot close: %v", err)
	}

	row, err := h.store.CloseConversation(ctx, req.Id)
	if err != nil {
		if err == ErrNotFound {
			return nil, status.Error(codes.NotFound, "conversation not found")
		}
		return nil, status.Errorf(codes.Internal, "closing conversation: %v", err)
	}

	return &hermesv1.InboxCloseConversationResponse{
		Conversation: conversationRowToProto(row),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 6: ListMessages
// ---------------------------------------------------------------------------

func (h *Handler) ListMessages(ctx context.Context, req *hermesv1.InboxListMessagesRequest) (*hermesv1.InboxListMessagesResponse, error) {
	if req.ConversationId == "" {
		return nil, status.Error(codes.InvalidArgument, "conversation_id is required")
	}

	page, pageSize := normalizePage(req.Pagination)
	rows, hasMore, total, err := h.store.ListMessages(ctx, req.ConversationId, req.BeforeMessageId, page, pageSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing messages: %v", err)
	}

	msgs := make([]*hermesv1.Message, 0, len(rows))
	for _, m := range rows {
		msgs = append(msgs, messageRowToProto(m))
	}

	return &hermesv1.InboxListMessagesResponse{
		Messages:   msgs,
		Pagination: pageResponse(total, page, pageSize),
		HasMore:    hasMore,
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 7: SendMessage
// ---------------------------------------------------------------------------

func (h *Handler) SendMessage(ctx context.Context, req *hermesv1.InboxSendMessageRequest) (*hermesv1.InboxSendMessageResponse, error) {
	if req.ConversationId == "" {
		return nil, status.Error(codes.InvalidArgument, "conversation_id is required")
	}
	if req.SenderUserId == "" {
		return nil, status.Error(codes.InvalidArgument, "sender_user_id is required")
	}

	ct := contentTypeToStr(req.ContentType)
	if ct == "" {
		ct = "text"
	}

	var body, mediaURL *string
	if req.Body != "" {
		body = &req.Body
	}
	if req.MediaUrl != "" {
		mediaURL = &req.MediaUrl
	}

	// Create message in DB (status = PENDING).
	msg, err := h.store.CreateMessage(ctx, req.ConversationId, "outbound", ct, body, mediaURL, "")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating message: %v", err)
	}

	// Calculate first_response_time_secs.
	conv, _ := h.store.GetConversation(ctx, req.ConversationId)
	if conv != nil && conv.FirstResponseTimeSecs == 0 {
		secs := int32(time.Since(conv.CreatedAt).Seconds())
		if secs < 1 {
			secs = 1
		}
		if err := h.store.SetFirstResponseTime(ctx, req.ConversationId, secs); err != nil {
			h.log.Error().Err(err).Str("conversation_id", req.ConversationId).Msg("failed to set first response time")
		}
	}

	// Update last_message_at.
	preview := req.Body
	if len(preview) > 100 {
		preview = preview[:100]
	}
	_ = h.store.UpdateLastMessage(ctx, req.ConversationId, preview)

	// Publish ManualSendTask to NATS.
	if h.js != nil && conv != nil {
		waNum, _ := h.store.GetConversationWaNumber(ctx, conv.WaNumberID)
		if waNum != nil {
			// Find contact to get recipient JID.
			contact, _ := h.store.GetConversationContact(ctx, conv.ContactID)
			recipientJID := ""
			if contact != nil {
				// Strip '+' prefix — WhatsApp JIDs use bare country code (e.g. 628xxx, not +628xxx).
				phone := strings.TrimPrefix(contact.Phone, "+")
				recipientJID = phone + "@s.whatsapp.net"
			}

			eventID := uuid.New().String()
			task := &hermesv1.ManualSendTask{
				Meta: &hermesv1.EventMeta{
					EventId:   eventID,
					TenantId:  waNum.TenantID,
					Timestamp: timestamppb.Now(),
					Source:    "hermes-inbox",
				},
				ConversationId: req.ConversationId,
				MessageId:      msg.ID,
				WaNumberId:     conv.WaNumberID,
				RecipientJid:   recipientJID,
				ContentType:    req.ContentType,
				Body:           req.Body,
				MediaUrl:       req.MediaUrl,
				SenderUserId:   req.SenderUserId,
				IdempotencyKey: msg.ID,
			}

			data, merr := proto.Marshal(task)
			if merr == nil {
				subject := "hermes.wa.send.manual." + waNum.TenantID
				if _, perr := h.js.Publish(subject, data, natsgo.MsgId(eventID)); perr != nil {
					h.log.Error().Err(perr).Str("subject", subject).Msg("failed to publish ManualSendTask")
				}
			}
		}
	}

	return &hermesv1.InboxSendMessageResponse{
		Message: messageRowToProto(msg),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 8: SearchMessages
// ---------------------------------------------------------------------------

func (h *Handler) SearchMessages(ctx context.Context, req *hermesv1.InboxSearchMessagesRequest) (*hermesv1.InboxSearchMessagesResponse, error) {
	if req.WorkspaceId == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	if req.Query == "" {
		return nil, status.Error(codes.InvalidArgument, "query is required")
	}

	page, pageSize := normalizePage(req.Pagination)

	var fromDate, toDate *time.Time
	if req.FromDate != nil {
		t := req.FromDate.AsTime()
		fromDate = &t
	}
	if req.ToDate != nil {
		t := req.ToDate.AsTime()
		toDate = &t
	}

	hits, total, err := h.store.SearchMessages(ctx, req.WorkspaceId, req.Query, req.ConversationId, fromDate, toDate, page, pageSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "searching messages: %v", err)
	}

	searchHits := make([]*hermesv1.InboxSearchHit, 0, len(hits))
	for _, h := range hits {
		searchHits = append(searchHits, &hermesv1.InboxSearchHit{
			Message:        messageRowToProto(&h.MessageRow),
			ConversationId: h.ConversationID,
			ContactName:    h.ContactName,
			ContactPhone:   h.ContactPhone,
			Highlight:      h.Highlight,
		})
	}

	return &hermesv1.InboxSearchMessagesResponse{
		Hits:       searchHits,
		Pagination: pageResponse(total, page, pageSize),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 9: GetContactCampaignHistory
// ---------------------------------------------------------------------------

func (h *Handler) GetContactCampaignHistory(ctx context.Context, req *hermesv1.InboxGetContactCampaignHistoryRequest) (*hermesv1.InboxGetContactCampaignHistoryResponse, error) {
	if req.ContactId == "" {
		return nil, status.Error(codes.InvalidArgument, "contact_id is required")
	}

	page, pageSize := normalizePage(req.Pagination)
	rows, total, err := h.store.GetContactCampaignHistory(ctx, req.ContactId, page, pageSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting campaign history: %v", err)
	}

	campaigns := make([]*hermesv1.InboxContactCampaignSummary, 0, len(rows))
	for _, r := range rows {
		cs := &hermesv1.InboxContactCampaignSummary{
			CampaignId:   r.CampaignID,
			CampaignName: r.CampaignName,
			TemplateName: r.TemplateName,
			ResolvedBody: r.ResolvedBody,
			Status:       strToContactSendStatus(r.Status),
		}
		if r.TemplateID != nil {
			cs.TemplateId = *r.TemplateID
		}
		if r.SentAt != nil {
			cs.SentAt = timestamppb.New(*r.SentAt)
		}
		if r.DeliveredAt != nil {
			cs.DeliveredAt = timestamppb.New(*r.DeliveredAt)
		}
		campaigns = append(campaigns, cs)
	}

	return &hermesv1.InboxGetContactCampaignHistoryResponse{
		Campaigns:  campaigns,
		Pagination: pageResponse(total, page, pageSize),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 10: CreateCannedResponse
// ---------------------------------------------------------------------------

func (h *Handler) CreateCannedResponse(ctx context.Context, req *hermesv1.InboxCreateCannedResponseRequest) (*hermesv1.InboxCreateCannedResponseResponse, error) {
	if req.WorkspaceId == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	if req.Shortcut == "" {
		return nil, status.Error(codes.InvalidArgument, "shortcut is required")
	}
	if req.Body == "" {
		return nil, status.Error(codes.InvalidArgument, "body is required")
	}

	var createdBy *string
	if req.CreatedBy != "" {
		createdBy = &req.CreatedBy
	}

	row, err := h.store.CreateCannedResponse(ctx, req.WorkspaceId, req.Shortcut, req.Body, createdBy)
	if err != nil {
		if err == ErrDuplicateShortcut {
			return nil, status.Error(codes.AlreadyExists, "shortcut already exists in this workspace")
		}
		return nil, status.Errorf(codes.Internal, "creating canned response: %v", err)
	}

	return &hermesv1.InboxCreateCannedResponseResponse{
		CannedResponse: cannedRowToProto(row),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 11: GetCannedResponse
// ---------------------------------------------------------------------------

func (h *Handler) GetCannedResponse(ctx context.Context, req *hermesv1.InboxGetCannedResponseRequest) (*hermesv1.InboxGetCannedResponseResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.GetCannedResponse(ctx, req.Id)
	if err != nil {
		if err == ErrNotFound {
			return nil, status.Error(codes.NotFound, "canned response not found")
		}
		return nil, status.Errorf(codes.Internal, "getting canned response: %v", err)
	}

	return &hermesv1.InboxGetCannedResponseResponse{
		CannedResponse: cannedRowToProto(row),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 12: ListCannedResponses
// ---------------------------------------------------------------------------

func (h *Handler) ListCannedResponses(ctx context.Context, req *hermesv1.InboxListCannedResponsesRequest) (*hermesv1.InboxListCannedResponsesResponse, error) {
	if req.WorkspaceId == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}

	page, pageSize := normalizePage(req.Pagination)
	rows, total, err := h.store.ListCannedResponses(ctx, req.WorkspaceId, req.Search, page, pageSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing canned responses: %v", err)
	}

	crs := make([]*hermesv1.CannedResponse, 0, len(rows))
	for _, r := range rows {
		crs = append(crs, cannedRowToProto(r))
	}

	return &hermesv1.InboxListCannedResponsesResponse{
		CannedResponses: crs,
		Pagination:      pageResponse(total, page, pageSize),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 13: UpdateCannedResponse
// ---------------------------------------------------------------------------

func (h *Handler) UpdateCannedResponse(ctx context.Context, req *hermesv1.InboxUpdateCannedResponseRequest) (*hermesv1.InboxUpdateCannedResponseResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.UpdateCannedResponse(ctx, req.Id, req.Shortcut, req.Body)
	if err != nil {
		if err == ErrNotFound {
			return nil, status.Error(codes.NotFound, "canned response not found")
		}
		if err == ErrDuplicateShortcut {
			return nil, status.Error(codes.AlreadyExists, "shortcut already exists in this workspace")
		}
		return nil, status.Errorf(codes.Internal, "updating canned response: %v", err)
	}

	return &hermesv1.InboxUpdateCannedResponseResponse{
		CannedResponse: cannedRowToProto(row),
	}, nil
}

// ---------------------------------------------------------------------------
// RPC 14: DeleteCannedResponse
// ---------------------------------------------------------------------------

func (h *Handler) DeleteCannedResponse(ctx context.Context, req *hermesv1.InboxDeleteCannedResponseRequest) (*hermesv1.InboxDeleteCannedResponseResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	if err := h.store.DeleteCannedResponse(ctx, req.Id); err != nil {
		if err == ErrNotFound {
			return nil, status.Error(codes.NotFound, "canned response not found")
		}
		return nil, status.Errorf(codes.Internal, "deleting canned response: %v", err)
	}

	return &hermesv1.InboxDeleteCannedResponseResponse{}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func ptrToStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
