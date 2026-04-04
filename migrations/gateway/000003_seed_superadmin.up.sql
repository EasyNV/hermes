-- Seed: default superadmin for local dev.
-- Email: admin@hermes.local / Password: admin123
-- This user has no tenant/workspace — superadmin is platform-level.

INSERT INTO tenants (id, name, settings_json, max_numbers_per_proxy)
VALUES ('00000000-0000-0000-0000-000000000001', 'Default Tenant', '{}', 5)
ON CONFLICT (id) DO NOTHING;

INSERT INTO workspaces (id, tenant_id, name, settings_json, daily_cap)
VALUES ('00000000-0000-0000-0000-000000000010', '00000000-0000-0000-0000-000000000001', 'Default Workspace', '{}', 200)
ON CONFLICT (id) DO NOTHING;

INSERT INTO users (id, tenant_id, email, password_hash, role)
VALUES (
  '00000000-0000-0000-0000-000000000100',
  '00000000-0000-0000-0000-000000000001',
  'admin@hermes.local',
  '$2a$10$jrsu/vwpqV.MpeLEOakif.F5HN/cI7syck5rZi/zmT6l6vOIVqwj2',
  'superadmin'
)
ON CONFLICT (email) DO NOTHING;

INSERT INTO workspace_members (user_id, workspace_id, role)
VALUES (
  '00000000-0000-0000-0000-000000000100',
  '00000000-0000-0000-0000-000000000010',
  'workspace_admin'
)
ON CONFLICT (user_id, workspace_id) DO NOTHING;
