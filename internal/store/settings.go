package store

import "context"

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	var item Settings
	err := s.DB.QueryRowContext(ctx, `SELECT default_cooldown_seconds, failure_threshold,
failure_window_seconds, probe_interval_seconds, first_output_timeout_seconds, stream_idle_timeout_seconds,
request_deadline_seconds, max_attempts,
log_retention_days, updated_at FROM app_settings WHERE id=1`).Scan(
		&item.DefaultCooldownSeconds,
		&item.FailureThreshold,
		&item.FailureWindowSeconds,
		&item.ProbeIntervalSeconds,
		&item.FirstOutputTimeoutSeconds,
		&item.StreamIdleTimeoutSeconds,
		&item.RequestDeadlineSeconds,
		&item.MaxAttempts,
		&item.LogRetentionDays,
		&item.UpdatedAt,
	)
	return item, err
}

func (s *Store) UpdateSettings(ctx context.Context, item Settings) (Settings, error) {
	item.DefaultCooldownSeconds = clamp(item.DefaultCooldownSeconds, 1, 86400)
	item.FailureThreshold = clamp(item.FailureThreshold, 2, 10)
	item.FailureWindowSeconds = clamp(item.FailureWindowSeconds, 1, 86400)
	item.ProbeIntervalSeconds = clamp(item.ProbeIntervalSeconds, 30, 86400)
	item.FirstOutputTimeoutSeconds = clamp(item.FirstOutputTimeoutSeconds, 1, 600)
	item.StreamIdleTimeoutSeconds = clamp(item.StreamIdleTimeoutSeconds, 1, 3600)
	item.RequestDeadlineSeconds = clamp(item.RequestDeadlineSeconds, 1, 3600)
	item.MaxAttempts = clamp(item.MaxAttempts, 1, 20)
	item.LogRetentionDays = clamp(item.LogRetentionDays, 1, 3650)
	item.UpdatedAt = NowMS()
	_, err := s.DB.ExecContext(ctx, `UPDATE app_settings SET
default_cooldown_seconds=?, failure_threshold=?, failure_window_seconds=?,
probe_interval_seconds=?, first_output_timeout_seconds=?, stream_idle_timeout_seconds=?,
request_deadline_seconds=?, max_attempts=?,
log_retention_days=?, updated_at=? WHERE id=1`,
		item.DefaultCooldownSeconds, item.FailureThreshold, item.FailureWindowSeconds,
		item.ProbeIntervalSeconds, item.FirstOutputTimeoutSeconds, item.StreamIdleTimeoutSeconds,
		item.RequestDeadlineSeconds, item.MaxAttempts,
		item.LogRetentionDays, item.UpdatedAt)
	return item, err
}

func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
