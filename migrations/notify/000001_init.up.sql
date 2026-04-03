CREATE TABLE IF NOT EXISTS notification_configs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('browser_push', 'sound', 'webhook')),
    webhook_url TEXT NOT NULL DEFAULT '',
    webhook_type TEXT NOT NULL CHECK (webhook_type IN ('', 'telegram', 'discord', 'custom')) DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, type, webhook_type)
);
CREATE INDEX idx_notification_configs_workspace ON notification_configs(workspace_id);
