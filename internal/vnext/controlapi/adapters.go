package controlapi

import (
	"context"
	"errors"

	"github.com/LuTianTian001/JieShan/internal/vnext/downstreamkeys"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

// StoreRepository is the control-plane projection of the canonical VNext
// store. It contains no legacy fallback and performs only type translation.
type StoreRepository struct {
	store *vnextstore.Store
}

func NewStoreRepository(store *vnextstore.Store) (*StoreRepository, error) {
	if store == nil {
		return nil, errors.New("VNext store is required")
	}
	return &StoreRepository{store: store}, nil
}

func (repository *StoreRepository) ListDownstreamKeys(ctx context.Context) ([]vnextstore.DownstreamKey, error) {
	return repository.store.ListDownstreamKeys(ctx)
}

func (repository *StoreRepository) GetDownstreamKey(ctx context.Context, id int64) (vnextstore.DownstreamKey, error) {
	return repository.store.GetDownstreamKey(ctx, id)
}

func (repository *StoreRepository) UpdateDownstreamKey(ctx context.Context, id int64, input KeyUpdate) (vnextstore.DownstreamKey, error) {
	return repository.store.UpdateDownstreamKey(ctx, id, vnextstore.DownstreamKeyUpdate{
		ExpectedRevision:     input.ExpectedRevision,
		Name:                 input.Name,
		RoutingProfileID:     cloneInt64(input.RoutingProfileID),
		Enabled:              input.Enabled,
		QuotaNanoUSD:         cloneInt64(input.QuotaNanoUSD),
		HourlyQuotaNanoUSD:   cloneInt64(input.HourlyQuotaNanoUSD),
		BillingMultiplierBPS: input.BillingMultiplierBPS,
		ExpiresAt:            cloneInt64(input.Expires),
	})
}

func (repository *StoreRepository) ListRoutingProfileRoutes(ctx context.Context, profileID int64) ([]vnextstore.RoutingProfileRoute, error) {
	return repository.store.ListRoutingProfileRoutes(ctx, profileID)
}

// KeyIssuerAdapter keeps raw key generation and encryption in downstreamkeys;
// the HTTP package only translates its validated request and one-time result.
type KeyIssuerAdapter struct {
	service *downstreamkeys.Service
}

func NewKeyIssuerAdapter(service *downstreamkeys.Service) (*KeyIssuerAdapter, error) {
	if service == nil {
		return nil, errors.New("downstream key service is required")
	}
	return &KeyIssuerAdapter{service: service}, nil
}

func (issuer *KeyIssuerAdapter) IssueDownstreamKey(ctx context.Context, input KeyCreate) (IssuedKey, error) {
	issued, err := issuer.service.Create(ctx, downstreamkeys.CreateInput{
		Name:                 input.Name,
		RoutingProfileID:     cloneInt64(input.RoutingProfileID),
		QuotaNanoUSD:         cloneInt64(input.QuotaNanoUSD),
		HourlyQuotaNanoUSD:   cloneInt64(input.HourlyQuotaNanoUSD),
		BillingMultiplierBPS: intPointer(input.BillingMultiplierBPS),
		ExpiresAt:            cloneInt64(input.Expires),
		Enabled:              input.Enabled,
	})
	if err != nil {
		return IssuedKey{}, err
	}
	return IssuedKey{Key: issued.Key, RawSecret: issued.RawSecret}, nil
}

func (issuer *KeyIssuerAdapter) RevealDownstreamKey(ctx context.Context, id int64) (string, error) {
	return issuer.service.Reveal(ctx, id)
}

func (issuer *KeyIssuerAdapter) RotateDownstreamKey(ctx context.Context, id, expectedRevision int64) (IssuedKey, error) {
	issued, err := issuer.service.Rotate(ctx, id, expectedRevision)
	if err != nil {
		return IssuedKey{}, err
	}
	return IssuedKey{Key: issued.Key, RawSecret: issued.RawSecret}, nil
}

// NewStoreHandler constructs a real VNext control-plane handler without
// importing or mounting the legacy httpapi package.
func NewStoreHandler(store *vnextstore.Store, keyService *downstreamkeys.Service) (*Handler, error) {
	repository, err := NewStoreRepository(store)
	if err != nil {
		return nil, err
	}
	issuer, err := NewKeyIssuerAdapter(keyService)
	if err != nil {
		return nil, err
	}
	return New(repository, issuer, issuer), nil
}

var _ Repository = (*StoreRepository)(nil)
var _ KeyIssuer = (*KeyIssuerAdapter)(nil)
var _ KeySecretManager = (*KeyIssuerAdapter)(nil)

func intPointer(value int) *int {
	copy := value
	return &copy
}
