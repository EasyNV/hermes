CREATE TABLE IF NOT EXISTS conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    contact_id UUID NOT NULL,
    wa_number_id UUID NOT NULL,
    assigned_to UUID,
    status TEXT NOT NULL CHECK (status IN ('unassigned', 'assigned', 'closed')) DEFAULT 'unassigned',
    last_message_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    campaign_id UUID,
    first_response_time_secs INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, contact_id, wa_number_id)
);
CREATE INDEX idx_conversations_workspace ON conversations(workspace_id);
CREATE INDEX idx_conversations_workspace_status ON conversations(workspace_id, status);
CREATE INDEX idx_conversations_assigned ON conversations(workspace_id, assigned_to);
CREATE INDEX idx_conversations_contact ON conversations(contact_id);
CREATE INDEX idx_conversations_last_msg ON conversations(workspace_id, last_message_at DESC);

CREATE TABLE IF NOT EXISTS messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    direction TEXT NOT NULL CHECK (direction IN ('inbound', 'outbound')),
    content_type TEXT NOT NULL CHECK (content_type IN ('text', 'image', 'document', 'audio', 'video')) DEFAULT 'text',
    body TEXT,
    media_url TEXT,
    template_id UUID,
    resolved_vars_json JSONB,
    wa_message_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'delivered', 'read', 'failed')) DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_messages_conversation ON messages(conversation_id, created_at);
CREATE INDEX idx_messages_created ON messages(created_at);
CREATE INDEX idx_messages_wa_id ON messages(wa_message_id) WHERE wa_message_id != '';

-- Full-text search index on message body
CREATE INDEX idx_messages_fts ON messages USING gin(to_tsvector('simple', coalesce(body, '')));

CREATE TABLE IF NOT EXISTS canned_responses (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    shortcut TEXT NOT NULL,
    body TEXT NOT NULL,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, shortcut)
);
CREATE INDEX idx_canned_responses_workspace ON canned_responses(workspace_id);
