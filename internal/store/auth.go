package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
)

type Admin struct {
	ID           int64
	Username     string
	PasswordHash string
}

func (s *Store) UpsertAdmin(ctx context.Context, username, passwordHash string) error {
	now := NowMS()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO admin_users(username, password_hash, created_at, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(username) DO UPDATE SET password_hash=excluded.password_hash, updated_at=excluded.updated_at`, username, passwordHash, now, now)
	return err
}

func (s *Store) AdminByUsername(ctx context.Context, username string) (Admin, error) {
	var admin Admin
	err := s.DB.QueryRowContext(ctx, "SELECT id, username, password_hash FROM admin_users WHERE username = ?", username).
		Scan(&admin.ID, &admin.Username, &admin.PasswordHash)
	return admin, err
}

func SessionDigest(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

func (s *Store) CreateSession(ctx context.Context, rawToken string, adminID, expiresAt int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO admin_sessions(token_hash, admin_id, expires_at, created_at)
VALUES (?, ?, ?, ?)`, SessionDigest(rawToken), adminID, expiresAt, NowMS())
	return err
}

func (s *Store) SessionAdmin(ctx context.Context, rawToken string, now int64) (Admin, error) {
	var admin Admin
	err := s.DB.QueryRowContext(ctx, `SELECT a.id, a.username, a.password_hash
FROM admin_sessions s JOIN admin_users a ON a.id=s.admin_id
WHERE s.token_hash=? AND s.expires_at>?`, SessionDigest(rawToken), now).
		Scan(&admin.ID, &admin.Username, &admin.PasswordHash)
	return admin, err
}

func (s *Store) DeleteSession(ctx context.Context, rawToken string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM admin_sessions WHERE token_hash=?", SessionDigest(rawToken))
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, now int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM admin_sessions WHERE expires_at<=?", now)
	return err
}

func IsNotFound(err error) bool { return err == sql.ErrNoRows }
