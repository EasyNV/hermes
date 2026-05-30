package handler

import (
	"context"
	"encoding/json"
	"errors"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/campaign/spintax"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Engine is the interface for the campaign dispatch engine.
// Defined here to avoid an import cycle with the engine package.
type Engine interface {
	Start(campaignID, workspaceID, tenantID string) error
	Stop(campaignID string)
	IsRunning(campaignID string) bool
}

// Handler implements the HermesCampaignServer gRPC interface.
type Handler struct {
	hermesv1.UnimplementedHermesCampaignServer
	store  Store
	engine Engine
	log    zerolog.Logger
}

func New(store Store, engine Engine, log zerolog.Logger) *Handler {
	return &Handler{store: store, engine: engine, log: log}
}

// ---------------------------------------------------------------------------
// Proto ↔ DB conversion helpers
// ---------------------------------------------------------------------------

func campaignStatusToStr(s hermesv1.CampaignStatus) string {
	switch s {
	case hermesv1.CampaignStatus_CAMPAIGN_STATUS_DRAFT:
		return "draft"
	case hermesv1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED:
		return "scheduled"
	case hermesv1.CampaignStatus_CAMPAIGN_STATUS_RUNNING:
		return "running"
	case hermesv1.CampaignStatus_CAMPAIGN_STATUS_PAUSED:
		return "paused"
	case hermesv1.CampaignStatus_CAMPAIGN_STATUS_COMPLETED:
		return "completed"
	case hermesv1.CampaignStatus_CAMPAIGN_STATUS_CANCELLED:
		return "cancelled"
	default:
		return ""
	}
}

func strToCampaignStatus(s string) hermesv1.CampaignStatus {
	switch s {
	case "draft":
		return hermesv1.CampaignStatus_CAMPAIGN_STATUS_DRAFT
	case "scheduled":
		return hermesv1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED
	case "running":
		return hermesv1.CampaignStatus_CAMPAIGN_STATUS_RUNNING
	case "paused":
		return hermesv1.CampaignStatus_CAMPAIGN_STATUS_PAUSED
	case "completed":
		return hermesv1.CampaignStatus_CAMPAIGN_STATUS_COMPLETED
	case "cancelled":
		return hermesv1.CampaignStatus_CAMPAIGN_STATUS_CANCELLED
	default:
		return hermesv1.CampaignStatus_CAMPAIGN_STATUS_UNSPECIFIED
	}
}

func contactSendStatusToStr(s hermesv1.ContactSendStatus) string {
	switch s {
	case hermesv1.ContactSendStatus_CONTACT_SEND_STATUS_PENDING:
		return "pending"
	case hermesv1.ContactSendStatus_CONTACT_SEND_STATUS_SENT:
		return "sent"
	case hermesv1.ContactSendStatus_CONTACT_SEND_STATUS_DELIVERED:
		return "delivered"
	case hermesv1.ContactSendStatus_CONTACT_SEND_STATUS_FAILED:
		return "failed"
	case hermesv1.ContactSendStatus_CONTACT_SEND_STATUS_SKIPPED:
		return "skipped"
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

func rotationStrategyToStr(s hermesv1.RotationStrategy) string {
	switch s {
	case hermesv1.RotationStrategy_ROTATION_STRATEGY_ROUND_ROBIN:
		return "round_robin"
	case hermesv1.RotationStrategy_ROTATION_STRATEGY_LEAST_USED:
		return "least_used"
	default:
		return "round_robin"
	}
}

func strToRotationStrategy(s string) hermesv1.RotationStrategy {
	switch s {
	case "round_robin":
		return hermesv1.RotationStrategy_ROTATION_STRATEGY_ROUND_ROBIN
	case "least_used":
		return hermesv1.RotationStrategy_ROTATION_STRATEGY_LEAST_USED
	default:
		return hermesv1.RotationStrategy_ROTATION_STRATEGY_UNSPECIFIED
	}
}

func strToWaNumberStatus(s string) hermesv1.WaNumberStatus {
	switch s {
	case "active":
		return hermesv1.WaNumberStatus_WA_NUMBER_STATUS_ACTIVE
	case "banned":
		return hermesv1.WaNumberStatus_WA_NUMBER_STATUS_BANNED
	case "disconnected":
		return hermesv1.WaNumberStatus_WA_NUMBER_STATUS_DISCONNECTED
	case "cooldown":
		return hermesv1.WaNumberStatus_WA_NUMBER_STATUS_COOLDOWN
	default:
		return hermesv1.WaNumberStatus_WA_NUMBER_STATUS_UNSPECIFIED
	}
}

func campaignRowToProto(r *CampaignRow) *hermesv1.Campaign {
	c := &hermesv1.Campaign{
		Id:                r.ID,
		WorkspaceId:       r.WorkspaceID,
		TemplateId:        r.TemplateID,
		Name:              r.Name,
		Status:            strToCampaignStatus(r.Status),
		DailyCapPerNum:    r.DailyCapPerNum,
		BanPauseThreshold: r.BanPauseThreshold,
		RotationStrategy:  strToRotationStrategy(r.RotationStrategy),
		DelayMinMs:        r.DelayMinMs,
		DelayMaxMs:        r.DelayMaxMs,
		TotalContacts:     r.TotalContacts,
		SentCount:         r.SentCount,
		FailedCount:       r.FailedCount,
		RepliedCount:      r.RepliedCount,
		BannedCount:       r.BannedCount,
		CreatedBy:         r.CreatedBy,
		CreatedAt:         timestamppb.New(r.CreatedAt),
		Channel:           r.Channel,
	}
	if r.ScheduleAt != nil {
		c.ScheduleAt = timestamppb.New(*r.ScheduleAt)
	}
	if r.StartedAt != nil {
		c.StartedAt = timestamppb.New(*r.StartedAt)
	}
	if r.CompletedAt != nil {
		c.CompletedAt = timestamppb.New(*r.CompletedAt)
	}
	return c
}

func templateRowToProto(r *TemplateRow) *hermesv1.Template {
	return &hermesv1.Template{
		Id:          r.ID,
		WorkspaceId: r.WorkspaceID,
		Name:        r.Name,
		Body:        r.Body,
		MediaUrl:    r.MediaURL,
		MediaType:   r.MediaType,
		Variables:   r.VariableNames(),
		CreatedBy:   r.CreatedBy,
		CreatedAt:   timestamppb.New(r.CreatedAt),
	}
}

func campaignNumberRowToProto(r *CampaignNumberRow) *hermesv1.CampaignNumber {
	return &hermesv1.CampaignNumber{
		CampaignId: r.CampaignID,
		WaNumberId: r.WaNumberID,
		Status:     strToWaNumberStatus(r.Status),
		SentCount:  r.SentCount,
		FailedCount: r.FailedCount,
	}
}

func paginationDefaults(p *hermesv1.PageRequest) (page, pageSize int32) {
	page = p.GetPage()
	pageSize = p.GetPageSize()
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return
}

func totalPages(total int64, pageSize int32) int32 {
	if total <= 0 {
		return 0
	}
	return int32((total + int64(pageSize) - 1) / int64(pageSize))
}

// ---------------------------------------------------------------------------
// Template RPCs
// ---------------------------------------------------------------------------

func (h *Handler) CreateTemplate(ctx context.Context, req *hermesv1.TemplateCreateRequest) (*hermesv1.TemplateCreateResponse, error) {
	if req.WorkspaceId == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.Body == "" {
		return nil, status.Error(codes.InvalidArgument, "body is required")
	}

	vars := spintax.ExtractVariables(req.Body)
	varsJSON, _ := json.Marshal(vars)

	row, err := h.store.CreateTemplate(ctx, req.WorkspaceId, req.Name, req.Body, req.MediaUrl, req.MediaType, req.CreatedBy, varsJSON)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating template: %v", err)
	}

	return &hermesv1.TemplateCreateResponse{Template: templateRowToProto(row)}, nil
}

func (h *Handler) GetTemplate(ctx context.Context, req *hermesv1.TemplateGetRequest) (*hermesv1.TemplateGetResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.GetTemplate(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting template: %v", err)
	}
	if row == nil {
		return nil, status.Error(codes.NotFound, "template not found")
	}

	return &hermesv1.TemplateGetResponse{Template: templateRowToProto(row)}, nil
}

func (h *Handler) ListTemplates(ctx context.Context, req *hermesv1.TemplateListRequest) (*hermesv1.TemplateListResponse, error) {
	if req.WorkspaceId == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}

	page, pageSize := paginationDefaults(req.Pagination)
	rows, total, err := h.store.ListTemplates(ctx, req.WorkspaceId, req.Search, page, pageSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing templates: %v", err)
	}

	templates := make([]*hermesv1.Template, 0, len(rows))
	for _, r := range rows {
		templates = append(templates, templateRowToProto(r))
	}

	return &hermesv1.TemplateListResponse{
		Templates: templates,
		Pagination: &hermesv1.PageResponse{
			Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages(total, pageSize),
		},
	}, nil
}

func (h *Handler) UpdateTemplate(ctx context.Context, req *hermesv1.TemplateUpdateRequest) (*hermesv1.TemplateUpdateResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	used, err := h.store.TemplateUsedByRunningCampaign(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "checking template usage: %v", err)
	}
	if used {
		return nil, status.Error(codes.FailedPrecondition, "cannot update template used by a running campaign")
	}

	var varsJSON []byte
	if req.Body != "" {
		vars := spintax.ExtractVariables(req.Body)
		varsJSON, _ = json.Marshal(vars)
	}

	row, err := h.store.UpdateTemplate(ctx, req.Id, req.Name, req.Body, req.MediaUrl, req.MediaType, varsJSON)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "updating template: %v", err)
	}
	if row == nil {
		return nil, status.Error(codes.NotFound, "template not found")
	}

	return &hermesv1.TemplateUpdateResponse{Template: templateRowToProto(row)}, nil
}

func (h *Handler) DeleteTemplate(ctx context.Context, req *hermesv1.TemplateDeleteRequest) (*hermesv1.TemplateDeleteResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	used, err := h.store.TemplateUsedByActiveCampaign(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "checking template usage: %v", err)
	}
	if used {
		return nil, status.Error(codes.FailedPrecondition, "cannot delete template used by active campaigns")
	}

	if err := h.store.DeleteTemplate(ctx, req.Id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, status.Error(codes.NotFound, "template not found")
		}
		return nil, status.Errorf(codes.Internal, "deleting template: %v", err)
	}

	return &hermesv1.TemplateDeleteResponse{}, nil
}

func (h *Handler) PreviewTemplate(ctx context.Context, req *hermesv1.TemplatePreviewRequest) (*hermesv1.TemplatePreviewResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.GetTemplate(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting template: %v", err)
	}
	if row == nil {
		return nil, status.Error(codes.NotFound, "template not found")
	}

	resolved := spintax.Resolve(row.Body)
	resolved = spintax.SubstituteVariables(resolved, req.Variables)

	return &hermesv1.TemplatePreviewResponse{
		ResolvedBody: resolved,
		MediaUrl:     row.MediaURL,
	}, nil
}

// ---------------------------------------------------------------------------
// Campaign CRUD RPCs
// ---------------------------------------------------------------------------

func (h *Handler) CreateCampaign(ctx context.Context, req *hermesv1.CampaignCreateRequest) (*hermesv1.CampaignCreateResponse, error) {
	if req.WorkspaceId == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}
	if req.TemplateId == "" {
		return nil, status.Error(codes.InvalidArgument, "template_id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	// ─── Chunk 8: channel validation ──────────────────────────────
	//
	// Defaults: empty channel → 'wa' (wire-compat with pre-chunk-8
	// clients). Anything other than 'wa'/'mbs' is rejected.
	//
	// Mutual exclusion: callers MUST set wa_number_ids xor
	// mbs_session_uids matching their declared channel. Both empty is
	// allowed (DRAFT campaign with no senders yet; UpdateCampaignNumbers
	// can attach later). Setting the OTHER channel's senders is a
	// programmer error and gets InvalidArgument.
	channel := req.Channel
	if channel == "" {
		channel = "wa"
	}
	if channel != "wa" && channel != "mbs" {
		return nil, status.Errorf(codes.InvalidArgument, "channel must be 'wa' or 'mbs', got %q", channel)
	}
	if channel == "wa" && len(req.MbsSessionUids) > 0 {
		return nil, status.Error(codes.InvalidArgument, "mbs_session_uids must be empty when channel='wa'")
	}
	if channel == "mbs" && len(req.WaNumberIds) > 0 {
		return nil, status.Error(codes.InvalidArgument, "wa_number_ids must be empty when channel='mbs'")
	}

	// Verify template exists.
	tmpl, err := h.store.GetTemplate(ctx, req.TemplateId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "checking template: %v", err)
	}
	if tmpl == nil {
		return nil, status.Error(codes.NotFound, "template not found")
	}

	row := &CampaignRow{
		WorkspaceID:       req.WorkspaceId,
		TemplateID:        req.TemplateId,
		Name:              req.Name,
		DailyCapPerNum:    req.DailyCapPerNum,
		BanPauseThreshold: req.BanPauseThreshold,
		RotationStrategy:  rotationStrategyToStr(req.RotationStrategy),
		DelayMinMs:        req.DelayMinMs,
		DelayMaxMs:        req.DelayMaxMs,
		CreatedBy:         req.CreatedBy,
		Channel:           channel,
	}
	if req.ScheduleAt != nil {
		t := req.ScheduleAt.AsTime()
		row.ScheduleAt = &t
	}
	if row.DelayMinMs <= 0 {
		row.DelayMinMs = 3000
	}
	if row.DelayMaxMs <= 0 {
		row.DelayMaxMs = 15000
	}
	if row.DelayMaxMs < row.DelayMinMs {
		row.DelayMaxMs = row.DelayMinMs
	}

	campaign, err := h.store.CreateCampaign(ctx, row)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating campaign: %v", err)
	}

	// Add initial senders + contacts if provided. Channel selects which
	// add path runs; the validation above ensures the OTHER channel's
	// field is empty so only one branch fires.
	if len(req.WaNumberIds) > 0 {
		if err := h.store.AddCampaignNumbers(ctx, campaign.ID, req.WaNumberIds); err != nil {
			return nil, status.Errorf(codes.Internal, "adding numbers: %v", err)
		}
	}
	if len(req.MbsSessionUids) > 0 {
		if err := h.store.AddCampaignMbsSessions(ctx, campaign.ID, req.MbsSessionUids); err != nil {
			return nil, status.Errorf(codes.Internal, "adding mbs sessions: %v", err)
		}
	}
	if len(req.ContactIds) > 0 {
		if _, err := h.store.AddCampaignContacts(ctx, campaign.ID, req.ContactIds); err != nil {
			return nil, status.Errorf(codes.Internal, "adding contacts: %v", err)
		}
		if err := h.store.UpdateTotalContacts(ctx, campaign.ID); err != nil {
			return nil, status.Errorf(codes.Internal, "updating total contacts: %v", err)
		}
	}

	// Re-fetch to get updated total_contacts.
	campaign, err = h.store.GetCampaign(ctx, campaign.ID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "re-fetching campaign: %v", err)
	}

	return &hermesv1.CampaignCreateResponse{Campaign: campaignRowToProto(campaign)}, nil
}

func (h *Handler) GetCampaign(ctx context.Context, req *hermesv1.CampaignGetRequest) (*hermesv1.CampaignGetResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	campaign, err := h.store.GetCampaign(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting campaign: %v", err)
	}
	if campaign == nil {
		return nil, status.Error(codes.NotFound, "campaign not found")
	}

	// Fetch numbers.
	nums, _, err := h.store.ListCampaignNumbers(ctx, req.Id, 1, 200)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing campaign numbers: %v", err)
	}
	protoNums := make([]*hermesv1.CampaignNumber, 0, len(nums))
	for _, n := range nums {
		protoNums = append(protoNums, campaignNumberRowToProto(n))
	}

	// Fetch template.
	tmpl, err := h.store.GetTemplate(ctx, campaign.TemplateID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting template: %v", err)
	}

	resp := &hermesv1.CampaignGetResponse{
		Campaign: campaignRowToProto(campaign),
		Numbers:  protoNums,
	}
	if tmpl != nil {
		resp.Template = templateRowToProto(tmpl)
	}

	return resp, nil
}

func (h *Handler) ListCampaigns(ctx context.Context, req *hermesv1.CampaignListRequest) (*hermesv1.CampaignListResponse, error) {
	if req.WorkspaceId == "" {
		return nil, status.Error(codes.InvalidArgument, "workspace_id is required")
	}

	page, pageSize := paginationDefaults(req.Pagination)
	statusFilter := campaignStatusToStr(req.Status)

	rows, total, err := h.store.ListCampaigns(ctx, req.WorkspaceId, statusFilter, page, pageSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing campaigns: %v", err)
	}

	campaigns := make([]*hermesv1.Campaign, 0, len(rows))
	for _, r := range rows {
		campaigns = append(campaigns, campaignRowToProto(r))
	}

	return &hermesv1.CampaignListResponse{
		Campaigns: campaigns,
		Pagination: &hermesv1.PageResponse{
			Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages(total, pageSize),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Campaign Lifecycle RPCs
// ---------------------------------------------------------------------------

func (h *Handler) StartCampaign(ctx context.Context, req *hermesv1.CampaignStartRequest) (*hermesv1.CampaignStartResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	campaign, err := h.store.GetCampaign(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting campaign: %v", err)
	}
	if campaign == nil {
		return nil, status.Error(codes.NotFound, "campaign not found")
	}

	if campaign.Status != "draft" && campaign.Status != "scheduled" {
		return nil, status.Errorf(codes.FailedPrecondition, "campaign status is %s, must be draft or scheduled", campaign.Status)
	}

	// Validate has senders and contacts. Branch on channel — chunk 9.
	// 'wa' (or empty for wire-compat) checks campaign_senders WHERE sender_kind='wa'.
	// 'mbs' checks campaign_senders WHERE sender_kind='mbs'.
	var senderCount int32
	switch campaign.Channel {
	case "", "wa":
		senderCount, err = h.store.CountCampaignNumbers(ctx, req.Id)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "counting numbers: %v", err)
		}
		if senderCount == 0 {
			return nil, status.Error(codes.FailedPrecondition, "campaign has no assigned numbers")
		}
	case "mbs":
		senderCount, err = h.store.CountCampaignMbsSessions(ctx, req.Id)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "counting mbs sessions: %v", err)
		}
		if senderCount == 0 {
			return nil, status.Error(codes.FailedPrecondition, "campaign has no assigned mbs sessions")
		}
	default:
		return nil, status.Errorf(codes.FailedPrecondition, "unknown campaign channel %q", campaign.Channel)
	}

	contactCount, err := h.store.CountCampaignContacts(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "counting contacts: %v", err)
	}
	if contactCount == 0 {
		return nil, status.Error(codes.FailedPrecondition, "campaign has no assigned contacts")
	}

	// Transition to RUNNING.
	campaign, err = h.store.UpdateCampaignStatus(ctx, req.Id, "running", true, false)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "updating campaign status: %v", err)
	}

	// Populate contact allowlist for this workspace (so inbox accepts replies).
	if added, alErr := h.store.PopulateAllowlistFromCampaign(ctx, req.Id, campaign.WorkspaceID); alErr != nil {
		h.log.Error().Err(alErr).Str("campaign_id", req.Id).Msg("failed to populate allowlist")
	} else {
		h.log.Info().Int64("added", added).Str("campaign_id", req.Id).Msg("allowlist populated from campaign contacts")
	}

	// Start dispatch engine.
	tenantID, err := h.store.GetWorkspaceTenantID(ctx, campaign.WorkspaceID)
	if err != nil {
		h.log.Error().Err(err).Str("campaign_id", req.Id).Msg("failed to resolve tenant_id, engine may fail to publish events")
	}
	if err := h.engine.Start(req.Id, campaign.WorkspaceID, tenantID); err != nil {
		h.log.Error().Err(err).Str("campaign_id", req.Id).Msg("failed to start engine")
	}

	return &hermesv1.CampaignStartResponse{Campaign: campaignRowToProto(campaign)}, nil
}

func (h *Handler) PauseCampaign(ctx context.Context, req *hermesv1.CampaignPauseRequest) (*hermesv1.CampaignPauseResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	campaign, err := h.store.GetCampaign(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting campaign: %v", err)
	}
	if campaign == nil {
		return nil, status.Error(codes.NotFound, "campaign not found")
	}

	if campaign.Status != "running" {
		return nil, status.Errorf(codes.FailedPrecondition, "campaign status is %s, must be running", campaign.Status)
	}

	h.engine.Stop(req.Id)

	campaign, err = h.store.UpdateCampaignStatus(ctx, req.Id, "paused", false, false)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "updating campaign status: %v", err)
	}

	return &hermesv1.CampaignPauseResponse{Campaign: campaignRowToProto(campaign)}, nil
}

func (h *Handler) ResumeCampaign(ctx context.Context, req *hermesv1.CampaignResumeRequest) (*hermesv1.CampaignResumeResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	campaign, err := h.store.GetCampaign(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting campaign: %v", err)
	}
	if campaign == nil {
		return nil, status.Error(codes.NotFound, "campaign not found")
	}

	if campaign.Status != "paused" {
		return nil, status.Errorf(codes.FailedPrecondition, "campaign status is %s, must be paused", campaign.Status)
	}

	campaign, err = h.store.UpdateCampaignStatus(ctx, req.Id, "running", false, false)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "updating campaign status: %v", err)
	}

	tenantID, _ := h.store.GetWorkspaceTenantID(ctx, campaign.WorkspaceID)
	if err := h.engine.Start(req.Id, campaign.WorkspaceID, tenantID); err != nil {
		h.log.Error().Err(err).Str("campaign_id", req.Id).Msg("failed to start engine on resume")
	}

	return &hermesv1.CampaignResumeResponse{Campaign: campaignRowToProto(campaign)}, nil
}

func (h *Handler) CancelCampaign(ctx context.Context, req *hermesv1.CampaignCancelRequest) (*hermesv1.CampaignCancelResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	campaign, err := h.store.GetCampaign(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting campaign: %v", err)
	}
	if campaign == nil {
		return nil, status.Error(codes.NotFound, "campaign not found")
	}

	if campaign.Status == "completed" || campaign.Status == "cancelled" {
		return nil, status.Errorf(codes.FailedPrecondition, "campaign is already %s", campaign.Status)
	}

	h.engine.Stop(req.Id)

	// Skip remaining pending contacts.
	if _, err := h.store.SkipPendingContacts(ctx, req.Id); err != nil {
		h.log.Error().Err(err).Str("campaign_id", req.Id).Msg("failed to skip pending contacts")
	}

	campaign, err = h.store.UpdateCampaignStatus(ctx, req.Id, "cancelled", false, true)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "updating campaign status: %v", err)
	}

	return &hermesv1.CampaignCancelResponse{Campaign: campaignRowToProto(campaign)}, nil
}

// ---------------------------------------------------------------------------
// Campaign Numbers & Contacts RPCs
// ---------------------------------------------------------------------------

func (h *Handler) UpdateCampaignNumbers(ctx context.Context, req *hermesv1.CampaignUpdateNumbersRequest) (*hermesv1.CampaignUpdateNumbersResponse, error) {
	if req.CampaignId == "" {
		return nil, status.Error(codes.InvalidArgument, "campaign_id is required")
	}

	campaign, err := h.store.GetCampaign(ctx, req.CampaignId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting campaign: %v", err)
	}
	if campaign == nil {
		return nil, status.Error(codes.NotFound, "campaign not found")
	}

	if campaign.Status != "draft" && campaign.Status != "paused" {
		return nil, status.Errorf(codes.FailedPrecondition, "campaign status is %s, must be draft or paused", campaign.Status)
	}

	if len(req.AddWaNumberIds) > 0 {
		if err := h.store.AddCampaignNumbers(ctx, req.CampaignId, req.AddWaNumberIds); err != nil {
			return nil, status.Errorf(codes.Internal, "adding numbers: %v", err)
		}
	}
	if len(req.RemoveWaNumberIds) > 0 {
		if err := h.store.RemoveCampaignNumbers(ctx, req.CampaignId, req.RemoveWaNumberIds); err != nil {
			return nil, status.Errorf(codes.Internal, "removing numbers: %v", err)
		}
	}

	// Re-fetch campaign and numbers.
	campaign, _ = h.store.GetCampaign(ctx, req.CampaignId)
	nums, _, err := h.store.ListCampaignNumbers(ctx, req.CampaignId, 1, 200)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing numbers: %v", err)
	}

	protoNums := make([]*hermesv1.CampaignNumber, 0, len(nums))
	for _, n := range nums {
		protoNums = append(protoNums, campaignNumberRowToProto(n))
	}

	return &hermesv1.CampaignUpdateNumbersResponse{
		Campaign: campaignRowToProto(campaign),
		Numbers:  protoNums,
	}, nil
}

func (h *Handler) UpdateCampaignContacts(ctx context.Context, req *hermesv1.CampaignUpdateContactsRequest) (*hermesv1.CampaignUpdateContactsResponse, error) {
	if req.CampaignId == "" {
		return nil, status.Error(codes.InvalidArgument, "campaign_id is required")
	}

	campaign, err := h.store.GetCampaign(ctx, req.CampaignId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting campaign: %v", err)
	}
	if campaign == nil {
		return nil, status.Error(codes.NotFound, "campaign not found")
	}

	if campaign.Status != "draft" && campaign.Status != "paused" {
		return nil, status.Errorf(codes.FailedPrecondition, "campaign status is %s, must be draft or paused", campaign.Status)
	}

	if len(req.AddContactIds) > 0 {
		if _, err := h.store.AddCampaignContacts(ctx, req.CampaignId, req.AddContactIds); err != nil {
			return nil, status.Errorf(codes.Internal, "adding contacts: %v", err)
		}
	}
	if len(req.RemoveContactIds) > 0 {
		if _, err := h.store.RemoveCampaignContacts(ctx, req.CampaignId, req.RemoveContactIds); err != nil {
			return nil, status.Errorf(codes.Internal, "removing contacts: %v", err)
		}
	}

	// Update total_contacts count.
	if err := h.store.UpdateTotalContacts(ctx, req.CampaignId); err != nil {
		return nil, status.Errorf(codes.Internal, "updating total contacts: %v", err)
	}

	totalContacts, err := h.store.CountCampaignContacts(ctx, req.CampaignId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "counting contacts: %v", err)
	}

	campaign, _ = h.store.GetCampaign(ctx, req.CampaignId)

	return &hermesv1.CampaignUpdateContactsResponse{
		Campaign:      campaignRowToProto(campaign),
		TotalContacts: totalContacts,
	}, nil
}

func (h *Handler) ListCampaignContacts(ctx context.Context, req *hermesv1.CampaignListContactsRequest) (*hermesv1.CampaignListContactsResponse, error) {
	if req.CampaignId == "" {
		return nil, status.Error(codes.InvalidArgument, "campaign_id is required")
	}

	page, pageSize := paginationDefaults(req.Pagination)
	statusFilter := contactSendStatusToStr(req.Status)

	rows, total, err := h.store.ListCampaignContacts(ctx, req.CampaignId, statusFilter, page, pageSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing campaign contacts: %v", err)
	}

	contacts := make([]*hermesv1.CampaignContactRow, 0, len(rows))
	for _, r := range rows {
		cc := &hermesv1.CampaignContact{
			CampaignId: r.CampaignID,
			ContactId:  r.ContactID,
			Status:     strToContactSendStatus(r.Status),
			Error:      r.Error,
		}
		if r.WaNumberID != nil {
			cc.WaNumberId = *r.WaNumberID
		}
		if r.SentAt != nil {
			cc.SentAt = timestamppb.New(*r.SentAt)
		}
		if r.DeliveredAt != nil {
			cc.DeliveredAt = timestamppb.New(*r.DeliveredAt)
		}
		if r.FailedAt != nil {
			cc.FailedAt = timestamppb.New(*r.FailedAt)
		}

		contacts = append(contacts, &hermesv1.CampaignContactRow{
			CampaignContact: cc,
			ContactName:     r.ContactName,
			ContactPhone:    r.ContactPhone,
		})
	}

	return &hermesv1.CampaignListContactsResponse{
		Contacts: contacts,
		Pagination: &hermesv1.PageResponse{
			Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages(total, pageSize),
		},
	}, nil
}

func (h *Handler) ListCampaignNumbers(ctx context.Context, req *hermesv1.CampaignListNumbersRequest) (*hermesv1.CampaignListNumbersResponse, error) {
	if req.CampaignId == "" {
		return nil, status.Error(codes.InvalidArgument, "campaign_id is required")
	}

	page, pageSize := paginationDefaults(req.Pagination)
	rows, total, err := h.store.ListCampaignNumbers(ctx, req.CampaignId, page, pageSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing campaign numbers: %v", err)
	}

	numbers := make([]*hermesv1.CampaignNumber, 0, len(rows))
	for _, r := range rows {
		numbers = append(numbers, campaignNumberRowToProto(r))
	}

	return &hermesv1.CampaignListNumbersResponse{
		Numbers: numbers,
		Pagination: &hermesv1.PageResponse{
			Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages(total, pageSize),
		},
	}, nil
}
