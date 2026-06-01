import { api, qs } from './client'
import type {
  MbsSessionState,
  MbsListSessionsResponse,
  MbsGetSessionStatusResponse,
  MbsListSessionAssetsResponse,
  MbsBurnSessionResponse,
  MbsResolvePhoneResponse,
  MbsSendMessageResponse,
  PageRequest,
} from './types'

// ─────────────────────────────────────────────────────────────────────
// MBS REST surface (Stage E2 chunk 4)
//
// All routes under /api/v1/mbs-sessions. Tenant ID is forced from the
// JWT at the gateway boundary — clients NEVER pass tenantId in the
// body, and path uid wins over body uid for the routes that take it.
//
// Backend handlers: internal/gateway/rest/handlers_mbs.go (chunk 2).
// ─────────────────────────────────────────────────────────────────────

/**
 * List MBS sessions for the caller's tenant.
 *
 * Pass `state` to filter; omit it (do NOT pass UNSPECIFIED) for "any
 * state". qs() drops null/undefined/empty values; UNSPECIFIED is a
 * non-empty enum string and would be sent verbatim → backend treats it
 * as an explicit filter.
 */
export function listMbsSessions(params: {
  tenantId: string
  state?: MbsSessionState
} & PageRequest) {
  return api.get<MbsListSessionsResponse>(`/mbs-sessions${qs(params)}`)
}

export function getMbsSessionStatus(uid: string) {
  return api.get<MbsGetSessionStatusResponse>(`/mbs-sessions/${uid}`)
}

export function listMbsSessionAssets(uid: string) {
  return api.get<MbsListSessionAssetsResponse>(`/mbs-sessions/${uid}/assets`)
}

/**
 * Burn the session (mark BURNED + zeroize encrypted creds at rest).
 * Body is optional — empty payload OK (backend treats reason as "" if
 * absent). Returns the updated session for store reconciliation.
 */
export function burnMbsSession(uid: string, reason?: string) {
  return api.post<MbsBurnSessionResponse>(
    `/mbs-sessions/${uid}/burn`,
    reason ? { reason } : undefined,
  )
}

/**
 * Resolve a phone number to its MBS messenger thread for a given uid.
 * Returns exists=false when the phone is not in this uid's contact
 * graph (cold-compose UX: caller should surface "not on Messenger"
 * rather than attempting send).
 */
export function resolveMbsPhone(uid: string, phone: string) {
  return api.post<MbsResolvePhoneResponse>(
    `/mbs-sessions/${uid}/resolve-phone`,
    { phone },
  )
}

/**
 * Send a text message on an existing thread. otid is the client-side
 * outbound transaction id used to reconcile the optimistic UI bubble
 * with the eventual mbs_outbound_status WS frame.
 */
export function sendMbsMessage(
  uid: string,
  params: { threadId: string; text: string; otid?: string },
) {
  return api.post<MbsSendMessageResponse>(`/mbs-sessions/${uid}/messages`, params)
}

// ─────────────────────────────────────────────────────────────────────
// BridgeLogin WS frame typing
//
// The bridge-login WebSocket is mounted at /ws/mbs/bridge-login
// (OUTSIDE /api/v1; the gateway adapter validates JWT inline).
// Frames are JSON-tagged by `type`. Backend forces tenantId from JWT
// on the `start` frame regardless of payload contents — the field is
// still sent for symmetry with the proto but treat its value as
// advisory; the backend overwrites it.
// ─────────────────────────────────────────────────────────────────────

// Client → gateway frames. Field names MUST match the gateway WS bridge
// struct tags in internal/gateway/rest/mbs_bridge_ws.go, which in turn
// mirror the proto (proto/hermes/v1/mbs.proto BridgeLoginStart /
// BridgeLoginInput). The gateway force-overwrites tenantId from the JWT;
// we still send it for symmetry.
export type MbsBridgeClientFrame =
  | {
      type: 'start'
      payload: {
        tenantId: string
        email: string
        password: string
        // base32 TOTP secret. When set, hermes-mbs derives 2FA codes
        // server-side and auto-fills the two_step_verification prompt
        // without surfacing it to the UI.
        totpSecret?: string
        // Force a fresh device_id — use when re-bridging an account
        // whose device_id may have been burned by Meta's risk engine.
        forceNewDeviceId?: boolean
        // Persist totpSecret encrypted for future unattended re-bridge.
        persistTotpSecret?: boolean
      }
    }
  | { type: 'input'; payload: { fieldId: string; value: string } }
  | { type: 'cancel' }

// A single field the server asks us to fill during a prompt (e.g. a
// 2FA code box, a captcha response). Mirrors proto BridgeLoginField.
export interface MbsBridgeField {
  id: string
  name: string
  type: string // "text" | "code" | "password"
}

// Server → client frames. The gateway tags every outbound frame with a
// "bridge_login_" prefix (see buildOutboundFrame); the plain "error"
// frame is the only un-prefixed one. Payload shapes mirror the proto
// BridgeLoginUpdate variants exactly (success is protojson-serialized
// with camelCase field names via EmitDefaultValues).
export type MbsBridgeServerFrame =
  | {
      type: 'bridge_login_prompt'
      payload: { stepId: string; instructions: string; fields: MbsBridgeField[] }
    }
  | { type: 'bridge_login_progress'; payload: { stage: string; detail: string } }
  | {
      type: 'bridge_login_success'
      payload: {
        uid: string
        displayName?: string
        pageCount?: number
        primaryPageId?: string
        primaryPageName?: string
        primaryWabaId?: string
        primaryWecMailboxId?: string
        primaryWecPhoneNumber?: string
        assets?: unknown[]
      }
    }
  | {
      type: 'bridge_login_failure'
      payload: { code: string; message: string; retryable?: boolean }
    }
  | { type: 'error'; payload: { code: string; message: string } }
