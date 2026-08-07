package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultFailureThreshold   = 2
	DefaultFailureWindow      = 5 * time.Minute
	DefaultCooldown           = 5 * time.Minute
	DefaultProbeInterval      = 5 * time.Minute
	DefaultFirstOutputTimeout = 15 * time.Second
	DefaultStreamIdleTimeout  = 60 * time.Second
	DefaultRequestTimeout     = 5 * time.Minute
	DefaultMaxAttempts        = 4
	DefaultLogRetentionDays   = 30
)

const (
	minFailureWindow    = 30 * time.Second
	maxFailureWindow    = 24 * time.Hour
	minCooldown         = 30 * time.Second
	maxCooldown         = 24 * time.Hour
	minProbeInterval    = time.Minute
	maxProbeInterval    = 24 * time.Hour
	minWatchdogTimeout  = time.Second
	maxOutputWatchdog   = 10 * time.Minute
	maxRequestTimeout   = 30 * time.Minute
	maxLogRetentionDays = 365
)

type RuntimeSettings struct {
	FailureThreshold   int
	FailureWindow      time.Duration
	Cooldown           time.Duration
	ProbeInterval      time.Duration
	FirstOutputTimeout time.Duration
	StreamIdleTimeout  time.Duration
	RequestTimeout     time.Duration
	MaxAttempts        int
	LogRetentionDays   int
	Revision           int64
	UpdatedAt          time.Time
}

type RuntimeSettingsWrite struct {
	FailureThreshold   int
	FailureWindow      time.Duration
	Cooldown           time.Duration
	ProbeInterval      time.Duration
	FirstOutputTimeout time.Duration
	StreamIdleTimeout  time.Duration
	RequestTimeout     time.Duration
	MaxAttempts        int
	LogRetentionDays   int
}

func DefaultRuntimeSettingsWrite() RuntimeSettingsWrite {
	return RuntimeSettingsWrite{
		FailureThreshold: DefaultFailureThreshold, FailureWindow: DefaultFailureWindow,
		Cooldown: DefaultCooldown, ProbeInterval: DefaultProbeInterval,
		FirstOutputTimeout: DefaultFirstOutputTimeout, StreamIdleTimeout: DefaultStreamIdleTimeout,
		RequestTimeout: DefaultRequestTimeout, MaxAttempts: DefaultMaxAttempts,
		LogRetentionDays: DefaultLogRetentionDays,
	}
}

func ValidateRuntimeSettingsWrite(input RuntimeSettingsWrite) error {
	if input.FailureThreshold < 2 || input.FailureThreshold > 20 {
		return errors.New("failureThreshold must be between 2 and 20")
	}
	for _, item := range []struct {
		name    string
		value   time.Duration
		minimum time.Duration
		maximum time.Duration
	}{
		{"failureWindowMs", input.FailureWindow, minFailureWindow, maxFailureWindow},
		{"cooldownMs", input.Cooldown, minCooldown, maxCooldown},
		{"probeIntervalMs", input.ProbeInterval, minProbeInterval, maxProbeInterval},
		{"firstOutputTimeoutMs", input.FirstOutputTimeout, minWatchdogTimeout, maxOutputWatchdog},
		{"streamIdleTimeoutMs", input.StreamIdleTimeout, minWatchdogTimeout, maxOutputWatchdog},
		{"requestTimeoutMs", input.RequestTimeout, minWatchdogTimeout, maxRequestTimeout},
	} {
		if item.value < item.minimum || item.value > item.maximum {
			return fmt.Errorf("%s must be between %d and %d", item.name, item.minimum.Milliseconds(), item.maximum.Milliseconds())
		}
		if item.value%time.Millisecond != 0 {
			return fmt.Errorf("%s must be a whole number of milliseconds", item.name)
		}
	}
	if input.RequestTimeout < input.FirstOutputTimeout || input.RequestTimeout < input.StreamIdleTimeout {
		return errors.New("requestTimeoutMs must not be shorter than firstOutputTimeoutMs or streamIdleTimeoutMs")
	}
	if input.MaxAttempts < 1 || input.MaxAttempts > 20 {
		return errors.New("maxAttempts must be between 1 and 20")
	}
	if input.LogRetentionDays < 1 || input.LogRetentionDays > maxLogRetentionDays {
		return errors.New("logRetentionDays must be between 1 and 365")
	}
	return nil
}

func (s *Store) GetRuntimeSettings(ctx context.Context) (RuntimeSettings, error) {
	if s == nil || s.DB == nil {
		return RuntimeSettings{}, errors.New("runtime settings store is unavailable")
	}
	return scanRuntimeSettings(s.DB.QueryRowContext(ctx, runtimeSettingsSelect+` WHERE singleton_id=1`))
}

// InitializeRuntimeSettings applies deployment-provided bootstrap values only
// to the untouched migration seed. Reopening an existing database can never
// overwrite settings saved by an administrator.
func (s *Store) InitializeRuntimeSettings(
	ctx context.Context,
	input RuntimeSettingsWrite,
	now time.Time,
) (RuntimeSettings, error) {
	if err := ValidateRuntimeSettingsWrite(input); err != nil {
		return RuntimeSettings{}, err
	}
	if now.IsZero() {
		return RuntimeSettings{}, errors.New("runtime settings initialization time is required")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE runtime_settings SET
failure_threshold=?,failure_window_ms=?,cooldown_ms=?,probe_interval_ms=?,
first_output_timeout_ms=?,stream_idle_timeout_ms=?,request_timeout_ms=?,max_attempts=?,
log_retention_days=?,updated_at=?
WHERE singleton_id=1 AND revision=1 AND updated_at=0`, runtimeSettingsArgs(input, now.UTC().UnixMilli())...)
	if err != nil {
		return RuntimeSettings{}, err
	}
	settings, err := s.GetRuntimeSettings(ctx)
	if err != nil {
		return RuntimeSettings{}, err
	}
	if err := s.synchronizeModelMonitorIntervals(ctx, settings.ProbeInterval, now.UTC()); err != nil {
		return RuntimeSettings{}, err
	}
	return settings, nil
}

func (s *Store) UpdateRuntimeSettingsCAS(
	ctx context.Context,
	expectedRevision int64,
	input RuntimeSettingsWrite,
	now time.Time,
) (RuntimeSettings, error) {
	if expectedRevision <= 0 {
		return RuntimeSettings{}, errors.New("expected runtime settings revision must be positive")
	}
	if err := ValidateRuntimeSettingsWrite(input); err != nil {
		return RuntimeSettings{}, err
	}
	if now.IsZero() {
		return RuntimeSettings{}, errors.New("runtime settings update time is required")
	}
	nowMS := now.UTC().UnixMilli()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RuntimeSettings{}, err
	}
	defer tx.Rollback()

	args := runtimeSettingsArgs(input, nowMS)
	args = append(args, expectedRevision)
	result, err := tx.ExecContext(ctx, `UPDATE runtime_settings SET
failure_threshold=?,failure_window_ms=?,cooldown_ms=?,probe_interval_ms=?,
first_output_timeout_ms=?,stream_idle_timeout_ms=?,request_timeout_ms=?,max_attempts=?,
log_retention_days=?,revision=revision+1,updated_at=?
WHERE singleton_id=1 AND revision=?`, args...)
	if err != nil {
		return RuntimeSettings{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return RuntimeSettings{}, err
	}
	if changed != 1 {
		return RuntimeSettings{}, ErrRevisionConflict
	}

	// The global probe interval is also materialized on each selected model so
	// due-job queries remain indexable and restart-safe. Updating both records
	// in this transaction prevents the scheduler from observing a mixed policy.
	_, err = tx.ExecContext(ctx, `UPDATE model_monitor_settings SET
interval_ms=?,
next_probe_at=CASE WHEN enabled=1 THEN ?+? ELSE next_probe_at END,
revision=revision+1,updated_at=?
WHERE interval_ms<>?`, input.ProbeInterval.Milliseconds(), nowMS, input.ProbeInterval.Milliseconds(),
		nowMS, input.ProbeInterval.Milliseconds())
	if err != nil {
		return RuntimeSettings{}, err
	}

	updated, err := scanRuntimeSettings(tx.QueryRowContext(ctx, runtimeSettingsSelect+` WHERE singleton_id=1`))
	if err != nil {
		return RuntimeSettings{}, err
	}
	if err := tx.Commit(); err != nil {
		return RuntimeSettings{}, err
	}
	return updated, nil
}

func (s *Store) synchronizeModelMonitorIntervals(ctx context.Context, interval time.Duration, now time.Time) error {
	if interval < time.Millisecond || interval%time.Millisecond != 0 || now.IsZero() {
		return errors.New("global probe interval and synchronization time are invalid")
	}
	nowMS := now.UTC().UnixMilli()
	_, err := s.DB.ExecContext(ctx, `UPDATE model_monitor_settings SET
interval_ms=?,
next_probe_at=CASE WHEN enabled=1 THEN ?+? ELSE next_probe_at END,
revision=revision+1,updated_at=?
WHERE interval_ms<>?`, interval.Milliseconds(), nowMS, interval.Milliseconds(), nowMS, interval.Milliseconds())
	return err
}

func runtimeSettingsArgs(input RuntimeSettingsWrite, updatedAt int64) []any {
	return []any{
		input.FailureThreshold, input.FailureWindow.Milliseconds(), input.Cooldown.Milliseconds(),
		input.ProbeInterval.Milliseconds(), input.FirstOutputTimeout.Milliseconds(),
		input.StreamIdleTimeout.Milliseconds(), input.RequestTimeout.Milliseconds(), input.MaxAttempts,
		input.LogRetentionDays, updatedAt,
	}
}

const runtimeSettingsSelect = `SELECT failure_threshold,failure_window_ms,cooldown_ms,probe_interval_ms,
first_output_timeout_ms,stream_idle_timeout_ms,request_timeout_ms,max_attempts,log_retention_days,
revision,updated_at FROM runtime_settings`

func scanRuntimeSettings(row scanner) (RuntimeSettings, error) {
	var item RuntimeSettings
	var failureWindowMS, cooldownMS, probeIntervalMS int64
	var firstOutputMS, streamIdleMS, requestMS, updatedAtMS int64
	err := row.Scan(&item.FailureThreshold, &failureWindowMS, &cooldownMS, &probeIntervalMS,
		&firstOutputMS, &streamIdleMS, &requestMS, &item.MaxAttempts, &item.LogRetentionDays,
		&item.Revision, &updatedAtMS)
	if err != nil {
		return RuntimeSettings{}, err
	}
	var convertErr error
	if item.FailureWindow, convertErr = durationFromMilliseconds(failureWindowMS, "failure window"); convertErr != nil {
		return RuntimeSettings{}, convertErr
	}
	if item.Cooldown, convertErr = durationFromMilliseconds(cooldownMS, "cooldown"); convertErr != nil {
		return RuntimeSettings{}, convertErr
	}
	if item.ProbeInterval, convertErr = durationFromMilliseconds(probeIntervalMS, "probe interval"); convertErr != nil {
		return RuntimeSettings{}, convertErr
	}
	if item.FirstOutputTimeout, convertErr = durationFromMilliseconds(firstOutputMS, "first output timeout"); convertErr != nil {
		return RuntimeSettings{}, convertErr
	}
	if item.StreamIdleTimeout, convertErr = durationFromMilliseconds(streamIdleMS, "stream idle timeout"); convertErr != nil {
		return RuntimeSettings{}, convertErr
	}
	if item.RequestTimeout, convertErr = durationFromMilliseconds(requestMS, "request timeout"); convertErr != nil {
		return RuntimeSettings{}, convertErr
	}
	if item.Revision <= 0 || updatedAtMS < 0 {
		return RuntimeSettings{}, errors.New("runtime settings row is corrupt")
	}
	if updatedAtMS > 0 {
		item.UpdatedAt = time.UnixMilli(updatedAtMS).UTC()
	}
	return item, nil
}

func durationFromMilliseconds(value int64, name string) (time.Duration, error) {
	if value <= 0 || value > int64((1<<63-1)/time.Millisecond) {
		return 0, fmt.Errorf("%s milliseconds are invalid", name)
	}
	return time.Duration(value) * time.Millisecond, nil
}
