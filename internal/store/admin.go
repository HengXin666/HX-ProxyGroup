package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type AdminAccountRecord struct {
	Username        string
	PasswordHash    string
	PasswordVersion int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type AdminSessionRecord struct {
	TokenHash       string
	CSRFToken       string
	PasswordVersion int
	CreatedAt       time.Time
	LastUsedAt      time.Time
	ExpiresAt       time.Time
}

// GetAdminAccount returns the single administrator account or ErrNotFound
// when initial setup has not completed yet.
func (s *Store) GetAdminAccount(ctx context.Context) (AdminAccountRecord, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT username, password_hash, password_version, created_at, updated_at
FROM admin_account WHERE id = 1
`)
	var record AdminAccountRecord
	var createdAt, updatedAt string
	if err := row.Scan(&record.Username, &record.PasswordHash, &record.PasswordVersion, &createdAt, &updatedAt); errors.Is(err, sql.ErrNoRows) {
		return AdminAccountRecord{}, ErrNotFound
	} else if err != nil {
		return AdminAccountRecord{}, fmt.Errorf("get admin account: %w", err)
	}
	var err error
	if record.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return AdminAccountRecord{}, fmt.Errorf("parse admin created_at: %w", err)
	}
	if record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return AdminAccountRecord{}, fmt.Errorf("parse admin updated_at: %w", err)
	}
	return record, nil
}

// CreateAdminAccount creates the single administrator. A second insert fails
// with ErrConflict so setup cannot run twice.
func (s *Store) CreateAdminAccount(ctx context.Context, record AdminAccountRecord) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO admin_account (id, username, password_hash, password_version, created_at, updated_at)
VALUES (1, ?, ?, ?, ?, ?)
`,
		record.Username,
		record.PasswordHash,
		record.PasswordVersion,
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
		record.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return ErrConflict
		}
		return fmt.Errorf("create admin account: %w", err)
	}
	return nil
}

// UpdateAdminPassword replaces the password hash and bumps password_version.
// Sessions created against older password versions become invalid.
func (s *Store) UpdateAdminPassword(ctx context.Context, passwordHash string, now time.Time) (int, error) {
	row := s.db.QueryRowContext(ctx, `
UPDATE admin_account
SET password_hash = ?, password_version = password_version + 1, updated_at = ?
WHERE id = 1
RETURNING password_version
`, passwordHash, now.UTC().Format(time.RFC3339Nano))
	var version int
	if err := row.Scan(&version); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, fmt.Errorf("update admin password: %w", err)
	}
	return version, nil
}

// UpdateAdminUsername replaces the login name and bumps password_version so
// sessions created for the previous identity become invalid.
func (s *Store) UpdateAdminUsername(ctx context.Context, username string, now time.Time) (int, error) {
	row := s.db.QueryRowContext(ctx, `
UPDATE admin_account
SET username = ?, password_version = password_version + 1, updated_at = ?
WHERE id = 1
RETURNING password_version
`, username, now.UTC().Format(time.RFC3339Nano))
	var version int
	if err := row.Scan(&version); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, fmt.Errorf("update admin username: %w", err)
	}
	return version, nil
}

func (s *Store) CreateAdminSession(ctx context.Context, record AdminSessionRecord) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO admin_sessions (token_hash, csrf_token, password_version, created_at, last_used_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?)
`,
		record.TokenHash,
		record.CSRFToken,
		record.PasswordVersion,
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
		record.LastUsedAt.UTC().Format(time.RFC3339Nano),
		record.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("create admin session: %w", err)
	}
	return nil
}

func (s *Store) GetAdminSession(ctx context.Context, tokenHash string) (AdminSessionRecord, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT token_hash, csrf_token, password_version, created_at, last_used_at, expires_at
FROM admin_sessions WHERE token_hash = ?
`, tokenHash)
	var record AdminSessionRecord
	var createdAt, lastUsedAt, expiresAt string
	if err := row.Scan(&record.TokenHash, &record.CSRFToken, &record.PasswordVersion, &createdAt, &lastUsedAt, &expiresAt); errors.Is(err, sql.ErrNoRows) {
		return AdminSessionRecord{}, ErrNotFound
	} else if err != nil {
		return AdminSessionRecord{}, fmt.Errorf("get admin session: %w", err)
	}
	var err error
	if record.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return AdminSessionRecord{}, fmt.Errorf("parse session created_at: %w", err)
	}
	if record.LastUsedAt, err = time.Parse(time.RFC3339Nano, lastUsedAt); err != nil {
		return AdminSessionRecord{}, fmt.Errorf("parse session last_used_at: %w", err)
	}
	if record.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt); err != nil {
		return AdminSessionRecord{}, fmt.Errorf("parse session expires_at: %w", err)
	}
	return record, nil
}

func (s *Store) TouchAdminSession(ctx context.Context, tokenHash string, lastUsedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE admin_sessions SET last_used_at = ? WHERE token_hash = ?
`, lastUsedAt.UTC().Format(time.RFC3339Nano), tokenHash)
	if err != nil {
		return fmt.Errorf("touch admin session: %w", err)
	}
	return nil
}

func (s *Store) DeleteAdminSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete admin session: %w", err)
	}
	return nil
}

func (s *Store) DeleteAllAdminSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions`)
	if err != nil {
		return fmt.Errorf("delete admin sessions: %w", err)
	}
	return nil
}

// DeleteExpiredAdminSessions removes sessions past their absolute expiry or
// idle for longer than idleTimeout.
func (s *Store) DeleteExpiredAdminSessions(ctx context.Context, now time.Time, idleTimeout time.Duration) error {
	_, err := s.db.ExecContext(ctx, `
DELETE FROM admin_sessions WHERE expires_at <= ? OR last_used_at <= ?
`,
		now.UTC().Format(time.RFC3339Nano),
		now.Add(-idleTimeout).UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("delete expired admin sessions: %w", err)
	}
	return nil
}
