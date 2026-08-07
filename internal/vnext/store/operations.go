package store

import (
	"context"
	"errors"
	"strings"
)

// MeteringDegradation groups settled requests whose upstream response did not
// contain enough trustworthy usage data for official-price settlement.
type MeteringDegradation struct {
	Code             string
	AffectedRequests int64
	Since            int64
	LastSeenAt       int64
}

func (s *Store) SummarizeMeteringDegradation(
	ctx context.Context,
	from int64,
) ([]MeteringDegradation, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("metering degradation store is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("metering degradation context is required")
	}
	if from < 0 {
		return nil, errors.New("metering degradation lower bound cannot be negative")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT
COALESCE(NULLIF(trim(metering_error_code),''),'metering_unavailable'),
COUNT(*),MIN(COALESCE(finished_at,started_at)),MAX(COALESCE(finished_at,started_at))
FROM request_logs
WHERE metering_status='unavailable' AND COALESCE(finished_at,started_at)>=?
GROUP BY COALESCE(NULLIF(trim(metering_error_code),''),'metering_unavailable')
ORDER BY MAX(COALESCE(finished_at,started_at)) DESC`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]MeteringDegradation, 0)
	for rows.Next() {
		var item MeteringDegradation
		if err := rows.Scan(&item.Code, &item.AffectedRequests, &item.Since, &item.LastSeenAt); err != nil {
			return nil, err
		}
		item.Code = strings.ToLower(strings.TrimSpace(item.Code))
		if item.Code == "" || item.AffectedRequests <= 0 || item.Since < 0 || item.LastSeenAt < item.Since {
			return nil, errors.New("stored metering degradation summary is invalid")
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
