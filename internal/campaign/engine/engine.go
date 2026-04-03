package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/campaign/handler"
	"github.com/hermes-waba/hermes/internal/campaign/spintax"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// engineStore defines the subset of handler.Store the engine needs.
// Satisfied implicitly by handler.PgStore.
type engineStore interface {
	GetCampaign(ctx context.Context, id string) (*handler.CampaignRow, error)
	GetTemplate(ctx context.Context, id string) (*handler.TemplateRow, error)
	GetActiveCampaignNumbers(ctx context.Context, campaignID string) ([]*handler.CampaignNumberRow, error)
	GetPendingContacts(ctx context.Context, campaignID string, limit int32) ([]*handler.PendingContactRow, error)
	UpdateContactSent(ctx context.Context, campaignID, contactID, waNumberID string) error
	IncrementSentCount(ctx context.Context, campaignID string) error
	IncrementNumberSentCount(ctx context.Context, campaignID, waNumberID string) error
	UpdateCampaignStatus(ctx context.Context, id, status string, setStarted, setCompleted bool) (*handler.CampaignRow, error)
}

type runningCampaign struct {
	cancel      context.CancelFunc
	tenantID    string
	workspaceID string
}

// Engine manages campaign dispatch goroutines.
type Engine struct {
	store   engineStore
	js      natsgo.JetStreamContext
	log     zerolog.Logger
	mu      sync.Mutex
	running map[string]*runningCampaign
}

func NewEngine(store engineStore, js natsgo.JetStreamContext, log zerolog.Logger) *Engine {
	return &Engine{
		store:   store,
		js:      js,
		log:     log,
		running: make(map[string]*runningCampaign),
	}
}

// Start begins dispatching for a campaign. No-op if already running.
func (e *Engine) Start(campaignID, workspaceID, tenantID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.running[campaignID]; exists {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.running[campaignID] = &runningCampaign{
		cancel:      cancel,
		tenantID:    tenantID,
		workspaceID: workspaceID,
	}

	go e.dispatchLoop(ctx, campaignID, tenantID, workspaceID)
	e.log.Info().Str("campaign_id", campaignID).Msg("campaign dispatch started")
	return nil
}

// Stop cancels dispatch for a campaign.
func (e *Engine) Stop(campaignID string) {
	e.mu.Lock()
	rc, exists := e.running[campaignID]
	if exists {
		delete(e.running, campaignID)
	}
	e.mu.Unlock()

	if exists {
		rc.cancel()
		e.log.Info().Str("campaign_id", campaignID).Msg("campaign dispatch stopped")
	}
}

// IsRunning returns true if a campaign's dispatch goroutine is active.
func (e *Engine) IsRunning(campaignID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, exists := e.running[campaignID]
	return exists
}

func (e *Engine) dispatchLoop(ctx context.Context, campaignID, tenantID, workspaceID string) {
	defer func() {
		e.mu.Lock()
		delete(e.running, campaignID)
		e.mu.Unlock()
	}()

	campaign, err := e.store.GetCampaign(ctx, campaignID)
	if err != nil || campaign == nil {
		e.log.Error().Err(err).Str("campaign_id", campaignID).Msg("failed to load campaign for dispatch")
		return
	}

	tmpl, err := e.store.GetTemplate(ctx, campaign.TemplateID)
	if err != nil || tmpl == nil {
		e.log.Error().Err(err).Str("campaign_id", campaignID).Msg("failed to load template for dispatch")
		return
	}

	var rotator Rotator
	switch campaign.RotationStrategy {
	case "least_used":
		rotator = NewLeastUsed()
	default:
		rotator = NewRoundRobin()
	}

	totalContacts := campaign.TotalContacts
	dispatched := campaign.SentCount // resume from where we left off
	batchSize := int32(100)
	lastProgress := time.Now()
	startTime := time.Now()

	for {
		if ctx.Err() != nil {
			return
		}

		contacts, err := e.store.GetPendingContacts(ctx, campaignID, batchSize)
		if err != nil {
			e.log.Error().Err(err).Str("campaign_id", campaignID).Msg("failed to fetch pending contacts")
			return
		}

		if len(contacts) == 0 {
			// All contacts processed — mark completed.
			if _, err := e.store.UpdateCampaignStatus(ctx, campaignID, "completed", false, true); err != nil {
				e.log.Error().Err(err).Str("campaign_id", campaignID).Msg("failed to mark campaign completed")
			}
			e.publishStatusEvent(tenantID, workspaceID, campaignID,
				hermesv1.CampaignStatus_CAMPAIGN_STATUS_RUNNING,
				hermesv1.CampaignStatus_CAMPAIGN_STATUS_COMPLETED, "completed")
			e.log.Info().Str("campaign_id", campaignID).Int32("dispatched", dispatched).Msg("campaign completed")
			return
		}

		for _, contact := range contacts {
			if ctx.Err() != nil {
				return
			}

			// Refresh number states for rotation decisions.
			nums, err := e.store.GetActiveCampaignNumbers(ctx, campaignID)
			if err != nil {
				e.log.Error().Err(err).Str("campaign_id", campaignID).Msg("failed to fetch active numbers")
				return
			}

			infos := make([]NumberInfo, 0, len(nums))
			for _, n := range nums {
				infos = append(infos, NumberInfo{
					WaNumberID: n.WaNumberID,
					SentToday:  n.SentCount,
					Status:     n.Status,
				})
			}

			numID, ok := rotator.Next(infos, campaign.DailyCapPerNum)
			if !ok {
				e.log.Warn().Str("campaign_id", campaignID).Msg("all numbers exhausted, stopping dispatch")
				return
			}

			// Build variables map.
			vars := map[string]string{
				"name":  contact.Name,
				"phone": contact.Phone,
			}
			for k, v := range contact.CustomFields {
				vars[k] = v
			}

			resolvedBody := spintax.Resolve(tmpl.Body)
			resolvedBody = spintax.SubstituteVariables(resolvedBody, vars)

			varsJSON, _ := json.Marshal(vars)
			typingMs := calculateTypingDuration(len(resolvedBody))
			postSendMs := randomDelay(campaign.DelayMinMs, campaign.DelayMaxMs)
			recipientJID := strings.TrimPrefix(contact.Phone, "+") + "@s.whatsapp.net"

			task := &hermesv1.CampaignSendTask{
				Meta: &hermesv1.EventMeta{
					EventId:   uuid.New().String(),
					TenantId:  tenantID,
					Timestamp: timestamppb.Now(),
					Source:    "hermes-campaign",
				},
				CampaignId:       campaignID,
				ContactId:        contact.ContactID,
				WaNumberId:       numID,
				RecipientJid:     recipientJID,
				RecipientPhone:   contact.Phone,
				ResolvedBody:     resolvedBody,
				MediaUrl:         tmpl.MediaURL,
				MediaType:        tmpl.MediaType,
				TypingDurationMs: int32(typingMs),
				PostSendDelayMs:  int32(postSendMs),
				TemplateId:       tmpl.ID,
				ResolvedVarsJson: string(varsJSON),
				IdempotencyKey:   campaignID + ":" + contact.ContactID,
			}

			if e.js != nil {
				data, err := proto.Marshal(task)
				if err != nil {
					e.log.Error().Err(err).Msg("failed to marshal send task")
					continue
				}
				subject := fmt.Sprintf("hermes.wa.send.campaign.%s", tenantID)
				if _, err := e.js.Publish(subject, data, natsgo.MsgId(task.Meta.EventId)); err != nil {
					e.log.Error().Err(err).Str("contact_id", contact.ContactID).Msg("failed to publish send task")
					continue
				}
			}

			// Update DB state.
			_ = e.store.UpdateContactSent(ctx, campaignID, contact.ContactID, numID)
			_ = e.store.IncrementSentCount(ctx, campaignID)
			_ = e.store.IncrementNumberSentCount(ctx, campaignID, numID)

			dispatched++

			// Publish progress every 10 sends or 5 seconds.
			if dispatched%10 == 0 || time.Since(lastProgress) >= 5*time.Second {
				e.publishProgress(tenantID, workspaceID, campaignID, totalContacts, dispatched, startTime)
				lastProgress = time.Now()
			}
		}
	}
}

// calculateTypingDuration: clamp(len * rand(50,80), 1500, 8000) ms.
func calculateTypingDuration(bodyLen int) int {
	perChar := 50 + rand.Intn(31) // 50-80 ms per char
	ms := bodyLen * perChar
	if ms < 1500 {
		ms = 1500
	}
	if ms > 8000 {
		ms = 8000
	}
	return ms
}

func randomDelay(minMs, maxMs int32) int {
	if maxMs <= minMs {
		return int(minMs)
	}
	return int(minMs) + rand.Intn(int(maxMs-minMs))
}

func (e *Engine) publishStatusEvent(tenantID, workspaceID, campaignID string, prev, next hermesv1.CampaignStatus, reason string) {
	if e.js == nil {
		return
	}

	event := &hermesv1.CampaignStatusEvent{
		Meta: &hermesv1.EventMeta{
			EventId:   uuid.New().String(),
			TenantId:  tenantID,
			Timestamp: timestamppb.Now(),
			Source:    "hermes-campaign",
		},
		CampaignId:     campaignID,
		WorkspaceId:    workspaceID,
		PreviousStatus: prev,
		NewStatus:      next,
		Reason:         reason,
	}

	data, err := proto.Marshal(event)
	if err != nil {
		return
	}

	subject := fmt.Sprintf("hermes.campaign.status.%s", tenantID)
	e.js.Publish(subject, data, natsgo.MsgId(event.Meta.EventId))
}

func (e *Engine) publishProgress(tenantID, workspaceID, campaignID string, totalContacts, dispatched int32, startTime time.Time) {
	if e.js == nil {
		return
	}

	var progressPct float32
	if totalContacts > 0 {
		progressPct = float32(dispatched) / float32(totalContacts) * 100
	}

	elapsed := time.Since(startTime).Minutes()
	var sendRate float32
	if elapsed > 0 {
		sendRate = float32(dispatched) / float32(elapsed)
	}

	var etaSeconds int32
	if sendRate > 0 {
		remaining := totalContacts - dispatched
		etaSeconds = int32(float32(remaining) / sendRate * 60)
	}

	event := &hermesv1.CampaignProgressEvent{
		Meta: &hermesv1.EventMeta{
			EventId:   uuid.New().String(),
			TenantId:  tenantID,
			Timestamp: timestamppb.Now(),
			Source:    "hermes-campaign",
		},
		CampaignId:      campaignID,
		WorkspaceId:     workspaceID,
		TotalContacts:   totalContacts,
		SentCount:       dispatched,
		ProgressPercent: progressPct,
		SendRatePerMin:  sendRate,
		EtaSeconds:      etaSeconds,
	}

	data, err := proto.Marshal(event)
	if err != nil {
		return
	}

	subject := fmt.Sprintf("hermes.campaign.progress.%s", tenantID)
	e.js.Publish(subject, data, natsgo.MsgId(event.Meta.EventId))
}
