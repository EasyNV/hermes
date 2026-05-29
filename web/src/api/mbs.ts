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

export type MbsBridgeClientFrame =
  | {
      type: 'start'
      payload: { tenantId: string; identifier: string; password: string }
    }
  | { type: 'input'; payload: { stepId: string; value: string } }
  | { type: 'cancel' }

export type MbsBridgePromptKind = 'otp_2fa' | 'checkpoint' | 'recovery'

export type MbsBridgeServerFrame =
  | {
      type: 'prompt'
      payload: { stepId: string; kind: MbsBridgePromptKind; prompt: string }
    }
  | { type: 'progress'; payload: { stage: string; detail: string } }
  | {
      type: 'success'
      payload: { uid: string; fbid: string; state: MbsSessionState }
    }
  | { type: 'failure'; payload: { code: string; message: string } }
  | { type: 'error'; payload: { code: string; message: string } }
