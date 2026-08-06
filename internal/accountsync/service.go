package accountsync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LuTianTian001/JieShan/internal/accountadapter"
	"github.com/LuTianTian001/JieShan/internal/redact"
	"github.com/LuTianTian001/JieShan/internal/secrets"
	"github.com/LuTianTian001/JieShan/internal/store"
)

const (
	DefaultInterval   = 30 * time.Minute
	staleFactor       = 2
	usageInitialRange = 30 * 24 * time.Hour
	usageSyncOverlap  = 5 * time.Minute
	usageSyncPages    = 10
	usageSyncLimit    = 100
)

var (
	ErrAccountDisabled    = errors.New("upstream account synchronization is disabled")
	ErrInvalidCredentials = errors.New("upstream account credentials are incomplete")
	ErrSyncInProgress     = errors.New("upstream account synchronization is already in progress")
	ErrUnsupportedAdapter = errors.New("unsupported upstream account adapter")
)

type Capabilities struct {
	Balance      bool `json:"balance"`
	Subscription bool `json:"subscription"`
	Usage        bool `json:"usage"`
	TokenRefresh bool `json:"tokenRefresh"`
}

type AdapterDescriptor struct {
	Key          string       `json:"key"`
	Label        string       `json:"label"`
	AuthKinds    []string     `json:"authKinds"`
	Capabilities Capabilities `json:"capabilities"`
}

type AdapterSummary struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type AuthInput struct {
	Kind         string `json:"kind"`
	APIToken     string `json:"apiToken,omitempty"`
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

type ConfigureInput struct {
	AdapterKey   string    `json:"adapterKey"`
	DashboardURL string    `json:"dashboardUrl"`
	Enabled      bool      `json:"enabled"`
	Auth         AuthInput `json:"auth"`
	RefreshNow   bool      `json:"refreshNow"`
}

type AuthSummary struct {
	Kind                 string  `json:"kind"`
	HasAPIToken          bool    `json:"hasApiToken"`
	HasAccessToken       bool    `json:"hasAccessToken"`
	HasRefreshToken      bool    `json:"hasRefreshToken"`
	AccessTokenExpiresAt *string `json:"accessTokenExpiresAt"`
}

type SyncErrorView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SyncView struct {
	State         string         `json:"state"`
	LastAttemptAt *string        `json:"lastAttemptAt"`
	LastSuccessAt *string        `json:"lastSuccessAt"`
	NextAt        *string        `json:"nextAt"`
	Stale         bool           `json:"stale"`
	Error         *SyncErrorView `json:"error"`
}

type SourceAmount struct {
	Value       string `json:"value"`
	Currency    string `json:"currency"`
	Display     string `json:"display,omitempty"`
	SourceLabel string `json:"sourceLabel,omitempty"`
}

type SubscriptionView struct {
	PlanName    string        `json:"planName"`
	Status      *string       `json:"status"`
	ExpiresAt   *string       `json:"expiresAt"`
	RenewsAt    *string       `json:"renewsAt"`
	PeriodStart *string       `json:"periodStart"`
	PeriodEnd   *string       `json:"periodEnd"`
	Remaining   *SourceAmount `json:"remaining,omitempty"`
	Total       *SourceAmount `json:"total,omitempty"`
}

type SnapshotView struct {
	CapturedAt   string            `json:"capturedAt"`
	Balance      *SourceAmount     `json:"balance"`
	Subscription *SubscriptionView `json:"subscription"`
}

type AccountView struct {
	Configured   bool            `json:"configured"`
	Enabled      bool            `json:"enabled"`
	DashboardURL string          `json:"dashboardUrl"`
	Adapter      *AdapterSummary `json:"adapter,omitempty"`
	Auth         *AuthSummary    `json:"auth,omitempty"`
	Capabilities Capabilities    `json:"capabilities"`
	Sync         SyncView        `json:"sync"`
	Snapshot     *SnapshotView   `json:"snapshot"`
}

type UsageItemView struct {
	ID                  string        `json:"id"`
	ExternalID          string        `json:"externalId,omitempty"`
	RequestID           string        `json:"requestId,omitempty"`
	UpstreamRequestID   string        `json:"upstreamRequestId,omitempty"`
	OccurredAt          *string       `json:"occurredAt"`
	SyncedAt            string        `json:"syncedAt"`
	Model               *string       `json:"model"`
	UpstreamModel       *string       `json:"upstreamModel"`
	ReasoningEffort     *string       `json:"reasoningEffort"`
	Amount              *SourceAmount `json:"amount"`
	OriginalCost        *string       `json:"originalCost"`
	ActualCost          *string       `json:"actualCost"`
	Quota               *string       `json:"quota"`
	InputTokens         *int64        `json:"inputTokens"`
	CacheReadTokens     *int64        `json:"cacheReadTokens"`
	CacheCreationTokens *int64        `json:"cacheCreationTokens"`
	OutputTokens        *int64        `json:"outputTokens"`
	ReasoningTokens     *int64        `json:"reasoningTokens"`
	TotalTokens         *int64        `json:"totalTokens"`
	HTTPStatus          *int          `json:"httpStatus"`
	Status              *string       `json:"status"`
	DurationMS          *int64        `json:"durationMs"`
	FirstTokenMS        *int64        `json:"firstTokenMs"`
	Stream              *bool         `json:"stream"`
	RateMultiplier      *string       `json:"rateMultiplier"`
	ModelMultiplier     *string       `json:"modelMultiplier"`
	GroupMultiplier     *string       `json:"groupMultiplier"`
	APIKeyID            string        `json:"apiKeyId,omitempty"`
	APIKeyName          string        `json:"apiKeyName,omitempty"`
	GroupID             string        `json:"groupId,omitempty"`
	GroupName           string        `json:"groupName,omitempty"`
	Endpoint            string        `json:"endpoint,omitempty"`
	RequestType         string        `json:"requestType,omitempty"`
	BillingType         string        `json:"billingType,omitempty"`
	BillingMode         string        `json:"billingMode,omitempty"`
	SourceText          string        `json:"sourceText,omitempty"`
}

type UsagePageView struct {
	Items        []UsageItemView `json:"items"`
	Range        string          `json:"range"`
	LastSyncedAt *string         `json:"lastSyncedAt"`
	NextBeforeID *string         `json:"nextBeforeId"`
	HasMore      bool            `json:"hasMore"`
}

type SyncError struct {
	Code string
	Err  error
}

func (e *SyncError) Error() string {
	if e == nil || e.Err == nil {
		return "account synchronization failed"
	}
	return e.Err.Error()
}

func (e *SyncError) Unwrap() error { return e.Err }

type authEnvelope struct {
	Version     int                        `json:"version"`
	Kind        string                     `json:"kind"`
	Credentials accountadapter.Credentials `json:"credentials"`
}

type snapshotDocument struct {
	Version       int                           `json:"version"`
	Account       accountadapter.Snapshot       `json:"account"`
	Subscriptions []accountadapter.Subscription `json:"subscriptions"`
	Warnings      []syncWarning                 `json:"warnings,omitempty"`
}

type syncWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Service struct {
	store    *store.Store
	cipher   *secrets.Cipher
	doer     accountadapter.Doer
	logger   *slog.Logger
	interval time.Duration
	locks    sync.Map
	syncing  sync.Map
}

var descriptors = []AdapterDescriptor{
	{
		Key: "ciii", Label: "Ciii", AuthKinds: []string{"access_refresh"},
		Capabilities: Capabilities{Balance: true, Subscription: true, Usage: true, TokenRefresh: true},
	},
	{
		Key: "new_api", Label: "New API", AuthKinds: []string{"api_token"},
		Capabilities: Capabilities{Balance: true, Subscription: true, Usage: true},
	},
	{
		Key: "one_api", Label: "One API", AuthKinds: []string{"api_token"},
		Capabilities: Capabilities{Balance: true, Usage: true},
	},
}

func New(s *store.Store, cipher *secrets.Cipher, doer accountadapter.Doer, logger *slog.Logger, interval time.Duration) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Service{store: s, cipher: cipher, doer: doer, logger: logger, interval: interval}
}

func (s *Service) Adapters() []AdapterDescriptor {
	items := make([]AdapterDescriptor, len(descriptors))
	copy(items, descriptors)
	for index := range items {
		items[index].AuthKinds = append([]string(nil), items[index].AuthKinds...)
	}
	return items
}

func (s *Service) Configure(ctx context.Context, upstreamID int64, input ConfigureInput) (AccountView, error) {
	mutex := s.upstreamMutex(upstreamID)
	mutex.Lock()
	defer mutex.Unlock()

	if _, err := s.store.GetUpstream(ctx, upstreamID); err != nil {
		return AccountView{}, err
	}
	descriptor, ok := descriptorFor(input.AdapterKey)
	if !ok {
		return AccountView{}, ErrUnsupportedAdapter
	}
	origin, err := normalizeOrigin(input.DashboardURL)
	if err != nil {
		return AccountView{}, err
	}

	existing, existingErr := s.store.GetUpstreamAccountSecret(ctx, upstreamID)
	var envelope authEnvelope
	if existingErr == nil {
		envelope, err = s.decryptEnvelope(existing.AuthCipher)
		if err != nil {
			return AccountView{}, err
		}
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return AccountView{}, existingErr
	}

	nextEnvelope, err := mergeAuth(envelope, descriptor, input.Auth, existingErr == nil && existing.AdapterKind == descriptor.Key)
	if err != nil {
		return AccountView{}, err
	}
	authCipher, err := s.encryptEnvelope(nextEnvelope)
	if err != nil {
		return AccountView{}, err
	}
	capabilities, err := json.Marshal(descriptor.Capabilities)
	if err != nil {
		return AccountView{}, err
	}

	if errors.Is(existingErr, sql.ErrNoRows) {
		_, err = s.store.CreateUpstreamAccount(ctx, store.UpstreamAccountWrite{
			UpstreamID: upstreamID, AdapterKind: descriptor.Key, APIOrigin: origin,
			AuthCipher: authCipher, Enabled: input.Enabled, Capabilities: capabilities,
		})
	} else {
		err = s.store.UpdateUpstreamAccount(ctx, upstreamID, store.UpstreamAccountUpdate{
			AdapterKind: descriptor.Key, APIOrigin: origin, AuthCipher: authCipher,
			ReplaceAuth: true, Enabled: input.Enabled, Capabilities: capabilities,
		})
	}
	if err != nil {
		return AccountView{}, err
	}
	if input.RefreshNow && input.Enabled {
		_, _ = s.refreshLocked(ctx, upstreamID)
	}
	return s.Get(ctx, upstreamID)
}

func (s *Service) Delete(ctx context.Context, upstreamID int64) error {
	mutex := s.upstreamMutex(upstreamID)
	mutex.Lock()
	defer mutex.Unlock()
	return s.store.DeleteUpstreamAccount(ctx, upstreamID)
}

func (s *Service) Get(ctx context.Context, upstreamID int64) (AccountView, error) {
	upstreamItem, err := s.store.GetUpstream(ctx, upstreamID)
	if err != nil {
		return AccountView{}, err
	}
	account, err := s.store.GetUpstreamAccountSecret(ctx, upstreamID)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountView{
			Configured: false, DashboardURL: defaultOrigin(upstreamItem),
			Capabilities: Capabilities{}, Sync: SyncView{State: "unconfigured"},
		}, nil
	}
	if err != nil {
		return AccountView{}, err
	}
	descriptor, ok := descriptorFor(account.AdapterKind)
	if !ok {
		return AccountView{}, ErrUnsupportedAdapter
	}
	envelope, err := s.decryptEnvelope(account.AuthCipher)
	if err != nil {
		return AccountView{}, err
	}
	view := AccountView{
		Configured: true, Enabled: account.Enabled, DashboardURL: account.APIOrigin,
		Adapter: &AdapterSummary{Key: descriptor.Key, Label: descriptor.Label},
		Auth:    authSummary(envelope), Capabilities: capabilitiesFrom(account.Capabilities, descriptor.Capabilities),
		Sync: s.syncView(account.UpstreamAccount),
	}
	snapshot, err := s.store.GetLatestUpstreamAccountSnapshot(ctx, upstreamID)
	if err == nil {
		view.Snapshot, err = snapshotView(snapshot, descriptor.Key)
		if err != nil {
			return AccountView{}, err
		}
		if view.Sync.Error == nil {
			var document snapshotDocument
			if json.Unmarshal(snapshot.Snapshot, &document) == nil && len(document.Warnings) > 0 {
				view.Sync.Error = &SyncErrorView{Code: "partial_sync", Message: warningMessage(document.Warnings)}
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AccountView{}, err
	}
	return view, nil
}

func (s *Service) Refresh(ctx context.Context, upstreamID int64) (AccountView, error) {
	mutex := s.upstreamMutex(upstreamID)
	if !mutex.TryLock() {
		return AccountView{}, ErrSyncInProgress
	}
	defer mutex.Unlock()
	return s.refreshLocked(ctx, upstreamID)
}

func (s *Service) refreshLocked(ctx context.Context, upstreamID int64) (AccountView, error) {
	s.syncing.Store(upstreamID, struct{}{})
	defer s.syncing.Delete(upstreamID)

	account, err := s.store.GetUpstreamAccountSecret(ctx, upstreamID)
	if err != nil {
		return AccountView{}, err
	}
	if !account.Enabled {
		return AccountView{}, ErrAccountDisabled
	}
	descriptor, ok := descriptorFor(account.AdapterKind)
	if !ok {
		return AccountView{}, ErrUnsupportedAdapter
	}
	envelope, err := s.decryptEnvelope(account.AuthCipher)
	if err != nil {
		return AccountView{}, err
	}
	adapter, err := accountadapter.New(accountadapter.Kind(descriptor.Key), s.doer)
	if err != nil {
		return AccountView{}, err
	}

	attemptedAt := store.NowMS()
	connection := accountadapter.Connection{Origin: account.APIOrigin, Credentials: envelope.Credentials}
	snapshot, rotated, err := adapter.Snapshot(ctx, connection)
	applyRotation(&connection, rotated)
	if err != nil {
		return AccountView{}, s.recordFailureWithCredentials(ctx, upstreamID, attemptedAt, descriptor.Capabilities, envelope, connection.Credentials, err)
	}

	capabilities := descriptor.Capabilities
	warnings := make([]syncWarning, 0, 2)
	var subscriptions []accountadapter.Subscription
	if capabilities.Subscription {
		subscriptions, rotated, err = adapter.Subscriptions(ctx, connection)
		applyRotation(&connection, rotated)
		if err != nil {
			if errors.Is(err, accountadapter.ErrUnsupported) {
				capabilities.Subscription = false
			} else {
				warnings = append(warnings, warningFor(err))
			}
		}
	}

	usage := make([]store.UpstreamAccountUsageWrite, 0)
	if capabilities.Usage {
		usageStart := time.Now().Add(-usageInitialRange)
		if account.LastSuccessAt != nil {
			incrementalStart := time.UnixMilli(*account.LastSuccessAt).Add(-usageSyncOverlap)
			if incrementalStart.After(usageStart) {
				usageStart = incrementalStart
			}
		}
		usage, rotated, err = s.fetchUsage(ctx, adapter, connection, descriptor.Key, attemptedAt, usageStart)
		applyRotation(&connection, rotated)
		if err != nil {
			if errors.Is(err, accountadapter.ErrUnsupported) {
				capabilities.Usage = false
			} else {
				warnings = append(warnings, warningFor(err))
			}
			usage = nil
		}
	}

	document, err := json.Marshal(snapshotDocument{
		Version: 1, Account: snapshot, Subscriptions: subscriptions, Warnings: warnings,
	})
	if err != nil {
		return AccountView{}, s.recordFailureWithCredentials(ctx, upstreamID, attemptedAt, capabilities, envelope, connection.Credentials, err)
	}
	capabilityJSON, err := json.Marshal(capabilities)
	if err != nil {
		return AccountView{}, s.recordFailureWithCredentials(ctx, upstreamID, attemptedAt, capabilities, envelope, connection.Credentials, err)
	}
	rotatedCipher, err := s.rotatedAuthCipher(envelope, connection.Credentials)
	if err != nil {
		return AccountView{}, s.recordFailure(ctx, upstreamID, attemptedAt, capabilities, err, nil)
	}
	succeededAt := store.NowMS()
	if err := s.store.UpdateSyncSuccess(ctx, upstreamID, store.UpstreamAccountSyncSuccess{
		AttemptedAt: attemptedAt, SucceededAt: succeededAt, SnapshotAt: succeededAt,
		Capabilities: capabilityJSON, Snapshot: document, Usage: usage, RotatedAuthCipher: rotatedCipher,
	}); err != nil {
		return AccountView{}, s.recordFailure(ctx, upstreamID, attemptedAt, capabilities, err, rotatedCipher)
	}
	return s.Get(ctx, upstreamID)
}

func (s *Service) Usage(ctx context.Context, upstreamID int64, rangeName string, limit int, beforeIDs ...int64) (UsagePageView, error) {
	account, err := s.store.GetUpstreamAccount(ctx, upstreamID)
	if err != nil {
		return UsagePageView{}, err
	}
	duration, ok := usageRange(rangeName)
	if !ok {
		return UsagePageView{}, fmt.Errorf("unsupported usage range %q", rangeName)
	}
	var beforeID int64
	if len(beforeIDs) > 0 {
		beforeID = beforeIDs[0]
	}
	items, err := s.store.ListUsage(ctx, upstreamID, store.UpstreamAccountUsageQuery{
		SinceAt: time.Now().Add(-duration).UnixMilli(), BeforeID: beforeID, Limit: limit + 1,
	})
	if err != nil {
		return UsagePageView{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	result := UsagePageView{
		Items: make([]UsageItemView, 0, len(items)), Range: rangeName,
		LastSyncedAt: isoPointer(account.LastSuccessAt), HasMore: hasMore,
	}
	for _, item := range items {
		result.Items = append(result.Items, usageItemView(item))
	}
	if hasMore && len(items) > 0 {
		cursor := strconv.FormatInt(items[len(items)-1].ID, 10)
		result.NextBeforeID = &cursor
	}
	return result, nil
}

func (s *Service) ConfigureSite(ctx context.Context, siteID int64, input ConfigureInput) (AccountView, error) {
	mutex := s.siteMutex(siteID)
	mutex.Lock()
	defer mutex.Unlock()

	if _, err := s.store.GetSite(ctx, siteID); err != nil {
		return AccountView{}, err
	}
	descriptor, ok := descriptorFor(input.AdapterKey)
	if !ok {
		return AccountView{}, ErrUnsupportedAdapter
	}
	origin, err := normalizeOrigin(input.DashboardURL)
	if err != nil {
		return AccountView{}, err
	}

	existing, existingErr := s.store.GetSiteAccountSecret(ctx, siteID)
	var envelope authEnvelope
	if existingErr == nil {
		envelope, err = s.decryptEnvelope(existing.AuthCipher)
		if err != nil {
			return AccountView{}, err
		}
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return AccountView{}, existingErr
	}

	nextEnvelope, err := mergeAuth(envelope, descriptor, input.Auth, existingErr == nil && existing.AdapterKind == descriptor.Key)
	if err != nil {
		return AccountView{}, err
	}
	authCipher, err := s.encryptEnvelope(nextEnvelope)
	if err != nil {
		return AccountView{}, err
	}
	capabilities, err := json.Marshal(descriptor.Capabilities)
	if err != nil {
		return AccountView{}, err
	}

	if errors.Is(existingErr, sql.ErrNoRows) {
		_, err = s.store.CreateSiteAccount(ctx, store.SiteAccountWrite{
			SiteID: siteID, AdapterKind: descriptor.Key, APIOrigin: origin,
			AuthCipher: authCipher, Enabled: input.Enabled, Capabilities: capabilities,
		})
	} else {
		err = s.store.UpdateSiteAccount(ctx, siteID, store.SiteAccountUpdate{
			AdapterKind: descriptor.Key, APIOrigin: origin, AuthCipher: authCipher,
			ReplaceAuth: true, Enabled: input.Enabled, Capabilities: capabilities,
		})
	}
	if err != nil {
		return AccountView{}, err
	}
	if input.RefreshNow && input.Enabled {
		_, _ = s.refreshSiteLocked(ctx, siteID)
	}
	return s.GetSite(ctx, siteID)
}

func (s *Service) DeleteSite(ctx context.Context, siteID int64) error {
	mutex := s.siteMutex(siteID)
	mutex.Lock()
	defer mutex.Unlock()
	return s.store.DeleteSiteAccount(ctx, siteID)
}

func (s *Service) GetSite(ctx context.Context, siteID int64) (AccountView, error) {
	site, err := s.store.GetSite(ctx, siteID)
	if err != nil {
		return AccountView{}, err
	}
	account, err := s.store.GetSiteAccountSecret(ctx, siteID)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountView{
			Configured: false, DashboardURL: s.defaultSiteOrigin(ctx, site),
			Capabilities: Capabilities{}, Sync: SyncView{State: "unconfigured"},
		}, nil
	}
	if err != nil {
		return AccountView{}, err
	}
	descriptor, ok := descriptorFor(account.AdapterKind)
	if !ok {
		return AccountView{}, ErrUnsupportedAdapter
	}
	envelope, err := s.decryptEnvelope(account.AuthCipher)
	if err != nil {
		return AccountView{}, err
	}
	view := AccountView{
		Configured: true, Enabled: account.Enabled, DashboardURL: account.APIOrigin,
		Adapter: &AdapterSummary{Key: descriptor.Key, Label: descriptor.Label},
		Auth:    authSummary(envelope), Capabilities: capabilitiesFrom(account.Capabilities, descriptor.Capabilities),
		Sync: s.siteSyncView(account.SiteAccount),
	}
	snapshot, err := s.store.GetLatestSiteAccountSnapshot(ctx, siteID)
	if err == nil {
		view.Snapshot, err = snapshotViewDocument(snapshot.Snapshot, snapshot.CapturedAt, descriptor.Key)
		if err != nil {
			return AccountView{}, err
		}
		if view.Sync.Error == nil {
			var document snapshotDocument
			if json.Unmarshal(snapshot.Snapshot, &document) == nil && len(document.Warnings) > 0 {
				view.Sync.Error = &SyncErrorView{Code: "partial_sync", Message: warningMessage(document.Warnings)}
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AccountView{}, err
	}
	return view, nil
}

func (s *Service) RefreshSite(ctx context.Context, siteID int64) (AccountView, error) {
	mutex := s.siteMutex(siteID)
	if !mutex.TryLock() {
		return AccountView{}, ErrSyncInProgress
	}
	defer mutex.Unlock()
	return s.refreshSiteLocked(ctx, siteID)
}

func (s *Service) refreshSiteLocked(ctx context.Context, siteID int64) (AccountView, error) {
	key := siteSyncKey(siteID)
	s.syncing.Store(key, struct{}{})
	defer s.syncing.Delete(key)

	account, err := s.store.GetSiteAccountSecret(ctx, siteID)
	if err != nil {
		return AccountView{}, err
	}
	if !account.Enabled {
		return AccountView{}, ErrAccountDisabled
	}
	descriptor, ok := descriptorFor(account.AdapterKind)
	if !ok {
		return AccountView{}, ErrUnsupportedAdapter
	}
	envelope, err := s.decryptEnvelope(account.AuthCipher)
	if err != nil {
		return AccountView{}, err
	}
	adapter, err := accountadapter.New(accountadapter.Kind(descriptor.Key), s.doer)
	if err != nil {
		return AccountView{}, err
	}

	attemptedAt := store.NowMS()
	connection := accountadapter.Connection{Origin: account.APIOrigin, Credentials: envelope.Credentials}
	snapshot, rotated, err := adapter.Snapshot(ctx, connection)
	applyRotation(&connection, rotated)
	if err != nil {
		return AccountView{}, s.recordSiteFailureWithCredentials(ctx, siteID, attemptedAt, descriptor.Capabilities, envelope, connection.Credentials, err)
	}

	capabilities := descriptor.Capabilities
	warnings := make([]syncWarning, 0, 2)
	var subscriptions []accountadapter.Subscription
	if capabilities.Subscription {
		subscriptions, rotated, err = adapter.Subscriptions(ctx, connection)
		applyRotation(&connection, rotated)
		if err != nil {
			if errors.Is(err, accountadapter.ErrUnsupported) {
				capabilities.Subscription = false
			} else {
				warnings = append(warnings, warningFor(err))
			}
		}
	}

	usage := make([]store.UpstreamAccountUsageWrite, 0)
	if capabilities.Usage {
		usageStart := time.Now().Add(-usageInitialRange)
		if account.LastSuccessAt != nil {
			incrementalStart := time.UnixMilli(*account.LastSuccessAt).Add(-usageSyncOverlap)
			if incrementalStart.After(usageStart) {
				usageStart = incrementalStart
			}
		}
		usage, rotated, err = s.fetchUsage(ctx, adapter, connection, descriptor.Key, attemptedAt, usageStart)
		applyRotation(&connection, rotated)
		if err != nil {
			if errors.Is(err, accountadapter.ErrUnsupported) {
				capabilities.Usage = false
			} else {
				warnings = append(warnings, warningFor(err))
			}
			usage = nil
		}
	}

	document, err := json.Marshal(snapshotDocument{
		Version: 1, Account: snapshot, Subscriptions: subscriptions, Warnings: warnings,
	})
	if err != nil {
		return AccountView{}, s.recordSiteFailureWithCredentials(ctx, siteID, attemptedAt, capabilities, envelope, connection.Credentials, err)
	}
	capabilityJSON, err := json.Marshal(capabilities)
	if err != nil {
		return AccountView{}, s.recordSiteFailureWithCredentials(ctx, siteID, attemptedAt, capabilities, envelope, connection.Credentials, err)
	}
	rotatedCipher, err := s.rotatedAuthCipher(envelope, connection.Credentials)
	if err != nil {
		return AccountView{}, s.recordSiteFailure(ctx, siteID, attemptedAt, capabilities, err, nil)
	}
	succeededAt := store.NowMS()
	if err := s.store.UpdateSiteAccountSyncSuccess(ctx, siteID, store.SiteAccountSyncSuccess{
		AttemptedAt: attemptedAt, SucceededAt: succeededAt, SnapshotAt: succeededAt,
		Capabilities: capabilityJSON, Snapshot: document, Usage: usage, RotatedAuthCipher: rotatedCipher,
	}); err != nil {
		return AccountView{}, s.recordSiteFailure(ctx, siteID, attemptedAt, capabilities, err, rotatedCipher)
	}
	return s.GetSite(ctx, siteID)
}

func (s *Service) SiteUsage(ctx context.Context, siteID int64, rangeName string, limit int, beforeIDs ...int64) (UsagePageView, error) {
	account, err := s.store.GetSiteAccount(ctx, siteID)
	if err != nil {
		return UsagePageView{}, err
	}
	duration, ok := usageRange(rangeName)
	if !ok {
		return UsagePageView{}, fmt.Errorf("unsupported usage range %q", rangeName)
	}
	var beforeID int64
	if len(beforeIDs) > 0 {
		beforeID = beforeIDs[0]
	}
	items, err := s.store.ListSiteAccountUsage(ctx, siteID, store.UpstreamAccountUsageQuery{
		SinceAt: time.Now().Add(-duration).UnixMilli(), BeforeID: beforeID, Limit: limit + 1,
	})
	if err != nil {
		return UsagePageView{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	result := UsagePageView{
		Items: make([]UsageItemView, 0, len(items)), Range: rangeName,
		LastSyncedAt: isoPointer(account.LastSuccessAt), HasMore: hasMore,
	}
	for _, item := range items {
		result.Items = append(result.Items, usageItemView(store.UpstreamAccountUsageRecord{
			ID: item.ID, DedupeKey: item.DedupeKey, ExternalID: item.ExternalID, ModelName: item.ModelName,
			Amount: item.Amount, Unit: item.Unit, Raw: item.Raw, OccurredAt: item.OccurredAt, SyncedAt: item.SyncedAt,
		}))
	}
	if hasMore && len(items) > 0 {
		cursor := strconv.FormatInt(items[len(items)-1].ID, 10)
		result.NextBeforeID = &cursor
	}
	return result, nil
}

func (s *Service) Run(ctx context.Context) {
	s.syncDue(ctx)
	tickEvery := s.interval / 6
	if tickEvery < time.Minute {
		tickEvery = time.Minute
	}
	if tickEvery > 5*time.Minute {
		tickEvery = 5 * time.Minute
	}
	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncDue(ctx)
		}
	}
}

func (s *Service) syncDue(ctx context.Context) {
	accounts, err := s.store.ListUpstreamAccounts(ctx)
	if err != nil {
		s.logger.Warn("list upstream accounts for synchronization", "error", redact.String(err.Error()))
		return
	}
	cutoff := time.Now().Add(-s.interval).UnixMilli()
	for _, account := range accounts {
		if !account.Enabled || (account.LastAttemptAt != nil && *account.LastAttemptAt > cutoff) {
			continue
		}
		if _, err := s.Refresh(ctx, account.UpstreamID); err != nil &&
			!errors.Is(err, context.Canceled) && !errors.Is(err, ErrSyncInProgress) {
			s.logger.Warn("synchronize upstream account", "upstream_id", account.UpstreamID, "error", redact.String(err.Error()))
		}
		if ctx.Err() != nil {
			return
		}
	}
	siteAccounts, err := s.store.ListSiteAccounts(ctx)
	if err != nil {
		s.logger.Warn("list site accounts for synchronization", "error", redact.String(err.Error()))
		return
	}
	for _, account := range siteAccounts {
		if !account.Enabled || (account.LastAttemptAt != nil && *account.LastAttemptAt > cutoff) {
			continue
		}
		if _, err := s.RefreshSite(ctx, account.SiteID); err != nil &&
			!errors.Is(err, context.Canceled) && !errors.Is(err, ErrSyncInProgress) {
			s.logger.Warn("synchronize site account", "site_id", account.SiteID, "error", redact.String(err.Error()))
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func (s *Service) fetchUsage(ctx context.Context, adapter accountadapter.Adapter, connection accountadapter.Connection, adapterKey string, syncedAt int64, start time.Time) ([]store.UpstreamAccountUsageWrite, *accountadapter.Credentials, error) {
	items := make([]store.UpstreamAccountUsageWrite, 0, usageSyncLimit*usageSyncPages)
	current := connection
	var latestRotation *accountadapter.Credentials
	now := time.Now()
	for page := 1; page <= usageSyncPages; page++ {
		result, rotated, err := adapter.Usage(ctx, current, accountadapter.UsageQuery{
			Page: page, PageSize: usageSyncLimit, StartUnix: start.Unix(), EndUnix: now.Unix(),
			SortBy: "created_at", SortOrder: "desc",
		})
		if rotated != nil {
			copy := *rotated
			latestRotation = &copy
			current.Credentials = copy
		}
		if err != nil {
			return nil, latestRotation, err
		}
		for _, item := range result.Items {
			items = append(items, usageWrite(adapterKey, item, syncedAt))
		}
		if !result.HasMore || len(result.Items) == 0 {
			break
		}
	}
	sort.SliceStable(items, func(left, right int) bool {
		leftAt, rightAt := items[left].OccurredAt, items[right].OccurredAt
		if leftAt == nil {
			return rightAt != nil
		}
		if rightAt == nil {
			return false
		}
		return *leftAt < *rightAt
	})
	return items, latestRotation, nil
}

func (s *Service) recordFailureWithCredentials(ctx context.Context, upstreamID, attemptedAt int64, capabilities Capabilities, envelope authEnvelope, credentials accountadapter.Credentials, cause error) error {
	rotatedCipher, err := s.rotatedAuthCipher(envelope, credentials)
	if err != nil {
		s.logger.Error("encrypt rotated upstream account credentials", "upstream_id", upstreamID, "error", redact.String(err.Error()))
		rotatedCipher = nil
	}
	return s.recordFailure(ctx, upstreamID, attemptedAt, capabilities, cause, rotatedCipher)
}

func (s *Service) recordFailure(ctx context.Context, upstreamID, attemptedAt int64, capabilities Capabilities, cause error, rotatedCipher []byte) error {
	classified := classify(cause)
	capabilityJSON, _ := json.Marshal(capabilities)
	if err := s.store.UpdateSyncFailure(ctx, upstreamID, store.UpstreamAccountSyncFailure{
		AttemptedAt: attemptedAt, State: "error", ErrorCode: classified.Code,
		ErrorMessage: classified.Error(), Capabilities: capabilityJSON, RotatedAuthCipher: rotatedCipher,
	}); err != nil {
		s.logger.Error("record upstream account synchronization failure", "upstream_id", upstreamID, "error", redact.String(err.Error()))
	}
	return classified
}

func (s *Service) recordSiteFailureWithCredentials(ctx context.Context, siteID, attemptedAt int64, capabilities Capabilities, envelope authEnvelope, credentials accountadapter.Credentials, cause error) error {
	rotatedCipher, err := s.rotatedAuthCipher(envelope, credentials)
	if err != nil {
		s.logger.Error("encrypt rotated site account credentials", "site_id", siteID, "error", redact.String(err.Error()))
		rotatedCipher = nil
	}
	return s.recordSiteFailure(ctx, siteID, attemptedAt, capabilities, cause, rotatedCipher)
}

func (s *Service) recordSiteFailure(ctx context.Context, siteID, attemptedAt int64, capabilities Capabilities, cause error, rotatedCipher []byte) error {
	classified := classify(cause)
	capabilityJSON, _ := json.Marshal(capabilities)
	if err := s.store.UpdateSiteAccountSyncFailure(ctx, siteID, store.SiteAccountSyncFailure{
		AttemptedAt: attemptedAt, State: "error", ErrorCode: classified.Code,
		ErrorMessage: classified.Error(), Capabilities: capabilityJSON, RotatedAuthCipher: rotatedCipher,
	}); err != nil {
		s.logger.Error("record site account synchronization failure", "site_id", siteID, "error", redact.String(err.Error()))
	}
	return classified
}

func (s *Service) syncView(account store.UpstreamAccount) SyncView {
	result := SyncView{
		State: "stale", LastAttemptAt: isoPointer(account.LastAttemptAt), LastSuccessAt: isoPointer(account.LastSuccessAt),
	}
	if account.Enabled {
		next := time.Now()
		if account.LastAttemptAt != nil {
			next = time.UnixMilli(*account.LastAttemptAt).Add(s.interval)
		}
		nextText := next.UTC().Format(time.RFC3339)
		result.NextAt = &nextText
	}
	if _, ok := s.syncing.Load(account.UpstreamID); ok {
		result.State = "syncing"
	} else if account.SyncState == "healthy" && account.LastSuccessAt != nil {
		result.State = "ready"
	} else if account.SyncState == "error" {
		result.State = "error"
	}
	if account.LastSuccessAt == nil || time.Since(time.UnixMilli(*account.LastSuccessAt)) > time.Duration(staleFactor)*s.interval {
		result.Stale = true
		if result.State == "ready" {
			result.State = "stale"
		}
	}
	if account.LastErrorCode != "" || account.LastErrorMessage != "" {
		result.Error = &SyncErrorView{Code: account.LastErrorCode, Message: account.LastErrorMessage}
	}
	return result
}

func (s *Service) siteSyncView(account store.SiteAccount) SyncView {
	result := SyncView{
		State: "stale", LastAttemptAt: isoPointer(account.LastAttemptAt), LastSuccessAt: isoPointer(account.LastSuccessAt),
	}
	if account.Enabled {
		next := time.Now()
		if account.LastAttemptAt != nil {
			next = time.UnixMilli(*account.LastAttemptAt).Add(s.interval)
		}
		nextText := next.UTC().Format(time.RFC3339)
		result.NextAt = &nextText
	}
	if _, ok := s.syncing.Load(siteSyncKey(account.SiteID)); ok {
		result.State = "syncing"
	} else if account.SyncState == "healthy" && account.LastSuccessAt != nil {
		result.State = "ready"
	} else if account.SyncState == "error" {
		result.State = "error"
	}
	if account.LastSuccessAt == nil || time.Since(time.UnixMilli(*account.LastSuccessAt)) > time.Duration(staleFactor)*s.interval {
		result.Stale = true
		if result.State == "ready" {
			result.State = "stale"
		}
	}
	if account.LastErrorCode != "" || account.LastErrorMessage != "" {
		result.Error = &SyncErrorView{Code: account.LastErrorCode, Message: account.LastErrorMessage}
	}
	return result
}

func (s *Service) upstreamMutex(upstreamID int64) *sync.Mutex {
	value, _ := s.locks.LoadOrStore(upstreamID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *Service) siteMutex(siteID int64) *sync.Mutex {
	value, _ := s.locks.LoadOrStore(siteSyncKey(siteID), &sync.Mutex{})
	return value.(*sync.Mutex)
}

func siteSyncKey(siteID int64) string {
	return "site:" + strconv.FormatInt(siteID, 10)
}

func (s *Service) rotatedAuthCipher(envelope authEnvelope, credentials accountadapter.Credentials) ([]byte, error) {
	if credentialsEqual(envelope.Credentials, credentials) {
		return nil, nil
	}
	envelope.Credentials = credentials
	return s.encryptEnvelope(envelope)
}

func (s *Service) encryptEnvelope(envelope authEnvelope) ([]byte, error) {
	envelope.Version = 1
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return s.cipher.Encrypt(string(encoded))
}

func (s *Service) decryptEnvelope(ciphertext []byte) (authEnvelope, error) {
	plain, err := s.cipher.Decrypt(ciphertext)
	if err != nil {
		return authEnvelope{}, err
	}
	var envelope authEnvelope
	if err := json.Unmarshal([]byte(plain), &envelope); err != nil {
		return authEnvelope{}, errors.New("cannot decode upstream account credentials")
	}
	if envelope.Version != 1 || envelope.Kind == "" {
		return authEnvelope{}, errors.New("unsupported upstream account credential format")
	}
	return envelope, nil
}

func descriptorFor(key string) (AdapterDescriptor, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, descriptor := range descriptors {
		if descriptor.Key == key {
			return descriptor, true
		}
	}
	return AdapterDescriptor{}, false
}

func mergeAuth(existing authEnvelope, descriptor AdapterDescriptor, input AuthInput, canReuse bool) (authEnvelope, error) {
	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	if !contains(descriptor.AuthKinds, kind) {
		return authEnvelope{}, fmt.Errorf("authentication kind %q is not supported by %s", kind, descriptor.Label)
	}
	result := authEnvelope{Version: 1, Kind: kind}
	if canReuse && existing.Kind == kind {
		result.Credentials = existing.Credentials
	}
	switch kind {
	case "api_token":
		if value := strings.TrimSpace(input.APIToken); value != "" {
			result.Credentials = accountadapter.Credentials{Authorization: value}
		}
		if strings.TrimSpace(result.Credentials.Authorization) == "" {
			return authEnvelope{}, ErrInvalidCredentials
		}
	case "access_refresh":
		if value := strings.TrimSpace(input.AccessToken); value != "" {
			result.Credentials.AccessToken = value
			result.Credentials.Authorization = ""
		}
		if value := strings.TrimSpace(input.RefreshToken); value != "" {
			result.Credentials.RefreshToken = value
		}
		if strings.TrimSpace(result.Credentials.AccessToken) == "" || strings.TrimSpace(result.Credentials.RefreshToken) == "" {
			return authEnvelope{}, ErrInvalidCredentials
		}
	default:
		return authEnvelope{}, ErrInvalidCredentials
	}
	return result, nil
}

func authSummary(envelope authEnvelope) *AuthSummary {
	result := &AuthSummary{Kind: envelope.Kind}
	switch envelope.Kind {
	case "api_token":
		result.HasAPIToken = strings.TrimSpace(envelope.Credentials.Authorization) != ""
	case "access_refresh":
		result.HasAccessToken = strings.TrimSpace(envelope.Credentials.AccessToken) != "" || strings.TrimSpace(envelope.Credentials.Authorization) != ""
		result.HasRefreshToken = strings.TrimSpace(envelope.Credentials.RefreshToken) != ""
		result.AccessTokenExpiresAt = credentialExpiry(envelope.Credentials.ExpiresAt)
	}
	return result
}

func normalizeOrigin(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("dashboard URL must be a valid HTTP or HTTPS origin")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("dashboard URL must use HTTP or HTTPS")
	}
	return value, nil
}

func defaultOrigin(item store.Upstream) string {
	if item.DashboardURL != "" {
		return strings.TrimRight(item.DashboardURL, "/")
	}
	parsed, err := url.Parse(item.BaseURL)
	if err != nil {
		return ""
	}
	path := strings.TrimRight(parsed.Path, "/")
	for _, suffix := range []string{"/api/v1", "/v1", "/api"} {
		if strings.HasSuffix(strings.ToLower(path), suffix) {
			path = path[:len(path)-len(suffix)]
			break
		}
	}
	parsed.Path = path
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func (s *Service) defaultSiteOrigin(ctx context.Context, item store.Site) string {
	if item.DashboardURL != "" {
		return strings.TrimRight(item.DashboardURL, "/")
	}
	endpoints, err := s.store.ListInferenceEndpoints(ctx, item.ID)
	if err != nil || len(endpoints) == 0 {
		return ""
	}
	return originFromBaseURL(endpoints[0].BaseURL)
}

func originFromBaseURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	path := strings.TrimRight(parsed.Path, "/")
	for _, suffix := range []string{"/api/v1", "/v1", "/api"} {
		if strings.HasSuffix(strings.ToLower(path), suffix) {
			path = path[:len(path)-len(suffix)]
			break
		}
	}
	parsed.Path = path
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func capabilitiesFrom(raw json.RawMessage, fallback Capabilities) Capabilities {
	var result Capabilities
	if len(raw) == 0 || json.Unmarshal(raw, &result) != nil {
		return fallback
	}
	return result
}

func snapshotView(snapshot store.UpstreamAccountSnapshot, adapterKey string) (*SnapshotView, error) {
	return snapshotViewDocument(snapshot.Snapshot, snapshot.CapturedAt, adapterKey)
}

func snapshotViewDocument(raw json.RawMessage, capturedAt int64, adapterKey string) (*SnapshotView, error) {
	var document snapshotDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, errors.New("cannot decode upstream account snapshot")
	}
	result := &SnapshotView{CapturedAt: time.UnixMilli(capturedAt).UTC().Format(time.RFC3339)}
	if document.Account.Balance != "" {
		currency := firstNonEmpty(document.Account.Currency, "raw")
		sourceLabel := "站点原始余额"
		if adapterKey == "new_api" || adapterKey == "one_api" {
			sourceLabel = "站点原始 quota"
		}
		result.Balance = &SourceAmount{Value: document.Account.Balance, Currency: currency, SourceLabel: sourceLabel}
	}
	if subscription := preferredSubscription(document.Subscriptions); subscription != nil {
		currency := firstNonEmpty(subscription.Currency, document.Account.Currency, "raw")
		result.Subscription = &SubscriptionView{
			PlanName: firstNonEmpty(subscription.Name, subscription.GroupName, "subscription"),
			Status:   optionalString(subscription.Status), ExpiresAt: normalizedTimePointer(subscription.ExpiresAt),
			RenewsAt: normalizedTimePointer(subscription.NextResetAt), PeriodStart: normalizedTimePointer(subscription.StartsAt),
			PeriodEnd: normalizedTimePointer(subscription.ExpiresAt),
		}
		if subscription.AmountRemaining != "" {
			result.Subscription.Remaining = &SourceAmount{Value: subscription.AmountRemaining, Currency: currency, SourceLabel: "站点原始套餐"}
		}
		if subscription.AmountTotal != "" {
			result.Subscription.Total = &SourceAmount{Value: subscription.AmountTotal, Currency: currency, SourceLabel: "站点原始套餐"}
		}
	}
	return result, nil
}

func preferredSubscription(items []accountadapter.Subscription) *accountadapter.Subscription {
	if len(items) == 0 {
		return nil
	}
	for index := range items {
		status := strings.ToLower(strings.TrimSpace(items[index].Status))
		if status == "active" || status == "valid" || status == "enabled" {
			return &items[index]
		}
	}
	return &items[0]
}

func usageWrite(adapterKey string, item accountadapter.UsageItem, syncedAt int64) store.UpstreamAccountUsageWrite {
	raw, _ := json.Marshal(item)
	externalID := firstNonEmpty(item.ID, item.RequestID, item.UpstreamRequestID)
	amount := item.ActualCost
	unit := "USD"
	if adapterKey == "new_api" || adapterKey == "one_api" {
		amount = item.Quota
		unit = "quota"
	} else if amount == "" {
		amount = firstNonEmpty(item.TotalCost, item.Quota)
	}
	var occurredAt *int64
	if parsed := parseTimestamp(item.CreatedAt); !parsed.IsZero() {
		value := parsed.UnixMilli()
		occurredAt = &value
	}
	return store.UpstreamAccountUsageWrite{
		DedupeKey: usageDedupe(item), ExternalID: externalID, ModelName: firstNonEmpty(item.Model, item.UpstreamModel),
		Amount: amount, Unit: unit, Raw: raw, OccurredAt: occurredAt, SyncedAt: syncedAt,
	}
}

func usageDedupe(item accountadapter.UsageItem) string {
	if item.ID != "" {
		return "id:" + item.ID
	}
	value := strings.Join([]string{
		item.RequestID, item.UpstreamRequestID, item.CreatedAt, item.Model, item.UpstreamModel,
		strconv.FormatInt(item.PromptTokens, 10), strconv.FormatInt(item.CompletionTokens, 10),
		item.Quota, item.TotalCost, item.ActualCost, item.Content,
	}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func usageItemView(record store.UpstreamAccountUsageRecord) UsageItemView {
	result := UsageItemView{
		ID: strconv.FormatInt(record.ID, 10), ExternalID: record.ExternalID,
		SyncedAt: time.UnixMilli(record.SyncedAt).UTC().Format(time.RFC3339),
	}
	if record.OccurredAt != nil {
		text := time.UnixMilli(*record.OccurredAt).UTC().Format(time.RFC3339)
		result.OccurredAt = &text
	}
	if record.ModelName != "" {
		model := record.ModelName
		result.Model = &model
	}
	if record.Amount != "" {
		result.Amount = &SourceAmount{Value: record.Amount, Currency: firstNonEmpty(record.Unit, "raw"), SourceLabel: "站点原始记录"}
	}
	var item accountadapter.UsageItem
	if json.Unmarshal(record.Raw, &item) == nil {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(record.Raw, &fields)
		result.RequestID = item.RequestID
		result.UpstreamRequestID = item.UpstreamRequestID
		result.UpstreamModel = nonEmptyStringPointer(item.UpstreamModel)
		result.ReasoningEffort = nonEmptyStringPointer(item.ReasoningEffort)
		result.OriginalCost = nonEmptyStringPointer(item.TotalCost)
		result.ActualCost = nonEmptyStringPointer(item.ActualCost)
		result.Quota = nonEmptyStringPointer(item.Quota)
		result.InputTokens = presentInt64(fields, "prompt_tokens", item.PromptTokens)
		result.CacheReadTokens = presentInt64(fields, "cache_read_tokens", item.CacheReadTokens)
		result.CacheCreationTokens = presentInt64(fields, "cache_creation_tokens", item.CacheCreationTokens)
		result.OutputTokens = presentInt64(fields, "completion_tokens", item.CompletionTokens)
		result.ReasoningTokens = presentInt64(fields, "reasoning_tokens", item.ReasoningTokens)
		result.TotalTokens = presentInt64(fields, "total_tokens", item.TotalTokens)
		result.DurationMS = presentInt64(fields, "duration_ms", item.DurationMS)
		result.FirstTokenMS = presentInt64(fields, "first_token_ms", item.FirstTokenMS)
		result.Stream = presentBool(fields, "stream", item.Stream)
		result.RateMultiplier = nonEmptyStringPointer(item.RateMultiplier)
		result.ModelMultiplier = nonEmptyStringPointer(item.ModelMultiplier)
		result.GroupMultiplier = nonEmptyStringPointer(item.GroupMultiplier)
		result.APIKeyID, result.APIKeyName = item.APIKeyID, item.APIKeyName
		result.GroupID, result.GroupName = item.GroupID, item.GroupName
		result.Endpoint = item.Endpoint
		result.RequestType, result.BillingType, result.BillingMode = item.Type, item.BillingType, item.BillingMode
		result.SourceText = firstNonEmpty(item.Content, item.RequestID, item.UpstreamRequestID)
		if statusCode := presentInt(fields, "status_code", item.StatusCode); statusCode != nil {
			result.HTTPStatus = statusCode
			status := fmt.Sprintf("HTTP %d", item.StatusCode)
			if item.StatusCode >= 200 && item.StatusCode < 300 {
				status = "success"
			}
			result.Status = &status
		}
	}
	return result
}

func nonEmptyStringPointer(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func presentInt64(fields map[string]json.RawMessage, key string, value int64) *int64 {
	if _, ok := fields[key]; !ok {
		return nil
	}
	copy := value
	return &copy
}

func presentInt(fields map[string]json.RawMessage, key string, value int) *int {
	if _, ok := fields[key]; !ok {
		return nil
	}
	copy := value
	return &copy
}

func presentBool(fields map[string]json.RawMessage, key string, value bool) *bool {
	if _, ok := fields[key]; !ok {
		return nil
	}
	copy := value
	return &copy
}

func warningFor(err error) syncWarning {
	classified := classify(err)
	return syncWarning{Code: classified.Code, Message: classified.Error()}
}

func warningMessage(items []syncWarning) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.Message)
	}
	return strings.Join(parts, "; ")
}

func classify(err error) *SyncError {
	if err == nil {
		return &SyncError{Code: "sync_failed", Err: errors.New("account synchronization failed")}
	}
	code := "sync_failed"
	var remote *accountadapter.RemoteError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.Is(err, accountadapter.ErrInvalidConnection), errors.Is(err, ErrInvalidCredentials):
		code = "invalid_configuration"
	case errors.Is(err, accountadapter.ErrUnsupported), errors.Is(err, accountadapter.ErrUnsupportedKind):
		code = "unsupported"
	case errors.As(err, &remote):
		switch {
		case remote.StatusCode == http.StatusUnauthorized || remote.StatusCode == http.StatusForbidden:
			code = "authentication_failed"
		case remote.StatusCode == http.StatusTooManyRequests:
			code = "rate_limited"
		case remote.StatusCode >= 500:
			code = "upstream_unavailable"
		case strings.HasPrefix(remote.Code, "MALFORMED"):
			code = "incompatible_response"
		default:
			code = "upstream_rejected"
		}
	}
	return &SyncError{Code: code, Err: errors.New(redact.String(err.Error()))}
}

func applyRotation(connection *accountadapter.Connection, rotated *accountadapter.Credentials) {
	if rotated != nil {
		connection.Credentials = *rotated
	}
}

func credentialsEqual(left, right accountadapter.Credentials) bool {
	return left.Authorization == right.Authorization && left.AccessToken == right.AccessToken &&
		left.RefreshToken == right.RefreshToken && left.ExpiresAt == right.ExpiresAt
}

func usageRange(value string) (time.Duration, bool) {
	switch value {
	case "24h":
		return 24 * time.Hour, true
	case "7d":
		return 7 * 24 * time.Hour, true
	case "30d":
		return 30 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

func credentialExpiry(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if parsed := parseTimestamp(value); !parsed.IsZero() {
		text := parsed.UTC().Format(time.RFC3339)
		return &text
	}
	return nil
}

func parseTimestamp(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed
	}
	integer, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}
	}
	if integer > 9_999_999_999 {
		return time.UnixMilli(integer)
	}
	return time.Unix(integer, 0)
}

func normalizedTimePointer(value string) *string {
	if parsed := parseTimestamp(value); !parsed.IsZero() {
		text := parsed.UTC().Format(time.RFC3339)
		return &text
	}
	return optionalString(value)
}

func isoPointer(value *int64) *string {
	if value == nil || *value <= 0 {
		return nil
	}
	text := time.UnixMilli(*value).UTC().Format(time.RFC3339)
	return &text
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
