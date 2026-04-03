package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/contacts/importer"
)

// Handler implements hermesv1.HermesContactsServer.
type Handler struct {
	hermesv1.UnimplementedHermesContactsServer
	store Store
	js    nats.JetStreamContext
	log   zerolog.Logger
}

// New creates a Handler. Pass nil for js to skip NATS event publishing (useful in tests).
func New(store Store, js nats.JetStreamContext, log zerolog.Logger) *Handler {
	return &Handler{store: store, js: js, log: log}
}

// -----------------------------------------------------------------------
// CreateContact
// -----------------------------------------------------------------------

func (h *Handler) CreateContact(ctx context.Context, req *hermesv1.ContactsCreateRequest) (*hermesv1.ContactsCreateResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.Phone == "" {
		return nil, status.Error(codes.InvalidArgument, "phone is required")
	}

	row, existed, err := h.store.CreateContact(ctx, req.TenantId, req.Phone, req.Name)
	if err != nil {
		h.log.Error().Err(err).Str("tenant_id", req.TenantId).Msg("failed to create contact")
		return nil, status.Errorf(codes.Internal, "failed to create contact: %v", err)
	}

	if !existed {
		if len(req.Tags) > 0 {
			if err := h.store.ReplaceTags(ctx, row.ID, req.Tags); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to set tags: %v", err)
			}
		}
		if len(req.CustomFields) > 0 {
			if err := h.store.MergeCustomFields(ctx, row.ID, req.CustomFields); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to set custom fields: %v", err)
			}
		}
	}

	contact, err := h.buildContact(ctx, row)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to build contact: %v", err)
	}
	return &hermesv1.ContactsCreateResponse{Contact: contact, AlreadyExisted: existed}, nil
}

// -----------------------------------------------------------------------
// ImportContacts
// -----------------------------------------------------------------------

func (h *Handler) ImportContacts(ctx context.Context, req *hermesv1.ContactsImportRequest) (*hermesv1.ContactsImportResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if len(req.CsvData) == 0 {
		return nil, status.Error(codes.InvalidArgument, "csv_data is required")
	}
	if req.ColumnMapping == nil || req.ColumnMapping["phone"] == "" {
		return nil, status.Error(codes.InvalidArgument, "column_mapping must include 'phone'")
	}

	dupStrategy := req.DuplicateStrategy
	if dupStrategy == hermesv1.ImportDuplicateStrategy_IMPORT_DUPLICATE_STRATEGY_UNSPECIFIED {
		dupStrategy = hermesv1.ImportDuplicateStrategy_IMPORT_DUPLICATE_STRATEGY_SKIP
	}

	resp := &hermesv1.ContactsImportResponse{}
	var bannedCount int32

	err := importer.ParseCSV(req.CsvData, req.ColumnMapping, req.DefaultTags, func(row importer.ParsedRow) {
		if row.Err != "" {
			resp.FailedCount++
			resp.Errors = append(resp.Errors, &hermesv1.ContactsImportError{
				Row:   int32(row.RowNum),
				Phone: row.Phone,
				Error: row.Err,
			})
			return
		}

		contact, existed, err := h.store.CreateContact(ctx, req.TenantId, row.Phone, row.Name)
		if err != nil {
			resp.FailedCount++
			resp.Errors = append(resp.Errors, &hermesv1.ContactsImportError{
				Row:   int32(row.RowNum),
				Phone: row.Phone,
				Error: fmt.Sprintf("db error: %v", err),
			})
			return
		}

		if existed {
			if dupStrategy == hermesv1.ImportDuplicateStrategy_IMPORT_DUPLICATE_STRATEGY_UPDATE {
				if row.Name != "" {
					if err := h.store.UpdateContactImport(ctx, contact.ID, row.Name); err != nil {
						h.log.Warn().Err(err).Str("contact_id", contact.ID).Msg("import: failed to update name")
					}
				}
				if len(row.Tags) > 0 {
					if err := h.store.ReplaceTags(ctx, contact.ID, row.Tags); err != nil {
						h.log.Warn().Err(err).Str("contact_id", contact.ID).Msg("import: failed to replace tags")
					}
				}
				if len(row.CustomFields) > 0 {
					if err := h.store.MergeCustomFields(ctx, contact.ID, row.CustomFields); err != nil {
						h.log.Warn().Err(err).Str("contact_id", contact.ID).Msg("import: failed to merge custom fields")
					}
				}
				resp.UpdatedCount++
			} else {
				resp.SkippedCount++
			}
		} else {
			if len(row.Tags) > 0 {
				if err := h.store.ReplaceTags(ctx, contact.ID, row.Tags); err != nil {
					h.log.Warn().Err(err).Str("contact_id", contact.ID).Msg("import: failed to set tags")
				}
			}
			if len(row.CustomFields) > 0 {
				if err := h.store.MergeCustomFields(ctx, contact.ID, row.CustomFields); err != nil {
					h.log.Warn().Err(err).Str("contact_id", contact.ID).Msg("import: failed to set custom fields")
				}
			}
			resp.ImportedCount++
		}

		if contact.IsBanned {
			bannedCount++
		}
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "CSV parse error: %v", err)
	}

	resp.BannedCount = bannedCount

	// Publish NATS event.
	if h.js != nil {
		h.publishImportDone(req, resp)
	}

	return resp, nil
}

func (h *Handler) publishImportDone(req *hermesv1.ContactsImportRequest, resp *hermesv1.ContactsImportResponse) {
	event := &hermesv1.ContactsImportDoneEvent{
		Meta: &hermesv1.EventMeta{
			EventId:   uuid.New().String(),
			TenantId:  req.TenantId,
			Timestamp: timestamppb.Now(),
			Source:    "hermes-contacts",
		},
		ImportedBy:    req.ImportedBy,
		Filename:      req.Filename,
		ImportedCount: resp.ImportedCount,
		SkippedCount:  resp.SkippedCount,
		UpdatedCount:  resp.UpdatedCount,
		FailedCount:   resp.FailedCount,
	}

	data, err := proto.Marshal(event)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to marshal import done event")
		return
	}

	subject := fmt.Sprintf("hermes.contacts.import.done.%s", req.TenantId)
	if _, err := h.js.Publish(subject, data, nats.MsgId(event.Meta.EventId)); err != nil {
		h.log.Error().Err(err).Str("subject", subject).Msg("failed to publish import done event")
	}
}

// -----------------------------------------------------------------------
// ListContacts
// -----------------------------------------------------------------------

func (h *Handler) ListContacts(ctx context.Context, req *hermesv1.ContactsListRequest) (*hermesv1.ContactsListResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	page, pageSize := normalizePagination(req.Pagination)

	rows, total, err := h.store.List(ctx, ListFilter{
		TenantID:     req.TenantId,
		Search:       req.Search,
		Tags:         req.Tags,
		FilterBanned: req.FilterBanned,
		IsBanned:     req.IsBanned,
		Page:         page,
		PageSize:     pageSize,
	})
	if err != nil {
		h.log.Error().Err(err).Str("tenant_id", req.TenantId).Msg("failed to list contacts")
		return nil, status.Errorf(codes.Internal, "failed to list contacts: %v", err)
	}

	pbContacts := make([]*hermesv1.Contact, 0, len(rows))
	for _, r := range rows {
		c, err := h.buildContact(ctx, r)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to build contact: %v", err)
		}
		pbContacts = append(pbContacts, c)
	}

	totalPages := int32(0)
	if pageSize > 0 {
		totalPages = int32((total + int64(pageSize) - 1) / int64(pageSize))
	}

	return &hermesv1.ContactsListResponse{
		Contacts: pbContacts,
		Pagination: &hermesv1.PageResponse{
			Total:      total,
			Page:       page,
			PageSize:   pageSize,
			TotalPages: totalPages,
		},
	}, nil
}

// -----------------------------------------------------------------------
// GetContact
// -----------------------------------------------------------------------

func (h *Handler) GetContact(ctx context.Context, req *hermesv1.ContactsGetRequest) (*hermesv1.ContactsGetResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	row, err := h.store.GetContactByID(ctx, req.Id)
	if errors.Is(err, ErrNotFound) {
		return nil, status.Error(codes.NotFound, "contact not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get contact: %v", err)
	}

	contact, err := h.buildContact(ctx, row)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to build contact: %v", err)
	}
	return &hermesv1.ContactsGetResponse{Contact: contact}, nil
}

// -----------------------------------------------------------------------
// GetContactByPhone
// -----------------------------------------------------------------------

func (h *Handler) GetContactByPhone(ctx context.Context, req *hermesv1.ContactsGetByPhoneRequest) (*hermesv1.ContactsGetByPhoneResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.Phone == "" {
		return nil, status.Error(codes.InvalidArgument, "phone is required")
	}

	row, err := h.store.GetContactByPhone(ctx, req.TenantId, req.Phone)
	if errors.Is(err, ErrNotFound) {
		return &hermesv1.ContactsGetByPhoneResponse{Found: false}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get contact: %v", err)
	}

	contact, err := h.buildContact(ctx, row)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to build contact: %v", err)
	}
	return &hermesv1.ContactsGetByPhoneResponse{Contact: contact, Found: true}, nil
}

// -----------------------------------------------------------------------
// UpdateContact
// -----------------------------------------------------------------------

func (h *Handler) UpdateContact(ctx context.Context, req *hermesv1.ContactsUpdateRequest) (*hermesv1.ContactsUpdateResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	current, err := h.store.GetContactByID(ctx, req.Id)
	if errors.Is(err, ErrNotFound) {
		return nil, status.Error(codes.NotFound, "contact not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get contact: %v", err)
	}

	// Apply only non-zero fields (proto3 default handling).
	name := current.Name
	if req.Name != "" {
		name = req.Name
	}
	phone := current.Phone
	if req.Phone != "" {
		phone = req.Phone
	}
	isBanned := current.IsBanned
	if req.IsBanned {
		isBanned = true
	}

	row, err := h.store.UpdateContact(ctx, req.Id, name, phone, isBanned)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update contact: %v", err)
	}

	if len(req.Tags) > 0 {
		if err := h.store.ReplaceTags(ctx, req.Id, req.Tags); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to replace tags: %v", err)
		}
	}
	if len(req.CustomFields) > 0 {
		if err := h.store.MergeCustomFields(ctx, req.Id, req.CustomFields); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to merge custom fields: %v", err)
		}
	}

	contact, err := h.buildContact(ctx, row)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to build contact: %v", err)
	}
	return &hermesv1.ContactsUpdateResponse{Contact: contact}, nil
}

// -----------------------------------------------------------------------
// DeleteContact
// -----------------------------------------------------------------------

func (h *Handler) DeleteContact(ctx context.Context, req *hermesv1.ContactsDeleteRequest) (*hermesv1.ContactsDeleteResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	err := h.store.DeleteContact(ctx, req.Id)
	if errors.Is(err, ErrNotFound) {
		return nil, status.Error(codes.NotFound, "contact not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete contact: %v", err)
	}
	return &hermesv1.ContactsDeleteResponse{}, nil
}

// -----------------------------------------------------------------------
// BulkDeleteContacts
// -----------------------------------------------------------------------

func (h *Handler) BulkDeleteContacts(ctx context.Context, req *hermesv1.ContactsBulkDeleteRequest) (*hermesv1.ContactsBulkDeleteResponse, error) {
	if len(req.Ids) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ids is required")
	}

	count, err := h.store.BulkDelete(ctx, req.Ids)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to bulk delete: %v", err)
	}
	return &hermesv1.ContactsBulkDeleteResponse{DeletedCount: int32(count)}, nil
}

// -----------------------------------------------------------------------
// BanCheck
// -----------------------------------------------------------------------

func (h *Handler) BanCheck(ctx context.Context, req *hermesv1.ContactsBanCheckRequest) (*hermesv1.ContactsBanCheckResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.Phone == "" {
		return nil, status.Error(codes.InvalidArgument, "phone is required")
	}

	banned, err := h.store.CheckBan(ctx, req.TenantId, req.Phone)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check ban: %v", err)
	}
	return &hermesv1.ContactsBanCheckResponse{IsBanned: banned}, nil
}

// -----------------------------------------------------------------------
// BulkBanCheck
// -----------------------------------------------------------------------

func (h *Handler) BulkBanCheck(ctx context.Context, req *hermesv1.ContactsBulkBanCheckRequest) (*hermesv1.ContactsBulkBanCheckResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if len(req.Phones) == 0 {
		return nil, status.Error(codes.InvalidArgument, "phones is required")
	}

	banMap, err := h.store.BulkCheckBan(ctx, req.TenantId, req.Phones)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to bulk ban check: %v", err)
	}

	results := make([]*hermesv1.BanCheckResult, 0, len(req.Phones))
	for _, phone := range req.Phones {
		results = append(results, &hermesv1.BanCheckResult{
			Phone:    phone,
			IsBanned: banMap[phone],
		})
	}
	return &hermesv1.ContactsBulkBanCheckResponse{Results: results}, nil
}

// -----------------------------------------------------------------------
// ListTags
// -----------------------------------------------------------------------

func (h *Handler) ListTags(ctx context.Context, req *hermesv1.ContactsListTagsRequest) (*hermesv1.ContactsListTagsResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}

	tags, err := h.store.ListTags(ctx, req.TenantId, req.Prefix)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list tags: %v", err)
	}

	pbTags := make([]*hermesv1.TagCount, 0, len(tags))
	for _, t := range tags {
		pbTags = append(pbTags, &hermesv1.TagCount{Tag: t.Tag, Count: t.Count})
	}
	return &hermesv1.ContactsListTagsResponse{Tags: pbTags}, nil
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

func (h *Handler) buildContact(ctx context.Context, row contactRow) (*hermesv1.Contact, error) {
	tags, err := h.store.GetTags(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	fields, err := h.store.GetCustomFields(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return &hermesv1.Contact{
		Id:           row.ID,
		TenantId:     row.TenantID,
		Phone:        row.Phone,
		Name:         row.Name,
		Tags:         tags,
		CustomFields: fields,
		IsBanned:     row.IsBanned,
		CreatedAt:    timestamppb.New(row.CreatedAt),
		UpdatedAt:    timestamppb.New(row.UpdatedAt),
	}, nil
}

func normalizePagination(p *hermesv1.PageRequest) (page, pageSize int32) {
	page, pageSize = 1, 50
	if p != nil {
		if p.Page > 0 {
			page = p.Page
		}
		if p.PageSize > 0 {
			pageSize = p.PageSize
		}
		if pageSize > 200 {
			pageSize = 200
		}
	}
	return
}
