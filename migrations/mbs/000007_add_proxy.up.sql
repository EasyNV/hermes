-- MBS proxy support (2026-06-03): add proxy assignment to mbs_sessions.
--
-- The proxy pool (proxies table, owned by hermes-proxy) is generalized to be
-- assignable to MBS sessions, not just WA numbers. This adds the per-session
-- link + an audit timestamp. The proxy is STICKY: pinned once at first connect
-- (auto from pool or explicit), reused across reconnect/self-heal, and only
-- changed on failure (auto-rotate) or explicit operator override.
--
-- proxy_id is a bare UUID referencing proxies.id. There is NO hard FK because
-- proxies belongs to the hermes-proxy service's logical DB boundary — this
-- mirrors how wa_numbers.proxy_id is a bare UUID (cross-service reference, not
-- an enforced FK). NULL = no proxy assigned (direct connection under the soft
-- policy, or refused under PROXY_REQUIRED=true).

ALTER TABLE mbs_sessions ADD COLUMN proxy_id UUID;
ALTER TABLE mbs_sessions ADD COLUMN proxy_assigned_at TIMESTAMPTZ;

CREATE INDEX idx_mbs_sessions_proxy ON mbs_sessions (proxy_id) WHERE proxy_id IS NOT NULL;

COMMENT ON COLUMN mbs_sessions.proxy_id IS
    'Assigned proxy (proxies.id, cross-service bare UUID, no hard FK). Sticky '
    'for the session lifetime. NULL = direct connection. proxies.assigned_count '
    'now counts WA numbers AND MBS sessions referencing the proxy.';
COMMENT ON COLUMN mbs_sessions.proxy_assigned_at IS
    'When the current proxy was pinned. Diagnostic for stickiness / rotation audit.';
