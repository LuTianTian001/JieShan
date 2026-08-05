package store

import (
	"context"
	"fmt"
)

const interruptedRequestMessage = "request interrupted by service restart"

type orphanedReservation struct {
	requestID       string
	downstreamKeyID int64
	amountMicroUSD  int64
}

// RecoverRunningRequests is intended to run once at single-instance startup,
// before the gateway accepts traffic. It atomically releases every outstanding
// reservation owned by an interrupted request and marks those requests failed.
func (s *Store) RecoverRunningRequests(ctx context.Context, recoveredAt int64) (int64, error) {
	if recoveredAt <= 0 {
		recoveredAt = NowMS()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT l.id,q.downstream_key_id,
SUM(CASE
  WHEN q.entry_type='reserve' THEN q.amount_micro_usd
  WHEN q.entry_type IN ('settle','release') THEN -q.amount_micro_usd
  ELSE 0
END) AS outstanding_micro_usd
FROM request_logs l
JOIN quota_ledger q ON q.request_id=l.id
WHERE l.status='running'
GROUP BY l.id,q.downstream_key_id
HAVING outstanding_micro_usd<>0
ORDER BY l.id,q.downstream_key_id`)
	if err != nil {
		return 0, err
	}
	reservations := make([]orphanedReservation, 0)
	for rows.Next() {
		var item orphanedReservation
		if err := rows.Scan(&item.requestID, &item.downstreamKeyID, &item.amountMicroUSD); err != nil {
			rows.Close()
			return 0, err
		}
		if item.amountMicroUSD < 0 {
			rows.Close()
			return 0, fmt.Errorf("%w: request %q has a negative outstanding reservation", ErrInvalidQuotaState, item.requestID)
		}
		reservations = append(reservations, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, item := range reservations {
		result, err := tx.ExecContext(ctx, `UPDATE downstream_keys
SET reserved_micro_usd=reserved_micro_usd-?,updated_at=?
WHERE id=? AND reserved_micro_usd>=?`, item.amountMicroUSD, recoveredAt, item.downstreamKeyID, item.amountMicroUSD)
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if changed != 1 {
			return 0, fmt.Errorf("%w: cannot release %d micro-USD for request %q", ErrInvalidQuotaState, item.amountMicroUSD, item.requestID)
		}
		if err := insertQuotaLedger(ctx, tx, item.downstreamKeyID, item.requestID, "release", item.amountMicroUSD, recoveredAt); err != nil {
			return 0, err
		}
	}

	result, err := tx.ExecContext(ctx, `UPDATE request_logs
SET status='failed',
    duration_ms=CASE WHEN started_at<=? THEN ?-started_at ELSE 0 END,
    error_message=?,
    finished_at=?
WHERE status='running'`, recoveredAt, recoveredAt, interruptedRequestMessage, recoveredAt)
	if err != nil {
		return 0, err
	}
	recovered, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return recovered, nil
}
