package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ResidentialChannelRecord binds one residential provider to one local entry
// point. Sticky channels additionally track which pooled session is currently
// selected so the exit IP can be rotated without recompiling the data plane.
type ResidentialChannelRecord struct {
	ID                 string
	Name               string
	ProviderID         string
	Mode               string
	ProxyGroupID       string
	ListenerID         string
	Region             string
	ActiveSessionIndex int
	RotateToken        string
	RotateCount        int
	LastRotatedAt      *time.Time
	LastExitIP         string
	PoolCreatedAt      *time.Time
	Enabled            bool
	Version            int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (s *Store) CreateResidentialChannel(
	ctx context.Context,
	record ResidentialChannelRecord,
) (ResidentialChannelRecord, error) {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO residential_channels(
    id, name, provider_id, mode, proxy_group_id, listener_id, region,
    active_session_index, rotate_token, rotate_count, last_rotated_at,
    last_exit_ip, pool_created_at, enabled, version, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		record.ID,
		record.Name,
		record.ProviderID,
		record.Mode,
		record.ProxyGroupID,
		record.ListenerID,
		record.Region,
		record.ActiveSessionIndex,
		record.RotateToken,
		record.RotateCount,
		nullableTimeString(record.LastRotatedAt),
		record.LastExitIP,
		nullableTimeString(record.PoolCreatedAt),
		boolToInteger(record.Enabled),
		record.Version,
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return ResidentialChannelRecord{}, ErrConflict
		}
		return ResidentialChannelRecord{}, fmt.Errorf("create residential channel: %w", err)
	}
	return record, nil
}

// SetResidentialChannelPoolCreatedAt records when the current session pool was
// rendered. It is runtime state, so it deliberately does not bump the channel
// configuration version.
func (s *Store) SetResidentialChannelPoolCreatedAt(
	ctx context.Context,
	id string,
	createdAt time.Time,
) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE residential_channels
SET pool_created_at = ?, updated_at = ?
WHERE id = ?
`,
		createdAt.UTC().Format(time.RFC3339Nano),
		createdAt.UTC().Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return fmt.Errorf("set residential channel pool timestamp: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read residential channel pool timestamp result: %w", err)
	}
	if rowsAffected != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetResidentialChannel(ctx context.Context, id string) (ResidentialChannelRecord, error) {
	record, err := scanResidentialChannel(s.db.QueryRowContext(ctx, residentialChannelSelect+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return ResidentialChannelRecord{}, ErrNotFound
	}
	if err != nil {
		return ResidentialChannelRecord{}, fmt.Errorf("get residential channel: %w", err)
	}
	return record, nil
}

// GetResidentialChannelByRotateToken resolves the public rotation token. The
// token itself is the credential for the consumer-facing rotate endpoint.
func (s *Store) GetResidentialChannelByRotateToken(
	ctx context.Context,
	token string,
) (ResidentialChannelRecord, error) {
	record, err := scanResidentialChannel(
		s.db.QueryRowContext(ctx, residentialChannelSelect+" WHERE rotate_token = ?", token),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ResidentialChannelRecord{}, ErrNotFound
	}
	if err != nil {
		return ResidentialChannelRecord{}, fmt.Errorf("get residential channel by rotate token: %w", err)
	}
	return record, nil
}

func (s *Store) ListResidentialChannels(ctx context.Context) ([]ResidentialChannelRecord, error) {
	rows, err := s.db.QueryContext(ctx, residentialChannelSelect+" ORDER BY created_at ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("list residential channels: %w", err)
	}
	defer rows.Close()
	records := make([]ResidentialChannelRecord, 0)
	for rows.Next() {
		record, err := scanResidentialChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan residential channel: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate residential channels: %w", err)
	}
	return records, nil
}

func (s *Store) UpdateResidentialChannel(
	ctx context.Context,
	record ResidentialChannelRecord,
	expectedVersion int,
) (ResidentialChannelRecord, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE residential_channels
SET
    name = ?, provider_id = ?, mode = ?, proxy_group_id = ?, listener_id = ?,
    region = ?, enabled = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?
`,
		record.Name,
		record.ProviderID,
		record.Mode,
		record.ProxyGroupID,
		record.ListenerID,
		record.Region,
		boolToInteger(record.Enabled),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
		record.ID,
		expectedVersion,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return ResidentialChannelRecord{}, ErrConflict
		}
		return ResidentialChannelRecord{}, fmt.Errorf("update residential channel: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return ResidentialChannelRecord{}, fmt.Errorf("read residential channel update result: %w", err)
	}
	if rowsAffected != 1 {
		if _, getErr := s.GetResidentialChannel(ctx, record.ID); errors.Is(getErr, ErrNotFound) {
			return ResidentialChannelRecord{}, ErrNotFound
		}
		return ResidentialChannelRecord{}, ErrConflict
	}
	return s.GetResidentialChannel(ctx, record.ID)
}

// SetResidentialChannelRotation records the outcome of a rotation. It does not
// bump the optimistic-concurrency version because rotation is runtime state
// rather than a configuration change, so concurrent editors are unaffected.
func (s *Store) SetResidentialChannelRotation(
	ctx context.Context,
	id string,
	sessionIndex int,
	exitIP string,
	rotatedAt time.Time,
) (ResidentialChannelRecord, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE residential_channels
SET
    active_session_index = ?,
    last_exit_ip = ?,
    rotate_count = rotate_count + 1,
    last_rotated_at = ?,
    updated_at = ?
WHERE id = ?
`,
		sessionIndex,
		exitIP,
		rotatedAt.UTC().Format(time.RFC3339Nano),
		rotatedAt.UTC().Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return ResidentialChannelRecord{}, fmt.Errorf("set residential channel rotation: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return ResidentialChannelRecord{}, fmt.Errorf("read residential channel rotation result: %w", err)
	}
	if rowsAffected != 1 {
		return ResidentialChannelRecord{}, ErrNotFound
	}
	return s.GetResidentialChannel(ctx, id)
}

// RotateResidentialChannelToken replaces the public rotation token so any
// previously distributed rotate URL stops working.
func (s *Store) RotateResidentialChannelToken(
	ctx context.Context,
	id string,
	token string,
) (ResidentialChannelRecord, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE residential_channels
SET rotate_token = ?, version = version + 1, updated_at = ?
WHERE id = ?
`, token, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		if isUniqueConstraint(err) {
			return ResidentialChannelRecord{}, ErrConflict
		}
		return ResidentialChannelRecord{}, fmt.Errorf("rotate residential channel token: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return ResidentialChannelRecord{}, fmt.Errorf("read residential channel token rotate result: %w", err)
	}
	if rowsAffected != 1 {
		return ResidentialChannelRecord{}, ErrNotFound
	}
	return s.GetResidentialChannel(ctx, id)
}

func (s *Store) DeleteResidentialChannel(ctx context.Context, id string, expectedVersion int) error {
	result, err := s.db.ExecContext(
		ctx,
		"DELETE FROM residential_channels WHERE id = ? AND version = ?",
		id,
		expectedVersion,
	)
	if err != nil {
		return fmt.Errorf("delete residential channel: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read residential channel delete result: %w", err)
	}
	if rowsAffected == 1 {
		return nil
	}
	if _, getErr := s.GetResidentialChannel(ctx, id); errors.Is(getErr, ErrNotFound) {
		return ErrNotFound
	}
	return ErrConflict
}

const residentialChannelSelect = `
SELECT
    id, name, provider_id, mode, proxy_group_id, listener_id, region,
    active_session_index, rotate_token, rotate_count, last_rotated_at,
    last_exit_ip, pool_created_at, enabled, version, created_at, updated_at
FROM residential_channels`

func scanResidentialChannel(source scanner) (ResidentialChannelRecord, error) {
	var record ResidentialChannelRecord
	var enabled int
	var lastRotatedAt string
	var poolCreatedAt string
	var createdAt string
	var updatedAt string
	if err := source.Scan(
		&record.ID,
		&record.Name,
		&record.ProviderID,
		&record.Mode,
		&record.ProxyGroupID,
		&record.ListenerID,
		&record.Region,
		&record.ActiveSessionIndex,
		&record.RotateToken,
		&record.RotateCount,
		&lastRotatedAt,
		&record.LastExitIP,
		&poolCreatedAt,
		&enabled,
		&record.Version,
		&createdAt,
		&updatedAt,
	); err != nil {
		return ResidentialChannelRecord{}, err
	}
	parsedCreatedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return ResidentialChannelRecord{}, fmt.Errorf("parse residential channel created_at: %w", err)
	}
	parsedUpdatedAt, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return ResidentialChannelRecord{}, fmt.Errorf("parse residential channel updated_at: %w", err)
	}
	if lastRotatedAt != "" {
		parsedRotatedAt, err := time.Parse(time.RFC3339Nano, lastRotatedAt)
		if err != nil {
			return ResidentialChannelRecord{}, fmt.Errorf("parse residential channel last_rotated_at: %w", err)
		}
		record.LastRotatedAt = &parsedRotatedAt
	}
	if poolCreatedAt != "" {
		parsedPoolCreatedAt, err := time.Parse(time.RFC3339Nano, poolCreatedAt)
		if err != nil {
			return ResidentialChannelRecord{}, fmt.Errorf("parse residential channel pool_created_at: %w", err)
		}
		record.PoolCreatedAt = &parsedPoolCreatedAt
	}
	record.Enabled = enabled != 0
	record.CreatedAt = parsedCreatedAt
	record.UpdatedAt = parsedUpdatedAt
	return record, nil
}
