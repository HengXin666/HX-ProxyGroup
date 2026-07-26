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
