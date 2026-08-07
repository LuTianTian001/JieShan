package siteadmin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrConnectionUnavailable = errors.New("site administration connection is unavailable")
	ErrAdapterUnavailable    = errors.New("site administration adapter is unavailable")
	ErrSyncFailed            = errors.New("site administration synchronization failed")
)

const (
	OperationSessionRefresh = "session_refresh"
	OperationBalanceRefresh = "balance_refresh"
	OperationUsageSync      = "usage_sync"
)

// StoredConnection is the decrypted, short-lived view needed for one
// administration operation. Repository implementations own encryption and
// compare-and-swap persistence of refreshed session material.
type StoredConnection struct {
	SiteID      int64
	AdapterKind string
	Revision    int64
	Connection  Connection
}

type UsageSaveResult struct {
	Inserted     int
	Deduplicated int
}

type Repository interface {
	LoadConnection(context.Context, int64) (StoredConnection, error)
	PersistSessionUpdate(context.Context, StoredConnection, SessionUpdate) error
	SaveBalanceSnapshot(context.Context, int64, string, BalanceSnapshot) error
	SaveUsagePage(context.Context, int64, string, UsagePage) (UsageSaveResult, error)
	RecordFailure(context.Context, int64, string, string, time.Time) error
}

type AdapterLookup interface {
	Lookup(string) (Adapter, error)
}

type BalanceResult struct {
	Snapshot       BalanceSnapshot
	SessionChanged bool
}

type UsageResult struct {
	Page           UsagePage
	Saved          UsageSaveResult
	SessionChanged bool
}

// Service keeps site-account administration outside the inference path.
// Operations for one site are serialized so rotating refresh tokens cannot be
// overwritten by another balance or usage request using an older revision.
type Service struct {
	repository Repository
	registry   AdapterLookup
	locksMu    sync.Mutex
	locks      map[int64]*sync.Mutex
}

func NewService(repository Repository, registry AdapterLookup) (*Service, error) {
	if repository == nil || registry == nil {
		return nil, errors.New("site administration repository and adapter registry are required")
	}
	return &Service{repository: repository, registry: registry, locks: make(map[int64]*sync.Mutex)}, nil
}

func (service *Service) Capabilities(ctx context.Context, siteID int64) (Capabilities, error) {
	connection, adapter, err := service.load(ctx, siteID)
	if err != nil {
		return Capabilities{}, err
	}
	clearStoredConnection(&connection)
	return adapter.Capabilities(), nil
}

func (service *Service) RefreshSession(ctx context.Context, siteID int64) (SessionUpdate, error) {
	unlock, err := service.lock(siteID)
	if err != nil {
		return SessionUpdate{}, err
	}
	defer unlock()
	connection, adapter, err := service.load(ctx, siteID)
	if err != nil {
		return SessionUpdate{}, err
	}
	defer clearStoredConnection(&connection)
	refresher, ok := adapter.(SessionRefresher)
	if !ok || !adapter.Capabilities().SessionRefresh {
		return SessionUpdate{}, ErrUnsupportedCapability
	}
	update, err := refresher.RefreshSession(ctx, connection.Connection)
	if err != nil {
		service.recordFailure(ctx, siteID, OperationSessionRefresh, "upstream_failed")
		return SessionUpdate{}, fmt.Errorf("%w: refresh session", ErrSyncFailed)
	}
	if err := validateSessionUpdate(update); err != nil {
		service.recordFailure(ctx, siteID, OperationSessionRefresh, "invalid_session")
		return SessionUpdate{}, fmt.Errorf("%w: invalid session update", ErrSyncFailed)
	}
	if update.Changed {
		if err := service.repository.PersistSessionUpdate(ctx, connection, update); err != nil {
			service.recordFailure(ctx, siteID, OperationSessionRefresh, "persistence_failed")
			return SessionUpdate{}, fmt.Errorf("%w: persist refreshed session", ErrSyncFailed)
		}
	}
	return update, nil
}

func (service *Service) RefreshBalance(ctx context.Context, siteID int64) (BalanceResult, error) {
	unlock, err := service.lock(siteID)
	if err != nil {
		return BalanceResult{}, err
	}
	defer unlock()
	connection, adapter, err := service.load(ctx, siteID)
	if err != nil {
		return BalanceResult{}, err
	}
	defer clearStoredConnection(&connection)
	reader, ok := adapter.(BalanceReader)
	if !ok || !adapter.Capabilities().Balance {
		return BalanceResult{}, ErrUnsupportedCapability
	}
	snapshot, update, err := reader.ReadBalance(ctx, connection.Connection)
	if err != nil {
		service.recordFailure(ctx, siteID, OperationBalanceRefresh, "upstream_failed")
		return BalanceResult{}, fmt.Errorf("%w: read balance", ErrSyncFailed)
	}
	if err := snapshot.Validate(); err != nil {
		service.recordFailure(ctx, siteID, OperationBalanceRefresh, "invalid_snapshot")
		return BalanceResult{}, fmt.Errorf("%w: invalid balance snapshot", ErrSyncFailed)
	}
	changed, err := service.persistSessionUpdate(ctx, connection, update)
	if err != nil {
		service.recordFailure(ctx, siteID, OperationBalanceRefresh, "session_persistence_failed")
		return BalanceResult{}, err
	}
	if err := service.repository.SaveBalanceSnapshot(ctx, siteID, adapter.Kind(), snapshot); err != nil {
		service.recordFailure(ctx, siteID, OperationBalanceRefresh, "persistence_failed")
		return BalanceResult{}, fmt.Errorf("%w: persist balance snapshot", ErrSyncFailed)
	}
	return BalanceResult{Snapshot: snapshot, SessionChanged: changed}, nil
}

func (service *Service) SyncUsagePage(ctx context.Context, siteID int64, query UsageQuery) (UsageResult, error) {
	if err := query.Validate(); err != nil {
		return UsageResult{}, err
	}
	unlock, err := service.lock(siteID)
	if err != nil {
		return UsageResult{}, err
	}
	defer unlock()
	connection, adapter, err := service.load(ctx, siteID)
	if err != nil {
		return UsageResult{}, err
	}
	defer clearStoredConnection(&connection)
	reader, ok := adapter.(UsageReader)
	if !ok || !adapter.Capabilities().Usage {
		return UsageResult{}, ErrUnsupportedCapability
	}
	page, update, err := reader.ReadUsage(ctx, connection.Connection, query)
	if err != nil {
		service.recordFailure(ctx, siteID, OperationUsageSync, "upstream_failed")
		return UsageResult{}, fmt.Errorf("%w: read upstream usage", ErrSyncFailed)
	}
	if err := validateUsagePage(page); err != nil {
		service.recordFailure(ctx, siteID, OperationUsageSync, "invalid_page")
		return UsageResult{}, fmt.Errorf("%w: invalid upstream usage page", ErrSyncFailed)
	}
	changed, err := service.persistSessionUpdate(ctx, connection, update)
	if err != nil {
		service.recordFailure(ctx, siteID, OperationUsageSync, "session_persistence_failed")
		return UsageResult{}, err
	}
	saved, err := service.repository.SaveUsagePage(ctx, siteID, adapter.Kind(), page)
	if err != nil {
		service.recordFailure(ctx, siteID, OperationUsageSync, "persistence_failed")
		return UsageResult{}, fmt.Errorf("%w: persist upstream usage", ErrSyncFailed)
	}
	if saved.Inserted < 0 || saved.Deduplicated < 0 || saved.Inserted+saved.Deduplicated > len(page.Records) {
		service.recordFailure(ctx, siteID, OperationUsageSync, "invalid_persistence_result")
		return UsageResult{}, fmt.Errorf("%w: invalid upstream usage persistence result", ErrSyncFailed)
	}
	return UsageResult{Page: page, Saved: saved, SessionChanged: changed}, nil
}

func (service *Service) persistSessionUpdate(
	ctx context.Context,
	connection StoredConnection,
	update *SessionUpdate,
) (bool, error) {
	if update == nil || !update.Changed {
		return false, nil
	}
	if err := validateSessionUpdate(*update); err != nil {
		return false, fmt.Errorf("%w: invalid session update", ErrSyncFailed)
	}
	if err := service.repository.PersistSessionUpdate(ctx, connection, *update); err != nil {
		return false, fmt.Errorf("%w: persist refreshed session", ErrSyncFailed)
	}
	return true, nil
}

func (service *Service) load(ctx context.Context, siteID int64) (StoredConnection, Adapter, error) {
	if siteID <= 0 {
		return StoredConnection{}, nil, ErrConnectionUnavailable
	}
	connection, err := service.repository.LoadConnection(ctx, siteID)
	if err != nil {
		return StoredConnection{}, nil, fmt.Errorf("%w: load connection", ErrConnectionUnavailable)
	}
	if connection.SiteID != siteID || connection.Revision <= 0 || strings.TrimSpace(connection.AdapterKind) == "" ||
		strings.TrimSpace(connection.Connection.Origin) == "" {
		clearStoredConnection(&connection)
		return StoredConnection{}, nil, ErrConnectionUnavailable
	}
	adapter, err := service.registry.Lookup(connection.AdapterKind)
	if err != nil || adapter == nil {
		clearStoredConnection(&connection)
		return StoredConnection{}, nil, ErrAdapterUnavailable
	}
	if err := ValidateAdapter(adapter); err != nil {
		clearStoredConnection(&connection)
		return StoredConnection{}, nil, ErrAdapterUnavailable
	}
	return connection, adapter, nil
}

func (service *Service) lock(siteID int64) (func(), error) {
	if siteID <= 0 {
		return nil, ErrConnectionUnavailable
	}
	service.locksMu.Lock()
	lock := service.locks[siteID]
	if lock == nil {
		lock = &sync.Mutex{}
		service.locks[siteID] = lock
	}
	service.locksMu.Unlock()
	lock.Lock()
	return lock.Unlock, nil
}

func (service *Service) recordFailure(ctx context.Context, siteID int64, operation, code string) {
	_ = service.repository.RecordFailure(ctx, siteID, operation, code, time.Now().UTC())
}

func validateSessionUpdate(update SessionUpdate) error {
	if !update.Changed {
		return nil
	}
	if update.RefreshedAt.IsZero() {
		return errors.New("session refresh time is required")
	}
	secrets := update.Secrets
	if strings.TrimSpace(secrets.Authorization) == "" && strings.TrimSpace(secrets.AccessToken) == "" &&
		strings.TrimSpace(secrets.RefreshToken) == "" && strings.TrimSpace(secrets.Cookie) == "" {
		return errors.New("session update contains no authentication material")
	}
	return nil
}

func validateUsagePage(page UsagePage) error {
	if page.FetchedAt.IsZero() {
		return errors.New("usage fetch time is required")
	}
	if page.HasMore && strings.TrimSpace(page.NextCursor) == "" {
		return errors.New("usage page with more records requires a cursor")
	}
	for index, record := range page.Records {
		if err := record.Validate(); err != nil {
			return fmt.Errorf("usage record %d: %w", index, err)
		}
	}
	return nil
}

func clearStoredConnection(connection *StoredConnection) {
	if connection == nil {
		return
	}
	connection.Connection.Secrets = Secrets{}
}

var _ AdapterLookup = (*Registry)(nil)
