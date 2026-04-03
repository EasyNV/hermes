CREATE TABLE IF NOT EXISTS templates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    body TEXT NOT NULL,
    media_url TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT '',
    variables JSONB DEFAULT '[]',
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_templates_workspace ON templates(workspace_id);

CREATE TABLE IF NOT EXISTS campaigns (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    template_id UUID NOT NULL REFERENCES templates(id),
    name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('draft', 'scheduled', 'running', 'paused', 'completed', 'cancelled')) DEFAULT 'draft',
    schedule_at TIMESTAMPTZ,
    daily_cap_per_num INT NOT NULL DEFAULT 200,
    ban_pause_threshold INT NOT NULL DEFAULT 0,
    rotation_strategy TEXT NOT NULL CHECK (rotation_strategy IN ('round_robin', 'least_used')) DEFAULT 'round_robin',
    delay_min_ms INT NOT NULL DEFAULT 3000,
    delay_max_ms INT NOT NULL DEFAULT 15000,
    total_contacts INT NOT NULL DEFAULT 0,
    sent_count INT NOT NULL DEFAULT 0,
    failed_count INT NOT NULL DEFAULT 0,
    replied_count INT NOT NULL DEFAULT 0,
    banned_count INT NOT NULL DEFAULT 0,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);
CREATE INDEX idx_campaigns_workspace ON campaigns(workspace_id);
CREATE INDEX idx_campaigns_workspace_status ON campaigns(workspace_id, status);

CREATE TABLE IF NOT EXISTS campaign_numbers (
    campaign_id UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    wa_number_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    sent_count INT NOT NULL DEFAULT 0,
    failed_count INT NOT NULL DEFAULT 0,
    PRIMARY KEY (campaign_id, wa_number_id)
);

CREATE TABLE IF NOT EXISTS campaign_contacts (
    campaign_id UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    contact_id UUID NOT NULL,
    wa_number_id UUID,
    status TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'delivered', 'failed', 'skipped')) DEFAULT 'pending',
    sent_at TIMESTAMPTZ,
    delivered_at TIMESTAMPTZ,
    failed_at TIMESTAMPTZ,
    error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (campaign_id, contact_id)
);
CREATE INDEX idx_campaign_contacts_status ON campaign_contacts(campaign_id, status);
CREATE INDEX idx_campaign_contacts_number ON campaign_contacts(campaign_id, wa_number_id);
