package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{
		version: 1,
		name:    "initial_control_plane_schema",
		sql: `
CREATE TABLE system_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE subscriptions (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    source_type TEXT NOT NULL CHECK (source_type IN ('remote', 'inline', 'file')),
    source_config_encrypted BLOB,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    refresh_interval_seconds INTEGER NOT NULL DEFAULT 3600 CHECK (refresh_interval_seconds >= 60),
    last_success_snapshot_id TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE subscription_snapshots (
    id TEXT PRIMARY KEY,
    subscription_id TEXT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    content_hash TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    node_count INTEGER NOT NULL DEFAULT 0 CHECK (node_count >= 0),
    status TEXT NOT NULL CHECK (status IN ('candidate', 'active', 'failed', 'retired')),
    artifact_path TEXT,
    parse_summary_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX subscription_snapshots_content
    ON subscription_snapshots(subscription_id, content_hash);
CREATE INDEX subscription_snapshots_status
    ON subscription_snapshots(subscription_id, status, fetched_at DESC);

CREATE TABLE nodes (
    id TEXT PRIMARY KEY,
    fingerprint TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    protocol TEXT NOT NULL,
    canonical_config_encrypted BLOB NOT NULL,
    lifecycle_state TEXT NOT NULL CHECK (
        lifecycle_state IN ('candidate', 'healthy', 'degraded', 'quarantined', 'disabled', 'retired')
    ),
    first_seen_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    retired_at TEXT,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1)
) STRICT;

CREATE TABLE subscription_nodes (
    subscription_id TEXT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    snapshot_id TEXT NOT NULL REFERENCES subscription_snapshots(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    source_name TEXT NOT NULL,
    PRIMARY KEY (snapshot_id, node_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX subscription_nodes_subscription
    ON subscription_nodes(subscription_id, node_id);

CREATE TABLE proxy_groups (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    source_refs_json TEXT NOT NULL DEFAULT '[]',
    pipeline_spec_json TEXT NOT NULL DEFAULT '[]',
    strategy TEXT NOT NULL,
    session_policy_json TEXT NOT NULL DEFAULT '{}',
    empty_policy_json TEXT NOT NULL DEFAULT '{"mode":"fail-closed"}',
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE listeners (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    listener_type TEXT NOT NULL,
    bind_address TEXT NOT NULL,
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    proxy_group_id TEXT NOT NULL REFERENCES proxy_groups(id) ON DELETE RESTRICT,
    auth_policy_encrypted BLOB,
    transport_json TEXT NOT NULL DEFAULT '{}',
    tls_policy_encrypted BLOB,
    limits_json TEXT NOT NULL DEFAULT '{}',
    public_endpoint_json TEXT NOT NULL DEFAULT '{}',
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (bind_address, port)
) STRICT;

CREATE TABLE config_versions (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    content_hash TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('candidate', 'active', 'failed', 'rolled_back')),
    artifact_path TEXT NOT NULL,
    summary_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    applied_at TEXT
) STRICT;

CREATE UNIQUE INDEX config_versions_single_active
    ON config_versions(status)
    WHERE status = 'active';
`,
	},
	{
		version: 2,
		name:    "unique_subscription_names",
		sql: `
CREATE UNIQUE INDEX subscriptions_name ON subscriptions(name);
`,
	},
	{
		version: 3,
		name:    "subscription_snapshot_fetch_metadata",
		sql: `
ALTER TABLE subscription_snapshots
    ADD COLUMN fetch_metadata_json TEXT NOT NULL DEFAULT '{}';
`,
	},
	{
		version: 4,
		name:    "subscription_refresh_schedule",
		sql: `
ALTER TABLE subscriptions ADD COLUMN last_refresh_attempt_at TEXT;
ALTER TABLE subscriptions ADD COLUMN next_refresh_at TEXT;
UPDATE subscriptions SET next_refresh_at = created_at WHERE next_refresh_at IS NULL;
CREATE INDEX subscriptions_refresh_due
    ON subscriptions(enabled, next_refresh_at)
    WHERE enabled = 1;
`,
	},
	{
		version: 5,
		name:    "runtime_proxy_groups_and_listeners",
		sql: `
ALTER TABLE proxy_groups ADD COLUMN source_spec_json TEXT NOT NULL DEFAULT '{"node_ids":[],"include_direct":false}';
ALTER TABLE proxy_groups ADD COLUMN rule_pipeline_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE proxy_groups ADD COLUMN empty_behavior TEXT NOT NULL DEFAULT 'fail-closed'
    CHECK (empty_behavior IN ('fail-closed', 'direct'));
ALTER TABLE proxy_groups ADD COLUMN fallback_target_id TEXT REFERENCES proxy_groups(id) ON DELETE SET NULL;

ALTER TABLE listeners ADD COLUMN kind TEXT NOT NULL DEFAULT 'mixed'
    CHECK (kind IN ('http', 'socks', 'mixed'));
ALTER TABLE listeners ADD COLUMN auth_mode TEXT NOT NULL DEFAULT 'none'
    CHECK (auth_mode IN ('none', 'userpass'));
ALTER TABLE listeners ADD COLUMN auth_config_encrypted BLOB;
UPDATE listeners SET kind = listener_type;
UPDATE listeners
SET auth_mode = CASE WHEN auth_policy_encrypted IS NULL THEN 'none' ELSE 'userpass' END,
    auth_config_encrypted = auth_policy_encrypted;
`,
	},
	{
		version: 6,
		name:    "node_quality_checks",
		sql: `
ALTER TABLE nodes ADD COLUMN last_checked_at TEXT;
ALTER TABLE nodes ADD COLUMN last_latency_ms INTEGER;
ALTER TABLE nodes ADD COLUMN consecutive_probe_failures INTEGER NOT NULL DEFAULT 0
    CHECK (consecutive_probe_failures >= 0);

CREATE TABLE node_quality_checks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    checked_at TEXT NOT NULL,
    success INTEGER NOT NULL CHECK (success IN (0, 1)),
    latency_ms INTEGER,
    test_url TEXT NOT NULL,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX node_quality_checks_node_time
    ON node_quality_checks(node_id, checked_at DESC);
`,
	},
	{
		version: 7,
		name:    "admin_account_and_sessions",
		sql: `
CREATE TABLE admin_account (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    password_version INTEGER NOT NULL DEFAULT 1 CHECK (password_version >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE admin_sessions (
    token_hash TEXT PRIMARY KEY,
    csrf_token TEXT NOT NULL,
    password_version INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    last_used_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
) STRICT;

CREATE INDEX admin_sessions_expiry ON admin_sessions(expires_at);
`,
	},
	{
		version: 8,
		name:    "alerts_and_alert_settings",
		sql: `
CREATE TABLE alerts (
    id TEXT PRIMARY KEY,
    rule TEXT NOT NULL,
    target_id TEXT NOT NULL,
    target_name TEXT NOT NULL,
    severity TEXT NOT NULL CHECK (severity IN ('warning', 'critical')),
    status TEXT NOT NULL CHECK (status IN ('firing', 'resolved')),
    message TEXT NOT NULL,
    fired_at TEXT NOT NULL,
    resolved_at TEXT,
    last_notified_at TEXT,
    notify_count INTEGER NOT NULL DEFAULT 0 CHECK (notify_count >= 0),
    acknowledged INTEGER NOT NULL DEFAULT 0 CHECK (acknowledged IN (0, 1))
) STRICT;

CREATE UNIQUE INDEX alerts_single_firing
    ON alerts(rule, target_id)
    WHERE status = 'firing';
CREATE INDEX alerts_status_time ON alerts(status, fired_at DESC);

CREATE TABLE alert_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    smtp_config_encrypted BLOB,
    updated_at TEXT NOT NULL
) STRICT;
`,
	},
	{
		version: 9,
		name:    "subscription_cron_and_failure_reason",
		sql: `
ALTER TABLE subscriptions ADD COLUMN refresh_cron TEXT NOT NULL DEFAULT '';
ALTER TABLE subscriptions ADD COLUMN last_failure_json TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 10,
		name:    "listener_share_tokens",
		sql: `
ALTER TABLE listeners ADD COLUMN share_token TEXT NOT NULL DEFAULT '';
UPDATE listeners SET share_token = lower(hex(randomblob(16)));
CREATE UNIQUE INDEX listeners_share_token ON listeners(share_token);
`,
	},
	{
		version: 11,
		name:    "websocket_listener_protocols",
		sql: `
ALTER TABLE listeners RENAME TO listeners_v10;

CREATE TABLE listeners (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    listener_type TEXT NOT NULL,
    bind_address TEXT NOT NULL,
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    proxy_group_id TEXT NOT NULL REFERENCES proxy_groups(id) ON DELETE RESTRICT,
    auth_policy_encrypted BLOB,
    transport_json TEXT NOT NULL DEFAULT '{}',
    tls_policy_encrypted BLOB,
    limits_json TEXT NOT NULL DEFAULT '{}',
    public_endpoint_json TEXT NOT NULL DEFAULT '{}',
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'mixed'
        CHECK (kind IN ('http', 'socks', 'mixed', 'vless', 'vmess', 'trojan')),
    auth_mode TEXT NOT NULL DEFAULT 'none'
        CHECK (auth_mode IN ('none', 'userpass')),
    auth_config_encrypted BLOB,
    share_token TEXT NOT NULL DEFAULT '',
    UNIQUE (bind_address, port)
) STRICT;

INSERT INTO listeners(
    id, name, listener_type, bind_address, port, proxy_group_id,
    auth_policy_encrypted, transport_json, tls_policy_encrypted, limits_json,
    public_endpoint_json, enabled, version, created_at, updated_at, kind,
    auth_mode, auth_config_encrypted, share_token
)
SELECT
    id, name, listener_type, bind_address, port, proxy_group_id,
    auth_policy_encrypted, transport_json, tls_policy_encrypted, limits_json,
    public_endpoint_json, enabled, version, created_at, updated_at, kind,
    auth_mode, auth_config_encrypted, share_token
FROM listeners_v10;

DROP TABLE listeners_v10;
CREATE UNIQUE INDEX listeners_share_token ON listeners(share_token);
`,
	},
	{
		version: 12,
		name:    "aggregated_traffic_statistics",
		sql: `
CREATE TABLE traffic_totals (
    resource_type TEXT NOT NULL CHECK (resource_type IN ('listener', 'proxy_group', 'node')),
    resource_id TEXT NOT NULL,
    upload_bytes INTEGER NOT NULL DEFAULT 0 CHECK (upload_bytes >= 0),
    download_bytes INTEGER NOT NULL DEFAULT 0 CHECK (download_bytes >= 0),
    connection_count INTEGER NOT NULL DEFAULT 0 CHECK (connection_count >= 0),
    active_connections INTEGER NOT NULL DEFAULT 0 CHECK (active_connections >= 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (resource_type, resource_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE traffic_buckets (
    resource_type TEXT NOT NULL CHECK (resource_type IN ('listener', 'proxy_group', 'node')),
    resource_id TEXT NOT NULL,
    bucket_start TEXT NOT NULL,
    granularity_seconds INTEGER NOT NULL CHECK (granularity_seconds IN (60, 300, 3600)),
    upload_bytes INTEGER NOT NULL DEFAULT 0 CHECK (upload_bytes >= 0),
    download_bytes INTEGER NOT NULL DEFAULT 0 CHECK (download_bytes >= 0),
    connection_count INTEGER NOT NULL DEFAULT 0 CHECK (connection_count >= 0),
    peak_active_connections INTEGER NOT NULL DEFAULT 0 CHECK (peak_active_connections >= 0),
    PRIMARY KEY (resource_type, resource_id, bucket_start, granularity_seconds)
) STRICT, WITHOUT ROWID;

CREATE INDEX traffic_buckets_time
    ON traffic_buckets(granularity_seconds, bucket_start);

CREATE TRIGGER traffic_delete_listener AFTER DELETE ON listeners BEGIN
    DELETE FROM traffic_totals WHERE resource_type = 'listener' AND resource_id = OLD.id;
    DELETE FROM traffic_buckets WHERE resource_type = 'listener' AND resource_id = OLD.id;
END;
CREATE TRIGGER traffic_delete_proxy_group AFTER DELETE ON proxy_groups BEGIN
    DELETE FROM traffic_totals WHERE resource_type = 'proxy_group' AND resource_id = OLD.id;
    DELETE FROM traffic_buckets WHERE resource_type = 'proxy_group' AND resource_id = OLD.id;
END;
CREATE TRIGGER traffic_delete_node AFTER DELETE ON nodes BEGIN
    DELETE FROM traffic_totals WHERE resource_type = 'node' AND resource_id = OLD.id;
    DELETE FROM traffic_buckets WHERE resource_type = 'node' AND resource_id = OLD.id;
END;
`,
	},
	{
		version: 13,
		name:    "residential_providers_and_channels",
		sql: `
ALTER TABLE nodes ADD COLUMN origin TEXT NOT NULL DEFAULT 'subscription';
ALTER TABLE nodes ADD COLUMN origin_ref TEXT NOT NULL DEFAULT '';

CREATE INDEX nodes_origin ON nodes(origin, origin_ref);

CREATE TABLE residential_providers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    vendor TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK (protocol IN ('http', 'https', 'socks5')),
    gateway_host TEXT NOT NULL,
    gateway_port INTEGER NOT NULL CHECK (gateway_port BETWEEN 1 AND 65535),
    credentials_encrypted BLOB NOT NULL,
    username_template TEXT NOT NULL,
    rotation_mode TEXT NOT NULL CHECK (rotation_mode IN ('session-template', 'per-request', 'api-list')),
    session_ttl_seconds INTEGER NOT NULL DEFAULT 600 CHECK (session_ttl_seconds >= 0),
    pool_size INTEGER NOT NULL DEFAULT 8 CHECK (pool_size BETWEEN 1 AND 64),
    default_region TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE residential_channels (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    provider_id TEXT NOT NULL REFERENCES residential_providers(id) ON DELETE RESTRICT,
    mode TEXT NOT NULL CHECK (mode IN ('passthrough', 'sticky')),
    proxy_group_id TEXT NOT NULL REFERENCES proxy_groups(id) ON DELETE RESTRICT,
    listener_id TEXT NOT NULL REFERENCES listeners(id) ON DELETE RESTRICT,
    region TEXT NOT NULL DEFAULT '',
    active_session_index INTEGER NOT NULL DEFAULT 0 CHECK (active_session_index >= 0),
    rotate_token TEXT NOT NULL,
    rotate_count INTEGER NOT NULL DEFAULT 0 CHECK (rotate_count >= 0),
    last_rotated_at TEXT NOT NULL DEFAULT '',
    last_exit_ip TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX residential_channels_rotate_token ON residential_channels(rotate_token);
CREATE INDEX residential_channels_provider ON residential_channels(provider_id);
`,
	},
	{
		version: 14,
		name:    "residential_provider_api_url",
		sql: `
ALTER TABLE residential_providers ADD COLUMN api_url TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 15,
		name:    "residential_provider_chain_settings",
		sql: `
ALTER TABLE residential_providers
    ADD COLUMN upstream_proxy_group_id TEXT REFERENCES proxy_groups(id) ON DELETE RESTRICT;
`,
	},
	{
		version: 16,
		name:    "residential_pool_lifecycle",
		sql: `
ALTER TABLE residential_channels
    ADD COLUMN pool_created_at TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 17,
		name:    "residential_client_sessions",
		sql: `
CREATE TABLE residential_client_sessions (
    channel_id TEXT NOT NULL REFERENCES residential_channels(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    auth_username TEXT NOT NULL UNIQUE,
    auth_password_encrypted BLOB NOT NULL,
    session_index INTEGER NOT NULL DEFAULT -1 CHECK (session_index >= -1),
    route_mode TEXT NOT NULL CHECK (route_mode IN ('residential', 'direct')),
    rotate_count INTEGER NOT NULL DEFAULT 0 CHECK (rotate_count >= 0),
    last_rotated_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (channel_id, session_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX residential_client_sessions_channel
    ON residential_client_sessions(channel_id, route_mode, session_index);
`,
	},
	{
		version: 18,
		name:    "residential_client_upstream_route",
		sql: `
CREATE TABLE residential_client_sessions_v18 (
    channel_id TEXT NOT NULL REFERENCES residential_channels(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    auth_username TEXT NOT NULL UNIQUE,
    auth_password_encrypted BLOB NOT NULL,
    session_index INTEGER NOT NULL DEFAULT -1 CHECK (session_index >= -1),
    route_mode TEXT NOT NULL CHECK (route_mode IN ('residential', 'direct', 'upstream')),
    rotate_count INTEGER NOT NULL DEFAULT 0 CHECK (rotate_count >= 0),
    last_rotated_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (channel_id, session_id)
) STRICT, WITHOUT ROWID;

INSERT INTO residential_client_sessions_v18
SELECT * FROM residential_client_sessions;

DROP TABLE residential_client_sessions;
ALTER TABLE residential_client_sessions_v18 RENAME TO residential_client_sessions;

CREATE INDEX residential_client_sessions_channel
    ON residential_client_sessions(channel_id, route_mode, session_index);
`,
	},
	{
		version: 19,
		name:    "residential_sessions_lazy_allocation",
		sql: `
ALTER TABLE residential_providers
    ADD COLUMN session_expiry_policy TEXT NOT NULL DEFAULT 'rotate'
    CHECK (session_expiry_policy IN ('expire', 'rotate'));

ALTER TABLE residential_client_sessions
    ADD COLUMN node_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE residential_client_sessions
    ADD COLUMN allocated_at TEXT NOT NULL DEFAULT '';
ALTER TABLE residential_client_sessions
    ADD COLUMN expires_at TEXT NOT NULL DEFAULT '';

CREATE INDEX residential_client_sessions_expiry
    ON residential_client_sessions(route_mode, expires_at)
    WHERE route_mode = 'residential' AND expires_at <> '';
`,
	},
	{
		version: 20,
		name:    "residential_region_selection",
		sql: `
ALTER TABLE residential_providers
    ADD COLUMN default_region_mode TEXT NOT NULL DEFAULT 'fixed'
    CHECK (default_region_mode IN ('fixed', 'application-random'));
ALTER TABLE residential_providers
    ADD COLUMN default_random_regions TEXT NOT NULL DEFAULT '[]';
ALTER TABLE residential_channels
    ADD COLUMN region_mode TEXT NOT NULL DEFAULT 'fixed'
    CHECK (region_mode IN ('fixed', 'application-random'));
ALTER TABLE residential_channels
    ADD COLUMN random_regions TEXT NOT NULL DEFAULT '[]';
		`,
	},
	{
		version: 21,
		name:    "residential_client_country_pin",
		sql: `
ALTER TABLE residential_client_sessions
    ADD COLUMN country_code TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 22,
		name:    "admin_totp_and_session_step_up",
		sql: `
CREATE TABLE admin_two_factor (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    secret_encrypted BLOB NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

ALTER TABLE admin_sessions
    ADD COLUMN two_factor_verified_at TEXT;
`,
	},
	{
		version: 23,
		name:    "residential_public_path_boundary",
		sql: `
-- Residential data-plane listeners are internal implementation details. Keep
-- them on loopback even when an older release allowed a public bind address.
UPDATE listeners
SET bind_address = '127.0.0.1'
WHERE id IN (SELECT listener_id FROM residential_channels)
  AND bind_address NOT IN ('127.0.0.1', '::1');

-- HTTPS path routes terminate at the reverse proxy's standard 443 port. Older
-- releases could accidentally persist the internal Mihomo port as the public
-- endpoint; normalize only residential HTTPS endpoints during upgrade.
UPDATE listeners
SET public_endpoint_json = json_set(public_endpoint_json, '$.port', 443)
WHERE id IN (SELECT listener_id FROM residential_channels)
  AND json_valid(public_endpoint_json)
  AND json_extract(public_endpoint_json, '$.host') <> ''
  AND json_extract(public_endpoint_json, '$.tls') = 1;
`,
	},
	{
		version: 24,
		name:    "residential_declared_sessions",
		sql: `
-- A sticky channel now declares how many logical sessions it publishes so a
-- subscription can render a stable node list. Existing channels keep their
-- current on-demand behaviour until an administrator sets a session count.
ALTER TABLE residential_channels
    ADD COLUMN session_count INTEGER NOT NULL DEFAULT 0
    CHECK (session_count BETWEEN 0 AND 64);

-- Idle release is opt-in. Zero keeps every declared session allocated, which
-- is what makes a published subscription usable without a warm-up request.
ALTER TABLE residential_channels
    ADD COLUMN idle_release_seconds INTEGER NOT NULL DEFAULT 0
    CHECK (idle_release_seconds >= 0);

-- The subscription token is separate from rotate_token: a share link is
-- pasted into client configuration and spreads, while the control token can
-- spend provider quota through next.
ALTER TABLE residential_channels
    ADD COLUMN control_token TEXT NOT NULL DEFAULT '';

-- A channel may publish one loopback WebSocket entry point behind the reverse
-- proxy and one directly reachable TCP entry point at the same time.
ALTER TABLE residential_channels
    ADD COLUMN direct_listener_id TEXT REFERENCES listeners(id) ON DELETE RESTRICT;

-- Declared sessions are addressed by their ordinal so node names stay stable
-- across rotations. On-demand sessions created by older clients keep -1.
ALTER TABLE residential_client_sessions
    ADD COLUMN declared_index INTEGER NOT NULL DEFAULT -1
    CHECK (declared_index >= -1);
ALTER TABLE residential_client_sessions
    ADD COLUMN last_used_at TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX residential_client_sessions_declared
    ON residential_client_sessions(channel_id, declared_index)
    WHERE declared_index >= 0;
`,
	},
	{
		version: 25,
		name:    "residential_channel_traffic",
		sql: `
-- Residential allocations are replaced during IP rotation, so node rows are
-- not a durable accounting identity. Add the stable channel id as a traffic
-- resource and keep both WS and direct listener traffic in the same totals.
DROP TRIGGER IF EXISTS traffic_delete_listener;
DROP TRIGGER IF EXISTS traffic_delete_proxy_group;
DROP TRIGGER IF EXISTS traffic_delete_node;

ALTER TABLE traffic_totals RENAME TO traffic_totals_v24;
CREATE TABLE traffic_totals (
    resource_type TEXT NOT NULL CHECK (resource_type IN ('listener', 'proxy_group', 'node', 'residential_channel')),
    resource_id TEXT NOT NULL,
    upload_bytes INTEGER NOT NULL DEFAULT 0 CHECK (upload_bytes >= 0),
    download_bytes INTEGER NOT NULL DEFAULT 0 CHECK (download_bytes >= 0),
    connection_count INTEGER NOT NULL DEFAULT 0 CHECK (connection_count >= 0),
    active_connections INTEGER NOT NULL DEFAULT 0 CHECK (active_connections >= 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (resource_type, resource_id)
) STRICT, WITHOUT ROWID;
INSERT INTO traffic_totals SELECT * FROM traffic_totals_v24;
DROP TABLE traffic_totals_v24;

ALTER TABLE traffic_buckets RENAME TO traffic_buckets_v24;
CREATE TABLE traffic_buckets (
    resource_type TEXT NOT NULL CHECK (resource_type IN ('listener', 'proxy_group', 'node', 'residential_channel')),
    resource_id TEXT NOT NULL,
    bucket_start TEXT NOT NULL,
    granularity_seconds INTEGER NOT NULL CHECK (granularity_seconds IN (60, 300, 3600)),
    upload_bytes INTEGER NOT NULL DEFAULT 0 CHECK (upload_bytes >= 0),
    download_bytes INTEGER NOT NULL DEFAULT 0 CHECK (download_bytes >= 0),
    connection_count INTEGER NOT NULL DEFAULT 0 CHECK (connection_count >= 0),
    peak_active_connections INTEGER NOT NULL DEFAULT 0 CHECK (peak_active_connections >= 0),
    PRIMARY KEY (resource_type, resource_id, bucket_start, granularity_seconds)
) STRICT, WITHOUT ROWID;
INSERT INTO traffic_buckets SELECT * FROM traffic_buckets_v24;
DROP TABLE traffic_buckets_v24;
CREATE INDEX traffic_buckets_time
    ON traffic_buckets(granularity_seconds, bucket_start);

CREATE TRIGGER traffic_delete_listener AFTER DELETE ON listeners BEGIN
    DELETE FROM traffic_totals WHERE resource_type = 'listener' AND resource_id = OLD.id;
    DELETE FROM traffic_buckets WHERE resource_type = 'listener' AND resource_id = OLD.id;
END;
CREATE TRIGGER traffic_delete_proxy_group AFTER DELETE ON proxy_groups BEGIN
    DELETE FROM traffic_totals WHERE resource_type = 'proxy_group' AND resource_id = OLD.id;
    DELETE FROM traffic_buckets WHERE resource_type = 'proxy_group' AND resource_id = OLD.id;
END;
CREATE TRIGGER traffic_delete_node AFTER DELETE ON nodes BEGIN
    DELETE FROM traffic_totals WHERE resource_type = 'node' AND resource_id = OLD.id;
    DELETE FROM traffic_buckets WHERE resource_type = 'node' AND resource_id = OLD.id;
END;
CREATE TRIGGER traffic_delete_residential_channel AFTER DELETE ON residential_channels BEGIN
    DELETE FROM traffic_totals WHERE resource_type = 'residential_channel' AND resource_id = OLD.id;
    DELETE FROM traffic_buckets WHERE resource_type = 'residential_channel' AND resource_id = OLD.id;
END;
		`,
	},
	{
		version: 26,
		name:    "remove_unified_client_subscription",
		sql: `
DELETE FROM system_metadata WHERE key = 'client_subscription_token';
`,
	},
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    applied_at TEXT NOT NULL
) STRICT;
`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	currentVersion, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	latestVersion := migrations[len(migrations)-1].version
	if currentVersion > latestVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", currentVersion, latestVersion)
	}

	for _, migration := range migrations {
		if migration.version <= currentVersion {
			continue
		}
		if err := s.applyMigration(ctx, migration); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, migration migration) error {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", migration.version, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	if _, err := transaction.ExecContext(ctx, migration.sql); err != nil {
		return fmt.Errorf("apply migration %d (%s): %w", migration.version, migration.name, err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO schema_migrations(version, name, applied_at)
VALUES (?, ?, ?)
`, migration.version, migration.name, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record migration %d: %w", migration.version, err)
	}
	if _, err := transaction.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", migration.version)); err != nil {
		return fmt.Errorf("set sqlite user_version %d: %w", migration.version, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", migration.version, err)
	}
	committed = true
	return nil
}
