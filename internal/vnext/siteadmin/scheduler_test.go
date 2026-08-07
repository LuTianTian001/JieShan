package siteadmin

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSchedulerRefreshesOnlyDueEnabledConnectionsAndBoundsUsagePages(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Minute)
	repository := syncCandidateRepositoryFake{items: []SyncCandidate{
		{SiteID: 1, Enabled: true, SecretConfigured: true},
		{SiteID: 2, Enabled: false, SecretConfigured: true},
		{SiteID: 3, Enabled: true, SecretConfigured: true, LastBalanceRefreshAt: &recent, LastUsageRefreshAt: &recent},
	}}
	service := &accountSynchronizerFake{capabilities: Capabilities{Balance: true, Usage: true}}
	scheduler, err := NewScheduler(repository, service, SchedulerOptions{
		Now: func() time.Time { return now }, UsageMaxPages: 2, UsagePageLimit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(service.balanceSites) != 1 || service.balanceSites[0] != 1 {
		t.Fatalf("balance refresh sites = %+v", service.balanceSites)
	}
	if len(service.usage) != 2 || service.usage[0].siteID != 1 || service.usage[0].query.Cursor != "" ||
		service.usage[1].query.Cursor != "2" || service.usage[0].query.Limit != 25 {
		t.Fatalf("usage calls = %+v", service.usage)
	}
	if !service.usage[0].query.From.Equal(now.Add(-24*time.Hour)) || !service.usage[0].query.To.Equal(now) {
		t.Fatalf("initial usage window = %s - %s", service.usage[0].query.From, service.usage[0].query.To)
	}
}

func TestSchedulerOverlapsIncrementalUsageWindowForDeduplication(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	last := now.Add(-10 * time.Minute)
	repository := syncCandidateRepositoryFake{items: []SyncCandidate{{
		SiteID: 9, Enabled: true, SecretConfigured: true, LastUsageRefreshAt: &last,
	}}}
	service := &accountSynchronizerFake{capabilities: Capabilities{Usage: true}, stopAfterFirstPage: true}
	scheduler, err := NewScheduler(repository, service, SchedulerOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(service.usage) != 1 || !service.usage[0].query.From.Equal(last.Add(-time.Minute)) {
		t.Fatalf("incremental usage calls = %+v", service.usage)
	}
}

type syncCandidateRepositoryFake struct {
	items []SyncCandidate
	err   error
}

func (repository syncCandidateRepositoryFake) ListSyncCandidates(context.Context) ([]SyncCandidate, error) {
	return append([]SyncCandidate(nil), repository.items...), repository.err
}

type usageCall struct {
	siteID int64
	query  UsageQuery
}

type accountSynchronizerFake struct {
	mu                 sync.Mutex
	capabilities       Capabilities
	balanceSites       []int64
	usage              []usageCall
	stopAfterFirstPage bool
}

func (service *accountSynchronizerFake) Capabilities(context.Context, int64) (Capabilities, error) {
	return service.capabilities, nil
}

func (service *accountSynchronizerFake) RefreshBalance(_ context.Context, siteID int64) (BalanceResult, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.balanceSites = append(service.balanceSites, siteID)
	return BalanceResult{}, nil
}

func (service *accountSynchronizerFake) SyncUsagePage(_ context.Context, siteID int64, query UsageQuery) (UsageResult, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.usage = append(service.usage, usageCall{siteID: siteID, query: query})
	hasMore := !service.stopAfterFirstPage && query.Cursor == ""
	next := ""
	if hasMore {
		next = "2"
	}
	return UsageResult{Page: UsagePage{HasMore: hasMore, NextCursor: next, FetchedAt: time.Now().UTC()}}, nil
}
