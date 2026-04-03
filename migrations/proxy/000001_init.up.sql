CREATE TABLE IF NOT EXISTS proxies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    host TEXT NOT NULL,
    port INT NOT NULL,
    username TEXT NOT NULL DEFAULT '',
    password TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL CHECK (type IN ('socks5', 'http')) DEFAULT 'socks5',
    status TEXT NOT NULL CHECK (status IN ('active', 'dead', 'flagged')) DEFAULT 'active',
    ban_count INT NOT NULL DEFAULT 0,
    assigned_count INT NOT NULL DEFAULT 0,
    last_health_check TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, host, port)
);
CREATE INDEX idx_proxies_tenant ON proxies(tenant_id);
CREATE INDEX idx_proxies_tenant_status ON proxies(tenant_id, status);
