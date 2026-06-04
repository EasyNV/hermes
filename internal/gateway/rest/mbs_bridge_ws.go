package rest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"

	hermesv1 "github.com/hermes-waba/hermes/gen/go/hermes/v1"
	"github.com/hermes-waba/hermes/internal/gateway/middleware"
	mbshandler "github.com/hermes-waba/hermes/internal/mbs/handler"
)

// ─────────────────────────────────────────────────────────────────────
// BridgeLogin WebSocket bridge
//
// Tunnels the bidirectional hermesv1.HermesMbs/BridgeLogin gRPC stream
// over a single WebSocket connection. Tenant is force-overwritten from
// the JWT claim on the inbound `start` frame — no matter what the
// client puts in payload.tenantId, the mbs backend sees the JWT
// tenant.
//
// Frame schema (browser ↔ gateway, both directions are JSON):
//
//   Browser → gateway:
//     {"type":"start","payload":{email, password, totpSecret?, ...}}
//     {"type":"input","payload":{fieldId, value}}
//     {"type":"cancel"}
//
//   Gateway → browser:
//     {"type":"bridge_login_prompt","payload":{stepId, instructions, fields[]}}
//     {"type":"bridge_login_progress","payload":{stage, detail}}
//     {"type":"bridge_login_success","payload":{full BridgeLoginSuccess JSON}}
//     {"type":"bridge_login_failure","payload":{code, message, retryable}}
//     {"type":"error","payload":{code, message}}
//
// Lifecycle:
//   1. JWT validate (?token=)
//   2. mbsClient nil-check → 503 BEFORE upgrade
//   3. WS Accept
//   4. Open gRPC BridgeLogin stream with outgoing metadata carrying
//      tenant-id (so mbs's interceptor sees the right tenant)
//   5. Read first WS frame (must be `start`); force tenant from JWT;
//      send BridgeLoginRequest_Start
//   6. Pump A goroutine: gRPC.Recv → write JSON frame to WS
//   7. Main loop: WS.Read → dispatch input/cancel → gRPC.Send
//   8. Cleanup: cancel grpcCtx → pump A exits → CloseNow defers
// ─────────────────────────────────────────────────────────────────────

const (
	// bridgeWSReadLimit caps inbound frames. The biggest legitimate
	// frame is the start with email + password + optional totpSecret —
	// 16 KB is well above worst case and prevents abuse.
	bridgeWSReadLimit = 16 * 1024

	// bridgeWSIdleTimeout bounds the whole BridgeLogin flow. The mbs
	// handler's BridgeOverallTimeout default is 180s; we add headroom
	// for 2FA prompts surfacing on the UI side. A stuck flow that
	// exceeds this gets clean teardown rather than hanging forever.
	bridgeWSIdleTimeout = 5 * time.Minute
)

// Frame discriminator strings — match the chunk-4 frontend types.
const (
	wsBridgeStart   = "start"
	wsBridgeInput   = "input"
	wsBridgeCancel  = "cancel"
	wsErrCodeBadFrame      = "BAD_FRAME"
	wsErrCodeStreamSend    = "STREAM_SEND_FAILED"
	wsErrCodeStreamRecv    = "STREAM_RECV_FAILED"
	wsErrCodeBridgeOpen    = "BRIDGE_OPEN_FAILED"
)

// startFramePayload is the body of the browser's first message.
type startFramePayload struct {
	TenantID          string `json:"tenantId"` // overwritten from JWT
	Email             string `json:"email"`
	Password          string `json:"password"`
	TotpSecret        string `json:"totpSecret,omitempty"`
	ForceNewDeviceId  bool   `json:"forceNewDeviceId,omitempty"`
	PersistTotpSecret bool   `json:"persistTotpSecret,omitempty"`
	ProxyID           string `json:"proxyId,omitempty"`
}

type startFrame struct {
	Type    string            `json:"type"`
	Payload startFramePayload `json:"payload"`
}

type inputFramePayload struct {
	FieldId string `json:"fieldId"`
	Value   string `json:"value"`
}

type inputFrame struct {
	Type    string            `json:"type"`
	Payload inputFramePayload `json:"payload"`
}

// genericFrame is used to discriminate before unmarshalling the typed
// shape — first-pass parse just reads the `type` field.
type genericFrame struct {
	Type string `json:"type"`
}

// bridgeLoginWS upgrades the request to a WebSocket, validates JWT,
// opens a gRPC BridgeLogin stream, and bridges frames in both
// directions until either end closes.
func (a *Adapter) bridgeLoginWS(w http.ResponseWriter, r *http.Request) {
	// ── 1. JWT (query param token; matches the existing /ws hub) ──
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	claims, err := middleware.ParseJWT(token, a.jwtSecret)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if claims.TenantID == "" {
		http.Error(w, "tenant claim missing", http.StatusUnauthorized)
		return
	}

	// ── 2. mbsClient availability check BEFORE upgrade ──
	// Surface as plain HTTP 503 so the browser sees a sensible
	// status instead of a half-open WS that closes immediately.
	if a.mbsClient == nil {
		http.Error(w, "mbs service not available", http.StatusServiceUnavailable)
		return
	}

	// ── 3. Upgrade ──
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// AcceptOptions zero-value uses sane defaults: subprotocol
		// negotiation off, origin check off (dev — production goes
		// behind a reverse proxy with allowed-origins).
	})
	if err != nil {
		a.log.Warn().Err(err).Msg("ws/mbs/bridge-login: accept failed")
		return
	}
	conn.SetReadLimit(bridgeWSReadLimit)
	defer conn.CloseNow() // hard close on the way out

	// ── 4. gRPC context with outgoing metadata ──
	bridgeCtx, cancel := context.WithTimeout(r.Context(), bridgeWSIdleTimeout)
	defer cancel()
	// Keys MUST match mbshandler.TenantMetadataKey ("x-tenant-id") —
	// the mbs server-side interceptor in internal/mbs/handler/tenant.go
	// reads that constant. An earlier revision sent bare "tenant-id"
	// (no x-) which silently broke every BridgeLogin RPC. x-user-id
	// follows the same convention for symmetry with the unary path.
	md := metadata.New(map[string]string{
		mbshandler.TenantMetadataKey: claims.TenantID,
		"x-user-id":                  claims.UserID,
	})
	grpcCtx := metadata.NewOutgoingContext(bridgeCtx, md)

	// ── 5. Open the gRPC BridgeLogin stream ──
	stream, err := a.mbsClient.BridgeLogin(grpcCtx)
	if err != nil {
		safeSendWSError(bridgeCtx, conn, nil, wsErrCodeBridgeOpen, err.Error())
		_ = conn.Close(websocket.StatusInternalError, "open stream failed")
		return
	}

	// ── 6. Read the first WS frame; must be `start` ──
	_, raw, err := conn.Read(bridgeCtx)
	if err != nil {
		return // client closed before sending start, or read deadline hit
	}
	var probe genericFrame
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Type != wsBridgeStart {
		safeSendWSError(bridgeCtx, conn, nil, wsErrCodeBadFrame, "first frame must be 'start'")
		_ = conn.Close(websocket.StatusPolicyViolation, "bad first frame")
		return
	}
	var start startFrame
	if err := json.Unmarshal(raw, &start); err != nil {
		safeSendWSError(bridgeCtx, conn, nil, wsErrCodeBadFrame, "start frame malformed")
		_ = conn.Close(websocket.StatusPolicyViolation, "bad start frame")
		return
	}

	// ── 7. Forward start to mbs with tenant FORCED from JWT ──
	if err := stream.Send(&hermesv1.BridgeLoginRequest{
		Payload: &hermesv1.BridgeLoginRequest_Start{
			Start: &hermesv1.BridgeLoginStart{
				TenantId:          claims.TenantID, // forced, ignores start.Payload.TenantID
				Email:             start.Payload.Email,
				Password:          start.Payload.Password,
				TotpSecret:        start.Payload.TotpSecret,
				ForceNewDeviceId:  start.Payload.ForceNewDeviceId,
				PersistTotpSecret: start.Payload.PersistTotpSecret,
				ProxyId:           start.Payload.ProxyID,
			},
		},
	}); err != nil {
		safeSendWSError(bridgeCtx, conn, nil, wsErrCodeStreamSend, err.Error())
		return
	}

	// ── 8. WS writes must be serialized — coder/websocket.Conn.Write
	// is NOT safe for concurrent calls. Both pumps may want to write
	// (pump A on every gRPC update, pump B on parse-failure errors).
	// A small mutex around writeFrame serializes them. ──
	var writeMu sync.Mutex

	// ── 9. Pump A — gRPC → WS ──
	pumpADone := make(chan struct{})
	go func() {
		defer close(pumpADone)
		for {
			upd, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				if bridgeCtx.Err() != nil {
					return // we tore down on purpose
				}
				safeSendWSError(bridgeCtx, conn, &writeMu, wsErrCodeStreamRecv, err.Error())
				return
			}
			frame := buildOutboundFrame(upd)
			if frame == nil {
				continue // unknown variant — drop
			}
			if err := safeWriteFrame(bridgeCtx, conn, &writeMu, frame); err != nil {
				return // WS closed
			}
		}
	}()

	// ── 10. Pump B — WS → gRPC (main goroutine) ──
wsLoop:
	for {
		_, raw, err := conn.Read(bridgeCtx)
		if err != nil {
			break
		}
		var probe genericFrame
		if err := json.Unmarshal(raw, &probe); err != nil {
			safeSendWSError(bridgeCtx, conn, &writeMu, wsErrCodeBadFrame, "invalid json")
			continue
		}
		switch probe.Type {
		case wsBridgeInput:
			var in inputFrame
			if err := json.Unmarshal(raw, &in); err != nil {
				safeSendWSError(bridgeCtx, conn, &writeMu, wsErrCodeBadFrame, "input frame malformed")
				continue
			}
			_ = stream.Send(&hermesv1.BridgeLoginRequest{
				Payload: &hermesv1.BridgeLoginRequest_Input{
					Input: &hermesv1.BridgeLoginInput{
						FieldId: in.Payload.FieldId,
						Value:   in.Payload.Value,
					},
				},
			})
		case wsBridgeCancel:
			_ = stream.Send(&hermesv1.BridgeLoginRequest{
				Payload: &hermesv1.BridgeLoginRequest_Cancel{
					Cancel: &hermesv1.BridgeLoginCancel{},
				},
			})
			_ = stream.CloseSend()
			// Labeled break — exit the WS read loop, NOT just the
			// switch. After cancel, pump A drains terminal frames
			// and we move to cleanup.
			break wsLoop
		default:
			safeSendWSError(bridgeCtx, conn, &writeMu, wsErrCodeBadFrame, "unknown type")
		}
	}

	// ── 11. Cleanup — cancel propagates to pump A, wait with timeout ──
	cancel()
	select {
	case <-pumpADone:
	case <-time.After(3 * time.Second):
		// Pump A stuck on a slow gRPC stream. coder/websocket.CloseNow
		// in our defer forcibly terminates the connection; pump A
		// will observe and exit on its own time.
	}
}

// ─── outbound frame mapping (gRPC → JSON) ────────────────────────────

// buildOutboundFrame converts a gRPC BridgeLoginUpdate into the JSON
// frame format the browser expects. Returns nil for unknown variants
// (graceful drop rather than reject — forward-compat with future enum
// additions).
//
// Success uses protojson to serialize the full BridgeLoginSuccess
// proto so the assets list shape stays in lockstep with the proto.
// Prompt/Progress/Failure are small enough that hand-mapping keeps
// the wire shape under direct control here.
func buildOutboundFrame(upd *hermesv1.BridgeLoginUpdate) map[string]any {
	switch ev := upd.GetEvent().(type) {
	case *hermesv1.BridgeLoginUpdate_Prompt:
		return map[string]any{
			"type": "bridge_login_prompt",
			"payload": map[string]any{
				"stepId":       ev.Prompt.GetStepId(),
				"instructions": ev.Prompt.GetInstructions(),
				"fields":       fieldsToJSON(ev.Prompt.GetFields()),
			},
		}
	case *hermesv1.BridgeLoginUpdate_Progress:
		return map[string]any{
			"type": "bridge_login_progress",
			"payload": map[string]any{
				"stage":  ev.Progress.GetStage(),
				"detail": ev.Progress.GetDetail(),
			},
		}
	case *hermesv1.BridgeLoginUpdate_Success:
		b, err := protojson.MarshalOptions{
			EmitDefaultValues: true,
			UseProtoNames:     false,
		}.Marshal(ev.Success)
		if err != nil {
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal(b, &payload); err != nil {
			return nil
		}
		return map[string]any{
			"type":    "bridge_login_success",
			"payload": payload,
		}
	case *hermesv1.BridgeLoginUpdate_Failure:
		return map[string]any{
			"type": "bridge_login_failure",
			"payload": map[string]any{
				"code":      ev.Failure.GetCode(),
				"message":   ev.Failure.GetMessage(),
				"retryable": ev.Failure.GetRetryable(),
			},
		}
	}
	return nil
}

func fieldsToJSON(in []*hermesv1.BridgeLoginField) []map[string]string {
	out := make([]map[string]string, 0, len(in))
	for _, f := range in {
		out = append(out, map[string]string{
			"id":   f.GetId(),
			"name": f.GetName(),
			"type": f.GetType(),
		})
	}
	return out
}

// ─── concurrency-safe write helpers ──────────────────────────────────

// safeWriteFrame marshals a frame to JSON and writes it to the WS
// connection under writeMu. If writeMu is nil (used for the
// pre-pump phase where only one goroutine exists), the write is
// unguarded — caller responsibility.
func safeWriteFrame(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, frame any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if writeMu != nil {
		writeMu.Lock()
		defer writeMu.Unlock()
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

// safeSendWSError writes an error frame, ignoring write errors (the
// connection is on its way out anyway). The error frame matches what
// the chunk-4 frontend client surfaces as `BridgeLoginEvent.error`.
func safeSendWSError(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, code, message string) {
	_ = safeWriteFrame(ctx, conn, writeMu, map[string]any{
		"type": "error",
		"payload": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
