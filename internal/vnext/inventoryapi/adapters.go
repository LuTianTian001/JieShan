package inventoryapi

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/vnext/platformdetect"
	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const siteCredentialCipherVersion = int64(1)

// StoreRepository is the inventory control-plane projection of the canonical
// VNext store. It owns record-bound secret sealing and discovery decryption so
// plaintext credentials never enter Store or any response type.
type StoreRepository struct {
	store    *vnextstore.Store
	box      *secretbox.Box
	registry *vnextprotocol.Registry
}

func NewStoreRepository(store *vnextstore.Store, box *secretbox.Box, registry *vnextprotocol.Registry) (*StoreRepository, error) {
	if store == nil || box == nil || registry == nil {
		return nil, errors.New("VNext store, secret box, and protocol registry are required")
	}
	return &StoreRepository{store: store, box: box, registry: registry}, nil
}

func NewStoreHandler(store *vnextstore.Store, box *secretbox.Box, registry *vnextprotocol.Registry) (*Handler, error) {
	repository, err := NewStoreRepository(store, box, registry)
	if err != nil {
		return nil, err
	}
	return New(repository)
}

func NewStoreHandlerWithPlatformDetector(
	store *vnextstore.Store,
	box *secretbox.Box,
	registry *vnextprotocol.Registry,
	detector PlatformDetector,
) (*Handler, error) {
	repository, err := NewStoreRepository(store, box, registry)
	if err != nil {
		return nil, err
	}
	return NewWithPlatformDetector(repository, detector)
}

func (repository *StoreRepository) ListSites(ctx context.Context) ([]vnextstore.Site, error) {
	return repository.store.ListSites(ctx)
}

func (repository *StoreRepository) CreateSite(ctx context.Context, input vnextstore.SiteWrite) (vnextstore.Site, error) {
	id, err := repository.store.CreateSite(ctx, input)
	if err != nil {
		return vnextstore.Site{}, err
	}
	return repository.store.GetSite(ctx, id)
}

func (repository *StoreRepository) GetSite(ctx context.Context, id int64) (vnextstore.Site, error) {
	return repository.store.GetSite(ctx, id)
}

func (repository *StoreRepository) GetPlatformSelection(
	ctx context.Context,
	siteID int64,
) (*platformdetect.ManualSelection, error) {
	connection, err := repository.store.GetSiteAccountConnection(ctx, siteID)
	if err != nil {
		return nil, err
	}
	return &platformdetect.ManualSelection{
		Platform: connection.AdapterKind,
		Origin:   connection.Origin,
		Locked:   true,
	}, nil
}

func (repository *StoreRepository) UpdateSite(ctx context.Context, id int64, input vnextstore.SiteUpdate) (vnextstore.Site, error) {
	return repository.store.UpdateSite(ctx, id, input)
}

func (repository *StoreRepository) DeleteSite(ctx context.Context, id, expectedRevision int64) error {
	return repository.store.DeleteSite(ctx, id, expectedRevision)
}

func (repository *StoreRepository) ListSiteEndpoints(ctx context.Context, siteID int64) ([]vnextstore.SiteEndpoint, error) {
	return repository.store.ListSiteEndpoints(ctx, siteID)
}

func (repository *StoreRepository) CreateSiteEndpoint(ctx context.Context, siteID int64, input vnextstore.SiteEndpointWrite) (vnextstore.SiteEndpoint, error) {
	if _, err := repository.GetSite(ctx, siteID); err != nil {
		return vnextstore.SiteEndpoint{}, err
	}
	id, err := repository.store.CreateSiteEndpoint(ctx, siteID, input)
	if err != nil {
		return vnextstore.SiteEndpoint{}, err
	}
	return repository.GetSiteEndpoint(ctx, siteID, id)
}

func (repository *StoreRepository) GetSiteEndpoint(ctx context.Context, siteID, endpointID int64) (vnextstore.SiteEndpoint, error) {
	item, err := repository.store.GetSiteEndpoint(ctx, endpointID)
	if err != nil {
		return vnextstore.SiteEndpoint{}, err
	}
	if item.SiteID != siteID {
		return vnextstore.SiteEndpoint{}, sql.ErrNoRows
	}
	return item, nil
}

func (repository *StoreRepository) UpdateSiteEndpoint(ctx context.Context, siteID, endpointID int64, input vnextstore.SiteEndpointUpdate) (vnextstore.SiteEndpoint, error) {
	return repository.store.UpdateSiteEndpoint(ctx, siteID, endpointID, input)
}

func (repository *StoreRepository) ListEndpointCredentialBindings(ctx context.Context, siteID, endpointID int64) ([]vnextstore.CredentialEndpointBinding, error) {
	if _, err := repository.GetSiteEndpoint(ctx, siteID, endpointID); err != nil {
		return nil, err
	}
	return repository.store.ListEndpointCredentialBindings(ctx, endpointID)
}

func (repository *StoreRepository) ReplaceEndpointCredentialBindings(
	ctx context.Context,
	siteID, endpointID, expectedRevision int64,
	credentialIDs []int64,
) (vnextstore.SiteEndpoint, error) {
	for _, credentialID := range credentialIDs {
		if _, err := repository.GetSiteCredential(ctx, siteID, credentialID); err != nil {
			return vnextstore.SiteEndpoint{}, err
		}
	}
	if err := repository.store.ReplaceEndpointCredentialBindings(ctx, siteID, endpointID, expectedRevision, credentialIDs); err != nil {
		return vnextstore.SiteEndpoint{}, err
	}
	return repository.GetSiteEndpoint(ctx, siteID, endpointID)
}

func (repository *StoreRepository) ListSiteCredentials(ctx context.Context, siteID int64) ([]CredentialRecord, error) {
	items, err := repository.store.ListSiteCredentials(ctx, siteID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	runtimeStates, err := repository.store.ListCredentialRuntimeStates(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]CredentialRecord, 0, len(items))
	for _, item := range items {
		result = append(result, CredentialRecord{Credential: item, Runtime: runtimeStates[item.ID]})
	}
	return result, nil
}

func (repository *StoreRepository) CreateSiteCredential(ctx context.Context, siteID int64, input CredentialCreate) (CredentialRecord, error) {
	if len(input.Secret) == 0 {
		return CredentialRecord{}, errors.New("credential secret is required")
	}
	if _, err := repository.GetSite(ctx, siteID); err != nil {
		return CredentialRecord{}, err
	}
	id, err := repository.store.CreateSealedSiteCredential(ctx, siteID, vnextstore.SealedSiteCredentialInput{
		Name: input.Name, CipherVersion: siteCredentialCipherVersion, Enabled: input.Enabled,
	}, func(credentialID, ownerSiteID int64) ([]byte, error) {
		return repository.box.Seal(secretbox.PurposeSiteCredential, secretbox.Identity{
			RecordID: credentialID,
			OwnerID:  ownerSiteID,
		}, input.Secret)
	})
	if err != nil {
		return CredentialRecord{}, err
	}
	return repository.GetSiteCredential(ctx, siteID, id)
}

func (repository *StoreRepository) GetSiteCredential(ctx context.Context, siteID, credentialID int64) (CredentialRecord, error) {
	item, err := repository.store.GetSiteCredential(ctx, credentialID)
	if err != nil {
		return CredentialRecord{}, err
	}
	if item.SiteID != siteID {
		return CredentialRecord{}, sql.ErrNoRows
	}
	runtimeState, err := repository.store.GetCredentialRuntimeState(ctx, credentialID)
	if err != nil {
		return CredentialRecord{}, err
	}
	return CredentialRecord{Credential: item, Runtime: runtimeState}, nil
}

func (repository *StoreRepository) UpdateSiteCredential(ctx context.Context, siteID, credentialID int64, input vnextstore.SiteCredentialUpdate) (CredentialRecord, error) {
	if _, err := repository.store.UpdateSiteCredential(ctx, siteID, credentialID, input); err != nil {
		return CredentialRecord{}, err
	}
	return repository.GetSiteCredential(ctx, siteID, credentialID)
}

func (repository *StoreRepository) ReplaceSiteCredentialSecret(ctx context.Context, siteID, credentialID int64, input CredentialSecretUpdate) (CredentialRecord, error) {
	if len(input.Secret) == 0 {
		return CredentialRecord{}, errors.New("credential secret is required")
	}
	ciphertext, err := repository.box.Seal(secretbox.PurposeSiteCredential, secretbox.Identity{
		RecordID: credentialID,
		OwnerID:  siteID,
	}, input.Secret)
	if err != nil {
		return CredentialRecord{}, err
	}
	defer clear(ciphertext)
	if _, err := repository.store.ReplaceSealedSiteCredentialSecret(
		ctx, siteID, credentialID, input.ExpectedRevision, siteCredentialCipherVersion, ciphertext,
	); err != nil {
		return CredentialRecord{}, err
	}
	return repository.GetSiteCredential(ctx, siteID, credentialID)
}

func (repository *StoreRepository) ImportTokenJSONItems(
	ctx context.Context,
	siteID int64,
	items []TokenJSONImportItem,
) (TokenJSONImportRecords, error) {
	if len(items) == 0 || len(items) > vnextstore.MaxSealedEndpointCredentialImports {
		return TokenJSONImportRecords{}, errors.New("token JSON import item count is invalid")
	}
	imports := make([]vnextstore.SealedEndpointCredentialImport, len(items))
	for index, item := range items {
		if len(item.Secret) == 0 {
			return TokenJSONImportRecords{}, errors.New("credential secret is required")
		}
		imports[index] = vnextstore.SealedEndpointCredentialImport{
			Credential: vnextstore.SealedSiteCredentialInput{
				Name: item.CredentialName, CipherVersion: siteCredentialCipherVersion, Enabled: true,
			},
			Endpoint: vnextstore.SiteEndpointWrite{
				Name: item.EndpointName, BaseURL: item.BaseURL, WireProtocol: item.WireProtocol,
				Surface: item.Surface, AdapterKind: item.AdapterKind, AuthScheme: item.AuthScheme,
				HeaderTemplate: []byte(`{}`), Enabled: true,
			},
		}
	}
	stored, err := repository.store.ImportSealedEndpointCredentials(
		ctx,
		siteID,
		imports,
		func(index int, credentialID, ownerSiteID int64) ([]byte, error) {
			if index < 0 || index >= len(items) || len(items[index].Secret) == 0 {
				return nil, errors.New("credential secret is unavailable")
			}
			return repository.box.Seal(secretbox.PurposeSiteCredential, secretbox.Identity{
				RecordID: credentialID,
				OwnerID:  ownerSiteID,
			}, items[index].Secret)
		},
	)
	if err != nil {
		return TokenJSONImportRecords{}, err
	}
	result := TokenJSONImportRecords{
		CredentialIDs: make([]int64, 0, len(stored)),
		EndpointIDs:   make([]int64, 0, len(stored)),
	}
	for _, item := range stored {
		result.CredentialIDs = append(result.CredentialIDs, item.CredentialID)
		result.EndpointIDs = append(result.EndpointIDs, item.EndpointID)
	}
	return result, nil
}

func (repository *StoreRepository) ListProviderModelTargets(ctx context.Context, siteID, endpointID int64) ([]vnextstore.ProviderModelTarget, error) {
	return repository.store.ListProviderModelTargets(ctx, siteID, endpointID)
}

func (repository *StoreRepository) CreateProviderModelTarget(ctx context.Context, input vnextstore.ProviderModelTargetWrite) (vnextstore.ProviderModelTarget, error) {
	if _, err := repository.GetSiteEndpoint(ctx, input.SiteID, input.EndpointID); err != nil {
		return vnextstore.ProviderModelTarget{}, err
	}
	id, err := repository.store.CreateProviderModelTarget(ctx, input)
	if err != nil {
		return vnextstore.ProviderModelTarget{}, err
	}
	return repository.GetProviderModelTarget(ctx, input.SiteID, input.EndpointID, id)
}

func (repository *StoreRepository) GetProviderModelTarget(ctx context.Context, siteID, endpointID, targetID int64) (vnextstore.ProviderModelTarget, error) {
	item, err := repository.store.GetProviderModelTarget(ctx, targetID)
	if err != nil {
		return vnextstore.ProviderModelTarget{}, err
	}
	if item.SiteID != siteID || item.EndpointID != endpointID {
		return vnextstore.ProviderModelTarget{}, sql.ErrNoRows
	}
	return item, nil
}

func (repository *StoreRepository) UpdateProviderModelTarget(ctx context.Context, siteID, endpointID, targetID int64, input vnextstore.ProviderModelTargetUpdate) (vnextstore.ProviderModelTarget, error) {
	return repository.store.UpdateProviderModelTarget(ctx, siteID, endpointID, targetID, input)
}

func (repository *StoreRepository) ImportProviderModelTargets(ctx context.Context, siteID, endpointID, credentialID int64, models []string) ([]vnextstore.ProviderModelTarget, error) {
	return repository.store.ImportProviderModelTargets(ctx, siteID, endpointID, credentialID, models, vnextstore.NowMS())
}

func (repository *StoreRepository) DiscoverModels(ctx context.Context, siteID, endpointID, credentialID int64) (ModelDiscovery, error) {
	endpoint, err := repository.GetSiteEndpoint(ctx, siteID, endpointID)
	if err != nil {
		return ModelDiscovery{}, err
	}
	if _, err := repository.GetSiteCredential(ctx, siteID, credentialID); err != nil {
		return ModelDiscovery{}, err
	}
	bundle, err := repository.store.LoadRuntimeSecretBundle(ctx, siteID, endpointID, credentialID)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelDiscovery{}, ErrCredentialUnavailable
	}
	if err != nil || bundle.CredentialCipherVersion != siteCredentialCipherVersion {
		return ModelDiscovery{}, ErrCredentialUnavailable
	}
	plaintext, err := repository.box.Open(secretbox.PurposeSiteCredential, secretbox.Identity{
		RecordID: credentialID,
		OwnerID:  siteID,
	}, bundle.CredentialCipher)
	if err != nil {
		return ModelDiscovery{}, ErrCredentialUnavailable
	}
	defer clear(plaintext)
	protocolID, err := vnextprotocol.ParseProtocol(endpoint.WireProtocol)
	if err != nil {
		return ModelDiscovery{}, ErrDiscoveryUnavailable
	}
	surface, err := vnextprotocol.ParseSurface(endpoint.Surface)
	if err != nil {
		return ModelDiscovery{}, ErrDiscoveryUnavailable
	}
	authScheme, err := vnextprotocol.ParseAuthScheme(endpoint.AuthScheme)
	if err != nil {
		return ModelDiscovery{}, ErrDiscoveryUnavailable
	}
	discovery, err := repository.PreviewModels(ctx, ModelDiscoveryPreviewInput{
		BaseURL: endpoint.BaseURL, Protocol: protocolID, Surface: surface, AuthScheme: authScheme, Secret: plaintext,
	})
	if err != nil {
		return ModelDiscovery{}, err
	}
	checkedAt := vnextstore.NowMS()
	if err := repository.store.ApplyCredentialModelDiscovery(
		ctx, siteID, endpointID, credentialID, discovery.Models, discovery.Complete, checkedAt,
	); err != nil {
		return ModelDiscovery{}, err
	}
	return discovery, nil
}

// PreviewModels performs one protocol-native discovery request without
// consulting or mutating inventory state. The concrete adapter owns URL shape
// and authentication placement; its HTTP client owns SSRF and redirect policy.
func (repository *StoreRepository) PreviewModels(ctx context.Context, input ModelDiscoveryPreviewInput) (ModelDiscovery, error) {
	if len(input.Secret) == 0 {
		return ModelDiscovery{}, ErrDiscoveryAuthFailed
	}
	components, err := repository.registry.Components(input.Protocol, input.Surface)
	if err != nil || components.Discoverer == nil {
		return ModelDiscovery{}, ErrDiscoveryUnavailable
	}
	result, err := components.Discoverer.DiscoverModels(ctx, vnextprotocol.DiscoveryInput{
		BaseURL: input.BaseURL,
		Auth:    vnextprotocol.AuthInput{Scheme: input.AuthScheme, Secret: string(input.Secret)},
	})
	if err != nil {
		return ModelDiscovery{}, classifyDiscoveryFailure(ctx, err)
	}
	models, err := normalizeDiscoveredModels(result.Models)
	if err != nil {
		return ModelDiscovery{}, ErrDiscoveryFailed
	}
	return ModelDiscovery{Models: models, Complete: result.Complete}, nil
}

func classifyDiscoveryFailure(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return ErrDiscoveryTimedOut
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "credential_auth"):
		return ErrDiscoveryAuthFailed
	case strings.Contains(message, "credential_permission"):
		return ErrDiscoveryForbidden
	case strings.Contains(message, "credential_payment_required"):
		return ErrDiscoveryPayment
	case strings.Contains(message, "credential_rate_limited"):
		return ErrDiscoveryRateLimited
	default:
		return ErrDiscoveryFailed
	}
}

func (repository *StoreRepository) ListProviderModelTargetCatalog(ctx context.Context) ([]ModelTargetCatalogEntry, error) {
	items, err := repository.store.ListProviderModelTargetInventory(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ModelTargetCatalogEntry, 0, len(items))
	for _, item := range items {
		protocolID, protocolErr := vnextprotocol.ParseProtocol(item.WireProtocol)
		surface, surfaceErr := vnextprotocol.ParseSurface(item.Surface)
		contract := vnextprotocol.Contract{}
		if protocolErr == nil && surfaceErr == nil {
			contract, _ = repository.registry.Lookup(protocolID, surface)
		}
		routable := item.Enabled && item.SiteEnabled && item.EndpointEnabled && item.UsableCredentialCount > 0 && contract.Routable()
		result = append(result, ModelTargetCatalogEntry{Target: item, Capabilities: contract.Capabilities, Routable: routable})
	}
	return result, nil
}

func normalizeDiscoveredModels(models []string) ([]string, error) {
	if len(models) > 5000 {
		return nil, errors.New("too many discovered models")
	}
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, raw := range models {
		model := strings.TrimSpace(raw)
		if model == "" || len(model) > 512 {
			continue
		}
		if _, duplicate := seen[model]; duplicate {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	sort.Strings(result)
	return result, nil
}

var _ Repository = (*StoreRepository)(nil)
