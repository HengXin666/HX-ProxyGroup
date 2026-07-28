package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

type NodeQualityCheckRecord struct {
	NodeQualityResult
	ID int64
}

func (s *Store) RecordNodeHealthResult(ctx context.Context, result NodeQualityResult) error {
	var latency any
	if result.LatencyMS != nil {
		latency = *result.LatencyMS
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO node_quality_checks(
    node_id, checked_at, success, latency_ms, test_url, error_code, error_message
) VALUES (?, ?, ?, ?, ?, ?, ?)
`, result.NodeID, result.CheckedAt.UTC().Format(time.RFC3339Nano), boolToInteger(result.Success), latency, result.TestURL, result.ErrorCode, result.ErrorMessage)
	if isForeignKeyConstraint(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("insert node health result: %w", err)
	}
	return nil
}

func (s *Store) ListLatestNodeHealthResults(ctx context.Context, nodeIDs []string) ([]NodeQualityCheckRecord, error) {
	if len(nodeIDs) == 0 {
		return []NodeQualityCheckRecord{}, nil
	}
	if len(nodeIDs) > 1000 {
		return nil, errors.New("at most 1000 node health result sets can be listed")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(nodeIDs)), ",")
	arguments := make([]any, len(nodeIDs))
	for index, id := range nodeIDs {
		arguments[index] = id
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT q.id, q.node_id, q.checked_at, q.success, q.latency_ms, q.test_url, q.error_code, q.error_message
FROM node_quality_checks q
WHERE q.node_id IN (`+placeholders+`)
  AND NOT EXISTS (
      SELECT 1 FROM node_quality_checks newer
      WHERE newer.node_id = q.node_id AND newer.test_url = q.test_url
        AND (newer.checked_at > q.checked_at OR (newer.checked_at = q.checked_at AND newer.id > q.id))
  )
ORDER BY q.node_id, q.test_url
`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list latest node health results: %w", err)
	}
	defer rows.Close()
	records := make([]NodeQualityCheckRecord, 0)
	for rows.Next() {
		var record NodeQualityCheckRecord
		var checkedAt string
		var success int
		var latency sql.NullInt64
		if err := rows.Scan(&record.ID, &record.NodeID, &checkedAt, &success, &latency, &record.TestURL, &record.ErrorCode, &record.ErrorMessage); err != nil {
			return nil, fmt.Errorf("scan latest node health result: %w", err)
		}
		record.Success = success == 1
		if latency.Valid {
			value := int(latency.Int64)
			record.LatencyMS = &value
		}
		record.CheckedAt, err = time.Parse(time.RFC3339Nano, checkedAt)
		if err != nil {
			return nil, fmt.Errorf("parse latest node health result time: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate latest node health results: %w", err)
	}
	return records, nil
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
