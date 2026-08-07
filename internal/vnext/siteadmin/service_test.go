package siteadmin

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestServicePersistsRotatedSessionBeforeBalance(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	repository := &serviceRepositoryFake{connection: testStoredConnection()}
	adapter := &serviceAdapterFake{
		capabilities: Capabilities{Balance: true},
		balance:      BalanceSnapshot{Available: Amount{Value: "12.50", Unit: "USD"}, CapturedAt: now},
		update:       &SessionUpdate{Changed: true, RefreshedAt: now, Secrets: Secrets{AccessToken: "rotated"}},
	}
	service := mustService(t, repository, adapter)

	result, err := service.RefreshBalance(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !result.SessionChanged || result.Snapshot.Available.Value != "12.50" {
		t.Fatalf("result = %+v", result)
	}
	if len(repository.operations) != 2 || repository.operations[0] != "session" || repository.operations[1] != "balance" {
		t.Fatalf("operations = %#v", repository.operations)
	}
}

func TestServicePersistsAndReportsDeduplicatedUsage(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	repository := &serviceRepositoryFake{connection: testStoredConnection(), usageSave: UsageSaveResult{Inserted: 1, Deduplicated: 1}}
	adapter := &serviceAdapterFake{
		capabilities: Capabilities{Usage: true},
		usage: UsagePage{FetchedAt: now, Records: []UsageRecord{
			{RemoteID: "one", OccurredAt: now, Model: "model-a"},
			{RemoteID: "one", OccurredAt: now, Model: "model-a"},
		}},
	}
	service := mustService(t, repository, adapter)

	result, err := service.SyncUsagePage(t.Context(), 7, UsageQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if result.Saved.Inserted != 1 || result.Saved.Deduplicated != 1 || len(result.Page.Records) != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestServiceRejectsAdvertisedButUnavailableCapability(t *testing.T) {
	repository := &serviceRepositoryFake{connection: testStoredConnection()}
	adapter := &sessionOnlyAdapter{}
	service := mustService(t, repository, adapter)
	if _, err := service.RefreshBalance(t.Context(), 7); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("RefreshBalance() error = %v", err)
	}
}

func TestServiceSerializesOperationsForOneSite(t *testing.T) {
	repository := &serviceRepositoryFake{connection: testStoredConnection()}
	adapter := &serviceAdapterFake{
		capabilities: Capabilities{Balance: true},
		balance:      BalanceSnapshot{Available: Amount{Value: "1", Unit: "USD"}, CapturedAt: time.Now().UTC()},
		block:        make(chan struct{}), entered: make(chan struct{}, 2),
	}
	service := mustService(t, repository, adapter)

	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = service.RefreshBalance(context.Background(), 7)
		}()
	}
	<-adapter.entered
	select {
	case <-adapter.entered:
		t.Fatal("same-site operation entered concurrently")
	case <-time.After(40 * time.Millisecond):
	}
	close(adapter.block)
	group.Wait()
	if adapter.maximumActive.Load() != 1 {
		t.Fatalf("maximum active operations = %d", adapter.maximumActive.Load())
	}
}

type serviceRepositoryFake struct {
	mu         sync.Mutex
	connection StoredConnection
	usageSave  UsageSaveResult
	operations []string
}

func (repository *serviceRepositoryFake) LoadConnection(context.Context, int64) (StoredConnection, error) {
	return repository.connection, nil
}

func (repository *serviceRepositoryFake) PersistSessionUpdate(context.Context, StoredConnection, SessionUpdate) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.operations = append(repository.operations, "session")
	return nil
}

func (repository *serviceRepositoryFake) SaveBalanceSnapshot(context.Context, int64, string, BalanceSnapshot) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.operations = append(repository.operations, "balance")
	return nil
}

func (repository *serviceRepositoryFake) SaveUsagePage(context.Context, int64, string, UsagePage) (UsageSaveResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	repository.operations = append(repository.operations, "usage")
	return repository.usageSave, nil
}

func (repository *serviceRepositoryFake) RecordFailure(context.Context, int64, string, string, time.Time) error {
	return nil
}

type serviceAdapterFake struct {
	capabilities  Capabilities
	balance       BalanceSnapshot
	usage         UsagePage
	update        *SessionUpdate
	block         chan struct{}
	entered       chan struct{}
	active        atomic.Int32
	maximumActive atomic.Int32
}

func (*serviceAdapterFake) Kind() string { return "test" }

func (adapter *serviceAdapterFake) Capabilities() Capabilities { return adapter.capabilities }

func (adapter *serviceAdapterFake) ReadBalance(context.Context, Connection) (BalanceSnapshot, *SessionUpdate, error) {
	active := adapter.active.Add(1)
	defer adapter.active.Add(-1)
	for {
		maximum := adapter.maximumActive.Load()
		if active <= maximum || adapter.maximumActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	if adapter.entered != nil {
		adapter.entered <- struct{}{}
	}
	if adapter.block != nil {
		<-adapter.block
	}
	return adapter.balance, adapter.update, nil
}

func (adapter *serviceAdapterFake) ReadUsage(context.Context, Connection, UsageQuery) (UsagePage, *SessionUpdate, error) {
	return adapter.usage, adapter.update, nil
}

type sessionOnlyAdapter struct{}

func (*sessionOnlyAdapter) Kind() string               { return "test" }
func (*sessionOnlyAdapter) Capabilities() Capabilities { return Capabilities{SessionRefresh: true} }
func (*sessionOnlyAdapter) RefreshSession(context.Context, Connection) (SessionUpdate, error) {
	return SessionUpdate{Changed: false}, nil
}

func mustService(t *testing.T, repository Repository, adapter Adapter) *Service {
	t.Helper()
	registry := NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repository, registry)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testStoredConnection() StoredConnection {
	return StoredConnection{
		SiteID: 7, AdapterKind: "test", Revision: 1,
		Connection: Connection{Origin: "https://relay.example", Secrets: Secrets{AccessToken: "token"}},
	}
}

var _ Repository = (*serviceRepositoryFake)(nil)
var _ BalanceReader = (*serviceAdapterFake)(nil)
var _ UsageReader = (*serviceAdapterFake)(nil)
var _ SessionRefresher = (*sessionOnlyAdapter)(nil)
