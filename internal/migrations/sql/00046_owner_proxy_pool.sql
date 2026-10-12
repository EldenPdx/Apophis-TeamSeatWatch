-- +goose Up
-- Owner-managed proxy candidates. Secret-bearing URLs are encrypted with the
-- deployment key ring; only redacted facts are returned to the Owner UI.
CREATE TABLE tsw_proxy_pool_sources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind TEXT NOT NULL CHECK (kind IN ('subscription')),
    label TEXT NOT NULL DEFAULT '',
    url_key_version INTEGER NOT NULL,
    url_nonce BYTEA NOT NULL,
    url_ciphertext BYTEA NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    last_synced_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tsw_proxy_pool_one_subscription ON tsw_proxy_pool_sources(kind);

CREATE TABLE tsw_proxy_pool_nodes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id UUID REFERENCES tsw_proxy_pool_sources(id) ON DELETE SET NULL,
    endpoint_key_version INTEGER NOT NULL,
    endpoint_nonce BYTEA NOT NULL,
    endpoint_ciphertext BYTEA NOT NULL,
    endpoint_digest BYTEA NOT NULL,
    scheme TEXT NOT NULL CHECK (scheme IN ('http', 'https', 'socks5', 'socks5h')),
    display_host TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'healthy', 'isolated')),
    failure_reason TEXT NOT NULL DEFAULT '',
    checked_at TIMESTAMPTZ,
    healthy_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX tsw_proxy_pool_nodes_state_idx ON tsw_proxy_pool_nodes(state);
CREATE UNIQUE INDEX tsw_proxy_pool_nodes_endpoint_idx ON tsw_proxy_pool_nodes(endpoint_digest);

CREATE TABLE tsw_proxy_pool_settings (
    id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id = true),
    target_healthy INTEGER NOT NULL DEFAULT 5 CHECK (target_healthy BETWEEN 1 AND 1000),
    probe_concurrency INTEGER NOT NULL DEFAULT 5 CHECK (probe_concurrency BETWEEN 1 AND 100),
    task_concurrency INTEGER NOT NULL DEFAULT 5 CHECK (task_concurrency BETWEEN 1 AND 100),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT NOT NULL DEFAULT 'system'
);
INSERT INTO tsw_proxy_pool_settings(id) VALUES (true) ON CONFLICT (id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS tsw_proxy_pool_nodes;
DROP TABLE IF EXISTS tsw_proxy_pool_sources;
DROP TABLE IF EXISTS tsw_proxy_pool_settings;
