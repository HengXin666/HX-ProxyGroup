package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type ListenerRecord struct {
	ID                  string
	Name                string
	Kind                string
	BindAddress         string
	Port                int
	ProxyGroupID        string
	AuthMode            string
	AuthConfigEncrypted []byte
	ShareToken          string
	Enabled             bool
	Version             int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (s *Store) CreateListener(ctx context.Context, record ListenerRecord) (ListenerRecord, error) {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO listeners(
    id, name, listener_type, kind, bind_address, port, proxy_group_id,
    auth_policy_encrypted, auth_mode, auth_config_encrypted,
    share_token, enabled, version, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		record.ID,
		record.Name,
		record.Kind,
		record.Kind,
		record.BindAddress,
		record.Port,
		record.ProxyGroupID,
		record.AuthConfigEncrypted,
		record.AuthMode,
		record.AuthConfigEncrypted,
		record.ShareToken,
		boolToInteger(record.Enabled),
		record.Version,
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return ListenerRecord{}, ErrConflict
		}
		return ListenerRecord{}, fmt.Errorf("create listener: %w", err)
	}
	return record, nil
}

func (s *Store) GetListener(ctx context.Context, id string) (ListenerRecord, error) {
	record, err := scanListener(s.db.QueryRowContext(ctx, listenerSelect+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return ListenerRecord{}, ErrNotFound
	}
	if err != nil {
		return ListenerRecord{}, fmt.Errorf("get listener: %w", err)
	}
	return record, nil
}

func (s *Store) ListListeners(ctx context.Context) ([]ListenerRecord, error) {
	rows, err := s.db.QueryContext(ctx, listenerSelect+" ORDER BY created_at ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("list listeners: %w", err)
	}
	defer rows.Close()
	records := make([]ListenerRecord, 0)
	for rows.Next() {
		record, err := scanListener(rows)
		if err != nil {
			return nil, fmt.Errorf("scan listener: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate listeners: %w", err)
	}
	return records, nil
}

func (s *Store) UpdateListener(
	ctx context.Context,
	record ListenerRecord,
	expectedVersion int,
) (ListenerRecord, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE listeners
SET
    name = ?, listener_type = ?, kind = ?, bind_address = ?, port = ?, proxy_group_id = ?,
    auth_policy_encrypted = ?, auth_mode = ?, auth_config_encrypted = ?, enabled = ?,
    version = version + 1, updated_at = ?
WHERE id = ? AND version = ?
`,
		record.Name,
		record.Kind,
		record.Kind,
		record.BindAddress,
		record.Port,
		record.ProxyGroupID,
		record.AuthConfigEncrypted,
		record.AuthMode,
		record.AuthConfigEncrypted,
		boolToInteger(record.Enabled),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
		record.ID,
		expectedVersion,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return ListenerRecord{}, ErrConflict
		}
		return ListenerRecord{}, fmt.Errorf("update listener: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return ListenerRecord{}, fmt.Errorf("read listener update result: %w", err)
	}
	if rowsAffected != 1 {
		if _, getErr := s.GetListener(ctx, record.ID); errors.Is(getErr, ErrNotFound) {
			return ListenerRecord{}, ErrNotFound
		}
		return ListenerRecord{}, ErrConflict
	}
	return s.GetListener(ctx, record.ID)
}

func (s *Store) DeleteListener(ctx context.Context, id string, expectedVersion int) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM listeners WHERE id = ? AND version = ?", id, expectedVersion)
	if err != nil {
		return fmt.Errorf("delete listener: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read listener delete result: %w", err)
	}
	if rowsAffected == 1 {
		return nil
	}
	if _, getErr := s.GetListener(ctx, id); errors.Is(getErr, ErrNotFound) {
		return ErrNotFound
	}
	return ErrConflict
}

const listenerSelect = `
SELECT
    id, name, kind, bind_address, port, proxy_group_id, auth_mode,
    auth_config_encrypted, share_token, enabled, version, created_at, updated_at
FROM listeners`

// GetListenerByShareToken resolves the public subscription-export token.
func (s *Store) GetListenerByShareToken(ctx context.Context, token string) (ListenerRecord, error) {
	record, err := scanListener(s.db.QueryRowContext(ctx, listenerSelect+" WHERE share_token = ?", token))
	if errors.Is(err, sql.ErrNoRows) {
		return ListenerRecord{}, ErrNotFound
	}
	if err != nil {
		return ListenerRecord{}, fmt.Errorf("get listener by share token: %w", err)
	}
	return record, nil
}

// RotateListenerShareToken replaces the share token and bumps the version so
// previously distributed subscription links stop working.
func (s *Store) RotateListenerShareToken(ctx context.Context, id, token string) (ListenerRecord, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE listeners
SET share_token = ?, version = version + 1, updated_at = ?
WHERE id = ?
`, token, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		if isUniqueConstraint(err) {
			return ListenerRecord{}, ErrConflict
		}
		return ListenerRecord{}, fmt.Errorf("rotate listener share token: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return ListenerRecord{}, fmt.Errorf("read listener rotate result: %w", err)
	}
	if rowsAffected != 1 {
		return ListenerRecord{}, ErrNotFound
	}
	return s.GetListener(ctx, id)
}

func scanListener(source scanner) (ListenerRecord, error) {
	var record ListenerRecord
	var enabled int
	var createdAt string
	var updatedAt string
	if err := source.Scan(
		&record.ID,
		&record.Name,
		&record.Kind,
		&record.BindAddress,
		&record.Port,
		&record.ProxyGroupID,
		&record.AuthMode,
		&record.AuthConfigEncrypted,
		&record.ShareToken,
		&enabled,
		&record.Version,
		&createdAt,
		&updatedAt,
	); err != nil {
		return ListenerRecord{}, err
	}
	parsedCreatedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return ListenerRecord{}, fmt.Errorf("parse listener created_at: %w", err)
	}
	parsedUpdatedAt, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return ListenerRecord{}, fmt.Errorf("parse listener updated_at: %w", err)
	}
	record.Enabled = enabled != 0
	record.CreatedAt = parsedCreatedAt
	record.UpdatedAt = parsedUpdatedAt
	return record, nil
}
