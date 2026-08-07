package siteadmin

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestSchedulerPersistsAndResumesUsageWindowPastPageLimit(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Minute)
	repository := newSyncCandidateRepositoryFake([]SyncCandidate{
		{SiteID: 1, Enabled: true, SecretConfigured: true},
		{SiteID: 2, Enabled: false, SecretConfigured: true},
		{SiteID: 3, Enabled: true, SecretConfigured: true, LastBalanceRefreshAt: &recent, UsageSyncThroughAt: &recent},
	})
	service := &accountSynchronizerFake{
		capabilities: Capabilities{Balance: true, Usage: true},
		repository:   repository,
	}
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
	if len(service.usage) != 2 || service.usage[0].window.Cursor != "" ||
		service.usage[1].window.Cursor != "2" || service.usage[0].limit != 25 {
		t.Fatalf("usage calls = %+v", service.usage)
	}
	if !service.usage[0].window.From.Equal(now.Add(-24*time.Hour)) || !service.usage[0].window.To.Equal(now) {
		t.Fatalf("initial usage window = %+v", service.usage[0].window)
	}
	window, ok, err := repository.NextUsageSyncWindow(t.Context(), 1)
	if err != nil || !ok || window.Cursor != "3" {
		t.Fatalf("persisted usage window = %+v, %v, %v", window, ok, err)
	}

	if err := scheduler.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(service.usage) != 3 || service.usage[2].window.ID != window.ID || service.usage[2].window.Cursor != "3" {
		t.Fatalf("resumed usage calls = %+v", service.usage)
	}
	if _, ok, err := repository.NextUsageSyncWindow(t.Context(), 1); err != nil || ok {
		t.Fatalf("completed usage window still pending: %v, %v", ok, err)
	}
}

func TestSchedulerQueuesNewUsageWindowWithoutDiscardingOlderBackfill(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	repository := newSyncCandidateRepositoryFake([]SyncCandidate{{SiteID: 9, Enabled: true, SecretConfigured: true}})
	service := &accountSynchronizerFake{capabilities: Capabilities{Usage: true}, repository: repository}
	current := now
	scheduler, err := NewScheduler(repository, service, SchedulerOptions{
		Now: func() time.Time { return current }, UsageInterval: 5 * time.Minute,
		UsageMaxPages: 2, UsagePageLimit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	oldWindow, ok, err := repository.NextUsageSyncWindow(t.Context(), 9)
	if err != nil || !ok || oldWindow.Cursor != "3" {
		t.Fatalf("old backfill = %+v, %v, %v", oldWindow, ok, err)
	}

	current = now.Add(5 * time.Minute)
	if err := scheduler.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(service.usage) != 4 || service.usage[2].window.ID != oldWindow.ID || service.usage[2].window.Cursor != "3" ||
		service.usage[3].window.ID == oldWindow.ID || service.usage[3].window.Cursor != "" {
		t.Fatalf("usage calls after new interval = %+v", service.usage)
	}
	newWindow, ok, err := repository.NextUsageSyncWindow(t.Context(), 9)
	if err != nil || !ok || newWindow.ID == oldWindow.ID || newWindow.Cursor != "2" {
		t.Fatalf("new pending window = %+v, %v, %v", newWindow, ok, err)
	}
	if !newWindow.From.Equal(now.Add(-time.Minute)) || !newWindow.To.Equal(current) {
		t.Fatalf("incremental window = %+v", newWindow)
	}
}

type syncCandidateRepositoryFake struct {
	mu      sync.Mutex
	items   []SyncCandidate
	windows map[int64][]UsageSyncWindow
	nextID  int64
	err     error
}

func newSyncCandidateRepositoryFake(items []SyncCandidate) *syncCandidateRepositoryFake {
	return &syncCandidateRepositoryFake{items: append([]SyncCandidate(nil), items...), windows: make(map[int64][]UsageSyncWindow)}
}

func (repository *syncCandidateRepositoryFake) ListSyncCandidates(context.Context) ([]SyncCandidate, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	items := append([]SyncCandidate(nil), repository.items...)
	for index := range items {
		items[index].HasPendingUsageSync = len(repository.windows[items[index].SiteID]) > 0
	}
	return items, repository.err
}

func (repository *syncCandidateRepositoryFake) PlanUsageSyncWindow(
	_ context.Context,
	siteID int64,
	through time.Time,
	lookback, overlap time.Duration,
) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for index := range repository.items {
		candidate := &repository.items[index]
		if candidate.SiteID != siteID {
			continue
		}
		if candidate.UsageSyncThroughAt != nil && !candidate.UsageSyncThroughAt.Before(through) {
			return nil
		}
		from := through.Add(-lookback)
		if candidate.UsageSyncThroughAt != nil {
			from = candidate.UsageSyncThroughAt.Add(-overlap)
		}
		repository.nextID++
		repository.windows[siteID] = append(repository.windows[siteID], UsageSyncWindow{
			ID: repository.nextID, From: from, To: through,
		})
		copy := through
		candidate.UsageSyncThroughAt = &copy
		return nil
	}
	return nil
}

func (repository *syncCandidateRepositoryFake) NextUsageSyncWindow(
	_ context.Context,
	siteID int64,
) (UsageSyncWindow, bool, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.windows[siteID]) == 0 {
		return UsageSyncWindow{}, false, nil
	}
	return repository.windows[siteID][0], true, nil
}

func (repository *syncCandidateRepositoryFake) commit(window UsageSyncWindow, next string, hasMore bool) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	windows := repository.windows[windowSiteID(repository.windows, window.ID)]
	if len(windows) == 0 || windows[0].ID != window.ID || windows[0].Cursor != window.Cursor {
		return
	}
	siteID := windowSiteID(repository.windows, window.ID)
	if hasMore {
		repository.windows[siteID][0].Cursor = next
		return
	}
	repository.windows[siteID] = repository.windows[siteID][1:]
}

func windowSiteID(windows map[int64][]UsageSyncWindow, windowID int64) int64 {
	for siteID, items := range windows {
		for _, item := range items {
			if item.ID == windowID {
				return siteID
			}
		}
	}
	return 0
}

type usageCall struct {
	siteID int64
	window UsageSyncWindow
	limit  int
}

type accountSynchronizerFake struct {
	mu           sync.Mutex
	capabilities Capabilities
	repository   *syncCandidateRepositoryFake
	balanceSites []int64
	usage        []usageCall
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

func (service *accountSynchronizerFake) SyncUsageWindowPage(
	_ context.Context,
	siteID int64,
	window UsageSyncWindow,
	limit int,
) (UsageResult, error) {
	service.mu.Lock()
	service.usage = append(service.usage, usageCall{siteID: siteID, window: window, limit: limit})
	service.mu.Unlock()
	page, _ := strconv.Atoi(window.Cursor)
	if page == 0 {
		page = 1
	}
	hasMore := page < 3
	next := ""
	if hasMore {
		next = strconv.Itoa(page + 1)
	}
	service.repository.commit(window, next, hasMore)
	return UsageResult{Page: UsagePage{HasMore: hasMore, NextCursor: next, FetchedAt: time.Now().UTC()}}, nil
}
