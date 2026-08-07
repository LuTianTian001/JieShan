package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/runtimeconfig"
)

const configRevisionTopic = "runtime_config_changed"

func (s *Store) LatestConfigRevision(ctx context.Context) (runtimeconfig.RevisionEvent, error) {
	if s == nil || s.DB == nil {
		return runtimeconfig.RevisionEvent{}, errors.New("configuration revision store is unavailable")
	}
	return scanConfigRevision(s.DB.QueryRowContext(ctx, `SELECT o.id,r.revision,r.reason,r.created_at
FROM config_outbox o JOIN config_revisions r ON r.revision=o.revision
WHERE o.topic=? ORDER BY o.id DESC LIMIT 1`, configRevisionTopic))
}

func (s *Store) PollConfigRevisions(
	ctx context.Context,
	after runtimeconfig.RevisionCursor,
	limit int,
) ([]runtimeconfig.RevisionEvent, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("configuration revision store is unavailable")
	}
	if after < 0 {
		return nil, errors.New("configuration revision cursor cannot be negative")
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New("configuration revision poll limit must be between 1 and 1000")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT o.id,r.revision,r.reason,r.created_at
FROM config_outbox o JOIN config_revisions r ON r.revision=o.revision
WHERE o.topic=? AND o.id>? ORDER BY o.id LIMIT ?`, configRevisionTopic, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]runtimeconfig.RevisionEvent, 0)
	for rows.Next() {
		event, err := scanConfigRevision(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

// ListConfigRevisions returns the newest durable configuration generations
// first. It is an administrative history view and never advances a runtime
// poller's acknowledgement cursor.
func (s *Store) ListConfigRevisions(ctx context.Context, limit int) ([]runtimeconfig.RevisionEvent, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("configuration revision store is unavailable")
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New("configuration revision history limit must be between 1 and 1000")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT o.id,r.revision,r.reason,r.created_at
FROM config_outbox o JOIN config_revisions r ON r.revision=o.revision
WHERE o.topic=? ORDER BY r.revision DESC LIMIT ?`, configRevisionTopic, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]runtimeconfig.RevisionEvent, 0, limit)
	for rows.Next() {
		event, err := scanConfigRevision(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) EnqueueConfigRevision(
	ctx context.Context,
	reason string,
	now time.Time,
) (runtimeconfig.RevisionEvent, error) {
	if s == nil || s.DB == nil {
		return runtimeconfig.RevisionEvent{}, errors.New("configuration revision store is unavailable")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return runtimeconfig.RevisionEvent{}, err
	}
	defer tx.Rollback()
	event, err := s.EnqueueConfigRevisionTx(ctx, tx, reason, now)
	if err != nil {
		return runtimeconfig.RevisionEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return runtimeconfig.RevisionEvent{}, err
	}
	return event, nil
}

// EnqueueConfigRevisionTx records the durable generation and its outbox event
// in the caller's control-plane transaction. The revision becomes visible to
// pollers if and only if the configuration write commits.
func (s *Store) EnqueueConfigRevisionTx(
	ctx context.Context,
	tx *sql.Tx,
	reason string,
	now time.Time,
) (runtimeconfig.RevisionEvent, error) {
	if s == nil || s.DB == nil || tx == nil {
		return runtimeconfig.RevisionEvent{}, errors.New("configuration revision transaction is unavailable")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 256 {
		return runtimeconfig.RevisionEvent{}, errors.New("configuration revision reason must be between 1 and 256 bytes")
	}
	if now.IsZero() {
		return runtimeconfig.RevisionEvent{}, errors.New("configuration revision time is required")
	}
	createdAt := now.UTC()
	var revision int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO config_revisions(reason,created_at) VALUES (?,?)
RETURNING revision`, reason, createdAt.UnixMilli()).Scan(&revision); err != nil {
		return runtimeconfig.RevisionEvent{}, fmt.Errorf("insert configuration revision: %w", err)
	}
	var cursor int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO config_outbox(revision,topic,created_at) VALUES (?,?,?)
RETURNING id`, revision, configRevisionTopic, createdAt.UnixMilli()).Scan(&cursor); err != nil {
		return runtimeconfig.RevisionEvent{}, fmt.Errorf("insert configuration outbox event: %w", err)
	}
	return runtimeconfig.RevisionEvent{
		Cursor: runtimeconfig.RevisionCursor(cursor), Revision: revision, Reason: reason, CreatedAt: createdAt,
	}, nil
}

func scanConfigRevision(row scanner) (runtimeconfig.RevisionEvent, error) {
	var event runtimeconfig.RevisionEvent
	var cursor, createdAt int64
	if err := row.Scan(&cursor, &event.Revision, &event.Reason, &createdAt); err != nil {
		return runtimeconfig.RevisionEvent{}, err
	}
	if cursor <= 0 || event.Revision <= 0 || strings.TrimSpace(event.Reason) == "" || createdAt < 0 {
		return runtimeconfig.RevisionEvent{}, errors.New("stored configuration revision is invalid")
	}
	event.Cursor = runtimeconfig.RevisionCursor(cursor)
	event.CreatedAt = time.UnixMilli(createdAt).UTC()
	return event, nil
}

var _ runtimeconfig.RevisionRepository = (*Store)(nil)
