package siteadmin

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	defaultAccountPollInterval     = time.Minute
	defaultBalanceRefreshInterval  = 15 * time.Minute
	defaultUsageRefreshInterval    = 5 * time.Minute
	defaultAccountOperationTimeout = 30 * time.Second
	defaultInitialUsageLookback    = 24 * time.Hour
	defaultUsagePageLimit          = 100
	defaultUsageMaxPages           = 3
)

type SyncCandidate struct {
	SiteID               int64
	Enabled              bool
	SecretConfigured     bool
	LastBalanceRefreshAt *time.Time
	LastUsageRefreshAt   *time.Time
}

type SyncCandidateRepository interface {
	ListSyncCandidates(context.Context) ([]SyncCandidate, error)
}

type AccountSynchronizer interface {
	Capabilities(context.Context, int64) (Capabilities, error)
	RefreshBalance(context.Context, int64) (BalanceResult, error)
	SyncUsagePage(context.Context, int64, UsageQuery) (UsageResult, error)
}

type SchedulerOptions struct {
	PollInterval         time.Duration
	BalanceInterval      time.Duration
	UsageInterval        time.Duration
	OperationTimeout     time.Duration
	InitialUsageLookback time.Duration
	UsagePageLimit       int
	UsageMaxPages        int
	Now                  func() time.Time
}

type Scheduler struct {
	repository SyncCandidateRepository
	service    AccountSynchronizer
	options    SchedulerOptions
}

func NewScheduler(
	repository SyncCandidateRepository,
	service AccountSynchronizer,
	options SchedulerOptions,
) (*Scheduler, error) {
	if repository == nil || service == nil {
		return nil, errors.New("site account scheduler repository and service are required")
	}
	options = normalizeSchedulerOptions(options)
	return &Scheduler{repository: repository, service: service, options: options}, nil
}

func (scheduler *Scheduler) Run(ctx context.Context) error {
	if scheduler == nil || ctx == nil {
		return errors.New("site account scheduler context is required")
	}
	_ = scheduler.RunOnce(ctx)
	ticker := time.NewTicker(scheduler.options.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			_ = scheduler.RunOnce(ctx)
		}
	}
}

// RunOnce is intentionally best effort per site. One malformed or unavailable
// management API must not prevent other balances and logs from refreshing.
func (scheduler *Scheduler) RunOnce(ctx context.Context) error {
	if scheduler == nil || ctx == nil {
		return errors.New("site account scheduler context is required")
	}
	candidates, err := scheduler.repository.ListSyncCandidates(ctx)
	if err != nil {
		return err
	}
	now := scheduler.options.Now().UTC()
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return nil
		}
		if candidate.SiteID <= 0 || !candidate.Enabled || !candidate.SecretConfigured {
			continue
		}
		operationCtx, cancel := context.WithTimeout(ctx, scheduler.options.OperationTimeout)
		capabilities, capabilityErr := scheduler.service.Capabilities(operationCtx, candidate.SiteID)
		cancel()
		if capabilityErr != nil {
			continue
		}
		if capabilities.Balance && refreshDue(candidate.LastBalanceRefreshAt, now, scheduler.options.BalanceInterval) {
			operationCtx, cancel = context.WithTimeout(ctx, scheduler.options.OperationTimeout)
			_, _ = scheduler.service.RefreshBalance(operationCtx, candidate.SiteID)
			cancel()
		}
		if capabilities.Usage && refreshDue(candidate.LastUsageRefreshAt, now, scheduler.options.UsageInterval) {
			scheduler.syncUsage(ctx, candidate, now)
		}
	}
	return nil
}

func (scheduler *Scheduler) syncUsage(ctx context.Context, candidate SyncCandidate, now time.Time) {
	from := now.Add(-scheduler.options.InitialUsageLookback)
	if candidate.LastUsageRefreshAt != nil && !candidate.LastUsageRefreshAt.IsZero() {
		from = candidate.LastUsageRefreshAt.UTC().Add(-time.Minute)
	}
	cursor := ""
	for pageIndex := 0; pageIndex < scheduler.options.UsageMaxPages; pageIndex++ {
		operationCtx, cancel := context.WithTimeout(ctx, scheduler.options.OperationTimeout)
		result, err := scheduler.service.SyncUsagePage(operationCtx, candidate.SiteID, UsageQuery{
			Cursor: cursor, From: from, To: now, Limit: scheduler.options.UsagePageLimit,
		})
		cancel()
		if err != nil || !result.Page.HasMore {
			return
		}
		next := strings.TrimSpace(result.Page.NextCursor)
		if next == "" || next == cursor {
			return
		}
		cursor = next
	}
}

func normalizeSchedulerOptions(options SchedulerOptions) SchedulerOptions {
	if options.PollInterval <= 0 {
		options.PollInterval = defaultAccountPollInterval
	}
	if options.BalanceInterval <= 0 {
		options.BalanceInterval = defaultBalanceRefreshInterval
	}
	if options.UsageInterval <= 0 {
		options.UsageInterval = defaultUsageRefreshInterval
	}
	if options.OperationTimeout <= 0 {
		options.OperationTimeout = defaultAccountOperationTimeout
	}
	if options.InitialUsageLookback <= 0 {
		options.InitialUsageLookback = defaultInitialUsageLookback
	}
	if options.UsagePageLimit < 1 || options.UsagePageLimit > 500 {
		options.UsagePageLimit = defaultUsagePageLimit
	}
	if options.UsageMaxPages < 1 || options.UsageMaxPages > 20 {
		options.UsageMaxPages = defaultUsageMaxPages
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return options
}

func refreshDue(last *time.Time, now time.Time, interval time.Duration) bool {
	return last == nil || last.IsZero() || !last.Add(interval).After(now)
}

var _ AccountSynchronizer = (*Service)(nil)
