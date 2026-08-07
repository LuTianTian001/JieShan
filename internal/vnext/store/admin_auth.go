package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/vnext/adminauth"
)

func (s *Store) GetAdminUser(ctx context.Context, username string) (adminauth.AdminUser, error) {
	return scanAdminUser(s.DB.QueryRowContext(ctx, `
SELECT id,username,password_hash,password_changed_at,revision,created_at,updated_at
FROM admin_users WHERE username=? COLLATE NOCASE`, strings.TrimSpace(username)))
}

func (s *Store) EnsureAdminUser(ctx context.Context, input adminauth.AdminUser) (adminauth.AdminUser, bool, error) {
	if err := validateAdminUser(input); err != nil {
		return adminauth.AdminUser{}, false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return adminauth.AdminUser{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO admin_users(
 id,username,password_hash,password_changed_at,revision,created_at,updated_at
) VALUES (?,?,?,?,?,?,?)`,
		input.ID, input.Username, input.PasswordHash, input.PasswordChangedAt,
		input.Revision, input.CreatedAt, input.UpdatedAt,
	)
	if err != nil {
		return adminauth.AdminUser{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return adminauth.AdminUser{}, false, err
	}
	admin, err := scanAdminUser(tx.QueryRowContext(ctx, `
SELECT id,username,password_hash,password_changed_at,revision,created_at,updated_at
FROM admin_users WHERE username=? COLLATE NOCASE`, adminauth.AdminUsername))
	if err != nil {
		return adminauth.AdminUser{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return adminauth.AdminUser{}, false, err
	}
	return admin, affected == 1, nil
}

func (s *Store) CreateAdminSession(ctx context.Context, input adminauth.Session, expectedAdminRevision int64) error {
	if err := validateAdminSession(input); err != nil {
		return err
	}
	if expectedAdminRevision <= 0 {
		return errors.New("administrator revision is required")
	}
	result, err := s.DB.ExecContext(ctx, `
INSERT INTO admin_sessions(token_hash,admin_user_id,csrf_hash,expires_at,last_seen_at,created_at)
SELECT ?,?,?,?,?,? FROM admin_users WHERE id=? AND revision=?`,
		input.TokenHash[:], input.AdminUserID, input.CSRFHash[:], input.ExpiresAt, input.LastSeenAt, input.CreatedAt,
		input.AdminUserID, expectedAdminRevision,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return adminauth.ErrAdminRevisionConflict
	}
	return nil
}

func (s *Store) ChangeAdminPassword(
	ctx context.Context,
	adminUserID int64,
	expectedRevision int64,
	passwordHash string,
	changedAt int64,
	retainedSession [32]byte,
) error {
	if adminUserID != 1 || expectedRevision <= 0 || changedAt <= 0 ||
		!strings.HasPrefix(passwordHash, "$argon2id$") || len(passwordHash) > 512 {
		return errors.New("administrator password change is invalid")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE admin_users
SET password_hash=?,password_changed_at=?,revision=revision+1,updated_at=?
WHERE id=? AND revision=?`, passwordHash, changedAt, changedAt, adminUserID, expectedRevision)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return adminauth.ErrAdminRevisionConflict
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM admin_sessions WHERE admin_user_id=? AND token_hash<>?`, adminUserID, retainedSession[:]); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetAdminSession(ctx context.Context, tokenHash [32]byte) (adminauth.Session, error) {
	var session adminauth.Session
	var storedTokenHash, csrfHash []byte
	err := s.DB.QueryRowContext(ctx, `
SELECT s.token_hash,s.admin_user_id,a.username,s.csrf_hash,s.expires_at,s.last_seen_at,s.created_at
FROM admin_sessions s
JOIN admin_users a ON a.id=s.admin_user_id
WHERE s.token_hash=?`, tokenHash[:]).Scan(
		&storedTokenHash, &session.AdminUserID, &session.AdminUsername, &csrfHash,
		&session.ExpiresAt, &session.LastSeenAt, &session.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return adminauth.Session{}, adminauth.ErrSessionNotFound
	}
	if err != nil {
		return adminauth.Session{}, err
	}
	if len(storedTokenHash) != len(session.TokenHash) || len(csrfHash) != len(session.CSRFHash) {
		return adminauth.Session{}, errors.New("stored administrator session digest is invalid")
	}
	copy(session.TokenHash[:], storedTokenHash)
	copy(session.CSRFHash[:], csrfHash)
	return session, nil
}

func (s *Store) TouchAdminSession(ctx context.Context, tokenHash [32]byte, lastSeenAt int64) error {
	if lastSeenAt <= 0 {
		return errors.New("administrator session touch time is required")
	}
	result, err := s.DB.ExecContext(ctx, `
UPDATE admin_sessions SET last_seen_at=?
WHERE token_hash=? AND last_seen_at<? AND expires_at>=?`, lastSeenAt, tokenHash[:], lastSeenAt, lastSeenAt)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var expiresAt int64
	err = s.DB.QueryRowContext(ctx, `SELECT expires_at FROM admin_sessions WHERE token_hash=?`, tokenHash[:]).Scan(&expiresAt)
	if errors.Is(err, sql.ErrNoRows) || expiresAt < lastSeenAt {
		return adminauth.ErrSessionNotFound
	}
	return err
}

func (s *Store) DeleteAdminSession(ctx context.Context, tokenHash [32]byte) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash=?`, tokenHash[:])
	return err
}

func (s *Store) DeleteExpiredAdminSessions(ctx context.Context, now int64) (int64, error) {
	if now <= 0 {
		return 0, errors.New("administrator session cleanup time is required")
	}
	result, err := s.DB.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at<=?`, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func scanAdminUser(row scanner) (adminauth.AdminUser, error) {
	var admin adminauth.AdminUser
	err := row.Scan(
		&admin.ID, &admin.Username, &admin.PasswordHash, &admin.PasswordChangedAt,
		&admin.Revision, &admin.CreatedAt, &admin.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return adminauth.AdminUser{}, adminauth.ErrAdminNotFound
	}
	return admin, err
}

func validateAdminUser(input adminauth.AdminUser) error {
	if input.ID != 1 || input.Username != adminauth.AdminUsername {
		return errors.New("only the VNext admin identity can be initialized")
	}
	if !strings.HasPrefix(input.PasswordHash, "$argon2id$") || len(input.PasswordHash) > 512 {
		return errors.New("administrator password hash is invalid")
	}
	if input.Revision <= 0 || input.CreatedAt <= 0 || input.PasswordChangedAt < input.CreatedAt ||
		input.UpdatedAt < input.PasswordChangedAt {
		return errors.New("administrator timestamps or revision are invalid")
	}
	return nil
}

func validateAdminSession(input adminauth.Session) error {
	if input.AdminUserID != 1 || input.AdminUsername != "" && input.AdminUsername != adminauth.AdminUsername {
		return errors.New("administrator session identity is invalid")
	}
	if input.CreatedAt <= 0 || input.ExpiresAt <= input.CreatedAt ||
		input.LastSeenAt < input.CreatedAt || input.LastSeenAt > input.ExpiresAt {
		return fmt.Errorf("administrator session timestamps are invalid")
	}
	return nil
}

var _ adminauth.Repository = (*Store)(nil)
