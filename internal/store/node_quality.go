package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type NodeQualityResult struct {
	NodeID       string
	CheckedAt    time.Time
	Success      bool
	LatencyMS    *int
	TestURL      string
	ErrorCode    string
	ErrorMessage string
}

func (s *Store) RecordNodeQualityResult(ctx context.Context, result NodeQualityResult) (NodeRecord, error) {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return NodeRecord{}, fmt.Errorf("begin node quality result: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	var latency any
	if result.LatencyMS != nil {
		latency = *result.LatencyMS
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO node_quality_checks(
    node_id, checked_at, success, latency_ms, test_url, error_code, error_message
) VALUES (?, ?, ?, ?, ?, ?, ?)
`,
		result.NodeID,
		result.CheckedAt.UTC().Format(time.RFC3339Nano),
		boolToInteger(result.Success),
		latency,
		result.TestURL,
		result.ErrorCode,
		result.ErrorMessage,
	); err != nil {
		if isForeignKeyConstraint(err) {
			return NodeRecord{}, ErrNotFound
		}
		return NodeRecord{}, fmt.Errorf("insert node quality result: %w", err)
	}

	if result.Success {
		state := "healthy"
		if result.LatencyMS != nil && *result.LatencyMS > 1500 {
			state = "degraded"
		}
		updateResult, err := transaction.ExecContext(ctx, `
UPDATE nodes
SET
    lifecycle_state = CASE WHEN lifecycle_state = 'disabled' THEN 'disabled' ELSE ? END,
    last_checked_at = ?,
    last_latency_ms = ?,
    consecutive_probe_failures = 0,
    version = version + 1
WHERE id = ? AND lifecycle_state <> 'retired'
`, state, result.CheckedAt.UTC().Format(time.RFC3339Nano), latency, result.NodeID)
		if err != nil {
			return NodeRecord{}, fmt.Errorf("update successful node quality: %w", err)
		}
		if err := requireOneRow(updateResult); err != nil {
			return NodeRecord{}, err
		}
	} else {
		updateResult, err := transaction.ExecContext(ctx, `
UPDATE nodes
SET
    lifecycle_state = CASE
        WHEN lifecycle_state = 'disabled' THEN 'disabled'
        WHEN consecutive_probe_failures + 1 >= 3 THEN 'quarantined'
        ELSE 'degraded'
    END,
    last_checked_at = ?,
    last_latency_ms = NULL,
    consecutive_probe_failures = consecutive_probe_failures + 1,
    version = version + 1
WHERE id = ? AND lifecycle_state <> 'retired'
`, result.CheckedAt.UTC().Format(time.RFC3339Nano), result.NodeID)
		if err != nil {
			return NodeRecord{}, fmt.Errorf("update failed node quality: %w", err)
		}
		if err := requireOneRow(updateResult); err != nil {
			return NodeRecord{}, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return NodeRecord{}, fmt.Errorf("commit node quality result: %w", err)
	}
	committed = true
	return s.GetNode(ctx, result.NodeID)
}

func requireOneRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read node quality update result: %w", err)
	}
	if rows != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DueNodeIDs(ctx context.Context, before time.Time, limit int) ([]string, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id
FROM nodes
WHERE lifecycle_state NOT IN ('disabled', 'retired')
  AND (last_checked_at IS NULL OR last_checked_at <= ?)
ORDER BY COALESCE(last_checked_at, first_seen_at) ASC, id ASC
LIMIT ?
`, before.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, fmt.Errorf("list due node checks: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan due node check: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due node checks: %w", err)
	}
	return ids, nil
}

func (s *Store) GetNodeConfig(ctx context.Context, id string) (NodeConfigRecord, error) {
	var record NodeConfigRecord
	err := s.db.QueryRowContext(ctx, `
SELECT id, fingerprint, display_name, protocol, lifecycle_state, canonical_config_encrypted
FROM nodes
WHERE id = ?
`, id).Scan(
		&record.ID,
		&record.Fingerprint,
		&record.DisplayName,
		&record.Protocol,
		&record.LifecycleState,
		&record.CanonicalConfigEncrypted,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeConfigRecord{}, ErrNotFound
	}
	if err != nil {
		return NodeConfigRecord{}, fmt.Errorf("get node config: %w", err)
	}
	return record, nil
}
