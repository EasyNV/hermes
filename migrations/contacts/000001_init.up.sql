CREATE TABLE IF NOT EXISTS contacts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    phone TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    is_banned BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, phone)
);
CREATE INDEX idx_contacts_tenant ON contacts(tenant_id);
CREATE INDEX idx_contacts_tenant_phone ON contacts(tenant_id, phone);

-- GIN index for full-text search on name + phone
CREATE INDEX idx_contacts_search ON contacts USING gin(
    (to_tsvector('simple', coalesce(name, '') || ' ' || coalesce(phone, '')))
);

CREATE TABLE IF NOT EXISTS contact_tags (
    contact_id UUID NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    tag TEXT NOT NULL,
    PRIMARY KEY (contact_id, tag)
);
CREATE INDEX idx_contact_tags_tag ON contact_tags(tag);

CREATE TABLE IF NOT EXISTS contact_custom_fields (
    contact_id UUID NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    value TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (contact_id, key)
);
