// Package retention owns bounded background pruning of operational history.
// Accounting audit state remains owned by the store and is never discarded by
// this service.
package retention

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"time"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const (
	DefaultInterval = 24 * time.Hour
	DefaultTimeout  = time.Minute
)

type Repository interface {
	PruneOperationalHistory(context.Context, time.Time) (vnextstore.RetentionCleanupResult, error)
	Maintain(context.Context) error
}

type SettingsProvider interface {
	Current() vnextstore.RuntimeSettings
}

type Options struct {
	Interval time.Duration
	Timeout  time.Duration
	Now      func() time.Time
	Logger   *slog.Logger
}

type Service struct {
	repository Repository
	settings   SettingsProvider
	interval   time.Duration
	timeout    time.Duration
	now        func() time.Time
	logger     *slog.Logger
}

func New(repository Repository, settings SettingsProvider, options Options) (*Service, error) {
	if nilLike(repository) || nilLike(settings) {
		return nil, errors.New("retention repository and settings provider are required")
	}
	if options.Interval == 0 {
		options.Interval = DefaultInterval
	}
	if options.Interval < time.Millisecond {
		return nil, errors.New("retention interval must be at least one millisecond")
	}
	if options.Timeout == 0 {
		options.Timeout = DefaultTimeout
	}
	if options.Timeout < time.Millisecond {
		return nil, errors.New("retention timeout must be at least one millisecond")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Service{
		repository: repository,
		settings:   settings,
		interval:   options.Interval,
		timeout:    options.Timeout,
		now:        options.Now,
		logger:     options.Logger,
	}, nil
}

// Run performs one cleanup immediately, then repeats at the configured
// interval. Individual cleanup failures are warnings only: request routing
// must stay available even when maintenance is temporarily blocked.
func (service *Service) Run(ctx context.Context) error {
	if service == nil {
		return errors.New("retention service is unavailable")
	}
	if ctx == nil {
		return errors.New("retention context is required")
	}
	service.runPass(ctx)
	ticker := time.NewTicker(service.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			service.runPass(ctx)
		}
	}
}

func (service *Service) runPass(parent context.Context) {
	settings := service.settings.Current()
	if settings.LogRetentionDays < 1 || settings.LogRetentionDays > 365 {
		service.logger.Warn("operational history cleanup skipped",
			"retention_days", settings.LogRetentionDays)
		return
	}
	now := service.now().UTC()
	cutoff := now.Add(-time.Duration(settings.LogRetentionDays) * 24 * time.Hour)
	ctx, cancel := context.WithTimeout(parent, service.timeout)
	result, err := service.repository.PruneOperationalHistory(ctx, cutoff)
	cancel()
	if err != nil {
		service.logger.Warn("operational history cleanup failed", "error", err)
		return
	}
	if err := service.repository.Maintain(ctx); err != nil {
		service.logger.Warn("SQLite maintenance failed", "error", err)
	}
	service.logger.Debug("operational history cleanup completed",
		"cutoff_at", result.CutoffAt,
		"request_attempts_deleted", result.RequestAttemptsDeleted,
		"request_logs_deleted", result.RequestLogsDeleted,
		"ledger_protected_request_logs", result.LedgerProtectedRequestLogs,
		"probe_results_deleted", result.ModelProbeResultsDeleted,
		"probe_runs_deleted", result.ModelProbeRunsDeleted,
		"site_usage_records_deleted", result.SiteUsageRecordsDeleted,
		"site_balance_snapshots_deleted", result.SiteBalanceSnapshotsDeleted,
	)
}

func nilLike(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ Repository = (*vnextstore.Store)(nil)
