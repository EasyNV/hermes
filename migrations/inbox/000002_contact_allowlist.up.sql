-- Contact allowlist: only contacts on this list generate inbox conversations.
-- Auto-populated when campaigns start. Manual additions for VIPs/support.
CREATE TABLE IF NOT EXISTS contact_allowlist (
    workspace_id UUID NOT NULL,
    phone TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'manual', -- 'campaign', 'manual', 'import'
    source_id TEXT NOT NULL DEFAULT '',     -- campaign_id if source='campaign'
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, phone)
);
CREATE INDEX idx_contact_allowlist_phone ON contact_allowlist(phone);
