package siteadminapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	"github.com/LuTianTian001/JieShan/internal/vnext/siteadmin"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const siteAccountCipherVersion = int64(1)

type ConnectionCreate struct {
	AdapterKind string
	Origin      string
	Enabled     bool
	Secrets     siteadmin.Secrets
}

type ConnectionUpdate struct {
	ExpectedRevision int64
	AdapterKind      string
	Origin           string
	Enabled          bool
}

type ConnectionSecretUpdate struct {
	ExpectedRevision int64
	Secrets          siteadmin.Secrets
}

type Repository interface {
	siteadmin.Repository
	ListConnections(context.Context) ([]vnextstore.SiteAccountConnection, error)
	GetConnection(context.Context, int64) (vnextstore.SiteAccountConnection, error)
	CreateConnection(context.Context, int64, ConnectionCreate) (vnextstore.SiteAccountConnection, error)
	UpdateConnection(context.Context, int64, ConnectionUpdate) (vnextstore.SiteAccountConnection, error)
	ReplaceConnectionSecrets(context.Context, int64, ConnectionSecretUpdate) (vnextstore.SiteAccountConnection, error)
	DeleteConnection(context.Context, int64, int64) error
	LatestBalance(context.Context, int64) (vnextstore.SiteBalanceSnapshot, error)
	ListUsage(context.Context, int64, vnextstore.SiteUsageListFilter) (vnextstore.SiteUsagePage, error)
}

type StoreRepository struct {
	store *vnextstore.Store
	box   *secretbox.Box
}

func NewStoreRepository(store *vnextstore.Store, box *secretbox.Box) (*StoreRepository, error) {
	if store == nil || box == nil {
		return nil, errors.New("VNext store and secret box are required")
	}
	return &StoreRepository{store: store, box: box}, nil
}

func (repository *StoreRepository) ListConnections(ctx context.Context) ([]vnextstore.SiteAccountConnection, error) {
	return repository.store.ListSiteAccountConnections(ctx)
}

func (repository *StoreRepository) ListSyncCandidates(ctx context.Context) ([]siteadmin.SyncCandidate, error) {
	connections, err := repository.store.ListSiteAccountConnections(ctx)
	if err != nil {
		return nil, err
	}
	states, err := repository.store.ListSiteUsageSyncStates(ctx)
	if err != nil {
		return nil, err
	}
	stateBySite := make(map[int64]vnextstore.SiteUsageSyncState, len(states))
	for _, state := range states {
		stateBySite[state.SiteID] = state
	}
	result := make([]siteadmin.SyncCandidate, 0, len(connections))
	for _, connection := range connections {
		state := stateBySite[connection.SiteID]
		result = append(result, siteadmin.SyncCandidate{
			SiteID: connection.SiteID, Enabled: connection.Enabled, SecretConfigured: connection.SecretConfigured,
			LastBalanceRefreshAt: millisTime(connection.LastBalanceRefreshAt),
			UsageSyncThroughAt:   millisTime(state.ThroughAt),
			HasPendingUsageSync:  state.HasPending,
		})
	}
	return result, nil
}

func (repository *StoreRepository) PlanUsageSyncWindow(
	ctx context.Context,
	siteID int64,
	through time.Time,
	initialLookback, overlap time.Duration,
) error {
	_, err := repository.store.PlanSiteUsageSyncWindow(ctx, siteID, through.UTC().UnixMilli(),
		initialLookback.Milliseconds(), overlap.Milliseconds())
	return err
}

func (repository *StoreRepository) NextUsageSyncWindow(
	ctx context.Context,
	siteID int64,
) (siteadmin.UsageSyncWindow, bool, error) {
	window, ok, err := repository.store.NextSiteUsageSyncWindow(ctx, siteID)
	if err != nil || !ok {
		return siteadmin.UsageSyncWindow{}, ok, err
	}
	return siteadmin.UsageSyncWindow{
		ID: window.ID, From: time.UnixMilli(window.FromAt).UTC(), To: time.UnixMilli(window.ToAt).UTC(), Cursor: window.Cursor,
	}, true, nil
}

func (repository *StoreRepository) GetConnection(ctx context.Context, siteID int64) (vnextstore.SiteAccountConnection, error) {
	return repository.store.GetSiteAccountConnection(ctx, siteID)
}

func millisTime(value *int64) *time.Time {
	if value == nil || *value <= 0 {
		return nil
	}
	parsed := time.UnixMilli(*value).UTC()
	return &parsed
}

func (repository *StoreRepository) CreateConnection(
	ctx context.Context,
	siteID int64,
	input ConnectionCreate,
) (vnextstore.SiteAccountConnection, error) {
	encoded, err := encodeSecrets(input.Secrets)
	if err != nil {
		return vnextstore.SiteAccountConnection{}, err
	}
	defer clear(encoded)
	return repository.store.CreateSealedSiteAccountConnection(ctx, siteID, vnextstore.SealedSiteAccountConnectionInput{
		AdapterKind: input.AdapterKind, Origin: input.Origin, CipherVersion: siteAccountCipherVersion, Enabled: input.Enabled,
	}, func(connectionID, ownerSiteID int64) ([]byte, error) {
		return repository.box.Seal(secretbox.PurposeSiteAdministration, secretbox.Identity{
			RecordID: connectionID,
			OwnerID:  ownerSiteID,
		}, encoded)
	})
}

func (repository *StoreRepository) UpdateConnection(
	ctx context.Context,
	siteID int64,
	input ConnectionUpdate,
) (vnextstore.SiteAccountConnection, error) {
	return repository.store.UpdateSiteAccountConnection(ctx, siteID, vnextstore.SiteAccountConnectionUpdate{
		ExpectedRevision: input.ExpectedRevision,
		AdapterKind:      input.AdapterKind,
		Origin:           input.Origin,
		Enabled:          input.Enabled,
	})
}

func (repository *StoreRepository) ReplaceConnectionSecrets(
	ctx context.Context,
	siteID int64,
	input ConnectionSecretUpdate,
) (vnextstore.SiteAccountConnection, error) {
	connection, err := repository.store.GetSiteAccountConnection(ctx, siteID)
	if err != nil {
		return vnextstore.SiteAccountConnection{}, err
	}
	encoded, err := encodeSecrets(input.Secrets)
	if err != nil {
		return vnextstore.SiteAccountConnection{}, err
	}
	defer clear(encoded)
	ciphertext, err := repository.box.Seal(secretbox.PurposeSiteAdministration, secretbox.Identity{
		RecordID: connection.ID,
		OwnerID:  connection.SiteID,
	}, encoded)
	if err != nil {
		return vnextstore.SiteAccountConnection{}, err
	}
	defer clear(ciphertext)
	return repository.store.ReplaceSealedSiteAccountSecret(ctx, siteID, input.ExpectedRevision, siteAccountCipherVersion, ciphertext)
}

func (repository *StoreRepository) DeleteConnection(ctx context.Context, siteID, expectedRevision int64) error {
	return repository.store.DeleteSiteAccountConnection(ctx, siteID, expectedRevision)
}

func (repository *StoreRepository) LoadConnection(ctx context.Context, siteID int64) (siteadmin.StoredConnection, error) {
	secret, err := repository.store.LoadSiteAccountSecret(ctx, siteID)
	if err != nil {
		return siteadmin.StoredConnection{}, err
	}
	if secret.Connection.CipherVersion != siteAccountCipherVersion {
		clear(secret.Ciphertext)
		return siteadmin.StoredConnection{}, errors.New("site account cipher version is unsupported")
	}
	plaintext, err := repository.box.Open(secretbox.PurposeSiteAdministration, secretbox.Identity{
		RecordID: secret.Connection.ID,
		OwnerID:  secret.Connection.SiteID,
	}, secret.Ciphertext)
	clear(secret.Ciphertext)
	if err != nil {
		return siteadmin.StoredConnection{}, err
	}
	defer clear(plaintext)
	secrets, err := decodeSecrets(plaintext)
	if err != nil {
		return siteadmin.StoredConnection{}, err
	}
	return siteadmin.StoredConnection{
		SiteID:      secret.Connection.SiteID,
		AdapterKind: secret.Connection.AdapterKind,
		Revision:    secret.Connection.Revision,
		Connection: siteadmin.Connection{
			Origin:  secret.Connection.Origin,
			Secrets: secrets,
		},
	}, nil
}

func (repository *StoreRepository) PersistSessionUpdate(
	ctx context.Context,
	connection siteadmin.StoredConnection,
	update siteadmin.SessionUpdate,
) error {
	stored, err := repository.store.GetSiteAccountConnection(ctx, connection.SiteID)
	if err != nil {
		return err
	}
	if stored.ID <= 0 || stored.Revision != connection.Revision {
		return vnextstore.ErrRevisionConflict
	}
	encoded, err := encodeSecrets(update.Secrets)
	if err != nil {
		return err
	}
	defer clear(encoded)
	ciphertext, err := repository.box.Seal(secretbox.PurposeSiteAdministration, secretbox.Identity{
		RecordID: stored.ID,
		OwnerID:  stored.SiteID,
	}, encoded)
	if err != nil {
		return err
	}
	defer clear(ciphertext)
	return repository.store.PersistSiteAccountSession(ctx, stored.SiteID, connection.Revision,
		siteAccountCipherVersion, ciphertext, update.RefreshedAt.UTC().UnixMilli())
}

func (repository *StoreRepository) SaveBalanceSnapshot(
	ctx context.Context,
	siteID int64,
	adapterKind string,
	snapshot siteadmin.BalanceSnapshot,
) error {
	input := vnextstore.SiteBalanceSnapshotWrite{
		AccountRemoteID: snapshot.AccountID,
		AccountName:     snapshot.AccountName,
		AvailableValue:  snapshot.Available.Value,
		AvailableUnit:   snapshot.Available.Unit,
		CapturedAt:      snapshot.CapturedAt.UTC().UnixMilli(),
	}
	if snapshot.Used != nil {
		value, unit := snapshot.Used.Value, snapshot.Used.Unit
		input.UsedValue, input.UsedUnit = &value, &unit
	}
	_, err := repository.store.SaveSiteBalanceSnapshot(ctx, siteID, adapterKind, input)
	return err
}

func (repository *StoreRepository) SaveUsagePage(
	ctx context.Context,
	siteID int64,
	adapterKind string,
	page siteadmin.UsagePage,
	progress *siteadmin.UsagePageProgress,
) (siteadmin.UsageSaveResult, error) {
	records := make([]vnextstore.SiteUsageRecordWrite, 0, len(page.Records))
	for _, record := range page.Records {
		item := vnextstore.SiteUsageRecordWrite{
			DedupKey:          record.DedupKey(),
			RemoteID:          record.RemoteID,
			RequestID:         record.RequestID,
			UpstreamRequestID: record.UpstreamRequestID,
			OccurredAt:        record.OccurredAt.UTC().UnixMilli(),
			Model:             record.Model,
			UpstreamModel:     record.UpstreamModel,
			Status:            record.Status,
			HTTPStatus:        cloneInt(record.HTTPStatus),
			InputTokens:       cloneInt64(record.Tokens.Input),
			OutputTokens:      cloneInt64(record.Tokens.Output),
			CacheReadTokens:   cloneInt64(record.Tokens.CacheRead),
			CacheWriteTokens:  cloneInt64(record.Tokens.CacheWrite),
			ReasoningTokens:   cloneInt64(record.Tokens.Reasoning),
			TotalTokens:       cloneInt64(record.Tokens.Total),
			DurationMS:        cloneInt64(record.DurationMS),
			APIKeyName:        record.APIKeyName,
			SourceFetchedAt:   page.FetchedAt.UTC().UnixMilli(),
		}
		if record.Charge != nil {
			value, unit := record.Charge.Value, record.Charge.Unit
			item.ChargeValue, item.ChargeUnit = &value, &unit
		}
		records = append(records, item)
	}
	var saved vnextstore.SiteUsageSaveResult
	var err error
	if progress == nil {
		saved, err = repository.store.SaveSiteUsageRecords(ctx, siteID, adapterKind, records, page.FetchedAt.UTC().UnixMilli())
	} else {
		saved, err = repository.store.SaveSiteUsageWindowPage(ctx, siteID, adapterKind, records,
			page.FetchedAt.UTC().UnixMilli(), vnextstore.SiteUsageSyncProgress{
				WindowID: progress.WindowID, ExpectedCursor: progress.ExpectedCursor,
				NextCursor: page.NextCursor, HasMore: page.HasMore,
			})
	}
	return siteadmin.UsageSaveResult{Inserted: saved.Inserted, Deduplicated: saved.Deduplicated}, err
}

func (repository *StoreRepository) RecordFailure(
	ctx context.Context,
	siteID int64,
	operation, code string,
	occurredAt time.Time,
) error {
	return repository.store.RecordSiteAccountFailure(ctx, siteID, operation, code, occurredAt.UTC().UnixMilli())
}

func (repository *StoreRepository) LatestBalance(ctx context.Context, siteID int64) (vnextstore.SiteBalanceSnapshot, error) {
	return repository.store.GetLatestSiteBalance(ctx, siteID)
}

func (repository *StoreRepository) ListUsage(
	ctx context.Context,
	siteID int64,
	filter vnextstore.SiteUsageListFilter,
) (vnextstore.SiteUsagePage, error) {
	return repository.store.ListSiteUsageRecords(ctx, siteID, filter)
}

type secretEnvelope struct {
	Authorization string `json:"authorization,omitempty"`
	AccessToken   string `json:"accessToken,omitempty"`
	RefreshToken  string `json:"refreshToken,omitempty"`
	Cookie        string `json:"cookie,omitempty"`
	ExpiresAt     *int64 `json:"expiresAt,omitempty"`
}

func encodeSecrets(secrets siteadmin.Secrets) ([]byte, error) {
	if err := validateSecrets(secrets); err != nil {
		return nil, err
	}
	envelope := secretEnvelope{
		Authorization: strings.TrimSpace(secrets.Authorization),
		AccessToken:   strings.TrimSpace(secrets.AccessToken),
		RefreshToken:  strings.TrimSpace(secrets.RefreshToken),
		Cookie:        strings.TrimSpace(secrets.Cookie),
	}
	if !secrets.ExpiresAt.IsZero() {
		value := secrets.ExpiresAt.UTC().UnixMilli()
		envelope.ExpiresAt = &value
	}
	return json.Marshal(envelope)
}

func decodeSecrets(data []byte) (siteadmin.Secrets, error) {
	var envelope secretEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return siteadmin.Secrets{}, errors.New("site account secret payload is malformed")
	}
	secrets := siteadmin.Secrets{
		Authorization: envelope.Authorization,
		AccessToken:   envelope.AccessToken,
		RefreshToken:  envelope.RefreshToken,
		Cookie:        envelope.Cookie,
	}
	if envelope.ExpiresAt != nil {
		secrets.ExpiresAt = time.UnixMilli(*envelope.ExpiresAt).UTC()
	}
	if err := validateSecrets(secrets); err != nil {
		return siteadmin.Secrets{}, errors.New("site account secret payload is incomplete")
	}
	return secrets, nil
}

func validateSecrets(secrets siteadmin.Secrets) error {
	if strings.TrimSpace(secrets.Authorization) == "" && strings.TrimSpace(secrets.AccessToken) == "" &&
		strings.TrimSpace(secrets.RefreshToken) == "" && strings.TrimSpace(secrets.Cookie) == "" {
		return errors.New("at least one site account credential is required")
	}
	return nil
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

var _ Repository = (*StoreRepository)(nil)
