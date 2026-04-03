-- WA numbers table (session metadata). whatsmeow manages its own session tables separately.

CREATE TABLE IF NOT EXISTS wa_numbers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    jid TEXT NOT NULL DEFAULT '',
    phone TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('active', 'banned', 'disconnected', 'cooldown')) DEFAULT 'disconnected',
    proxy_id UUID,
    health_score INT NOT NULL DEFAULT 100,
    daily_sent_count INT NOT NULL DEFAULT 0,
    total_sent BIGINT NOT NULL DEFAULT 0,
    ban_count INT NOT NULL DEFAULT 0,
    last_ban_at TIMESTAMPTZ,
    connected_at TIMESTAMPTZ,
    pod_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_wa_numbers_tenant ON wa_numbers(tenant_id);
CREATE INDEX idx_wa_numbers_tenant_status ON wa_numbers(tenant_id, status);
CREATE INDEX idx_wa_numbers_pod ON wa_numbers(pod_id);
CREATE UNIQUE INDEX idx_wa_numbers_tenant_phone ON wa_numbers(tenant_id, phone);

CREATE TABLE IF NOT EXISTS wa_number_workspaces (
    wa_number_id UUID NOT NULL REFERENCES wa_numbers(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL,
    PRIMARY KEY (wa_number_id, workspace_id)
);
CREATE INDEX idx_wa_number_workspaces_workspace ON wa_number_workspaces(workspace_id);
