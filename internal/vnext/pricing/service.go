package pricing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type CatalogSummary struct {
	Version            string    `json:"version"`
	Digest             string    `json:"digest"`
	SettlementCurrency string    `json:"settlement_currency"`
	Source             string    `json:"source"`
	SourceDigest       string    `json:"source_digest"`
	EntryCount         int       `json:"entry_count"`
	EffectiveAt        time.Time `json:"effective_at"`
	VerifiedAt         time.Time `json:"verified_at"`
	ImportedAt         time.Time `json:"imported_at"`
	Active             bool      `json:"active"`
}

type CatalogState struct {
	ActiveVersion string    `json:"active_version,omitempty"`
	Revision      int64     `json:"revision"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type RepositoryImportResult struct {
	Imported bool
}

type Repository interface {
	ListPriceCatalogs(context.Context) ([]CatalogSummary, error)
	GetPriceCatalog(context.Context, string) (Catalog, error)
	GetPriceCatalogState(context.Context) (CatalogState, error)
	ImportPriceCatalog(context.Context, Catalog) (RepositoryImportResult, error)
	ActivatePriceCatalog(context.Context, string, int64) (CatalogState, error)
}

type Service struct {
	repository Repository
	now        func() time.Time
	operations sync.RWMutex
	book       *Book
}

type ServiceOption func(*Service)

func WithClock(now func() time.Time) ServiceOption {
	return func(service *Service) {
		if now != nil {
			service.now = now
		}
	}
}

func NewService(repository Repository, options ...ServiceOption) (*Service, error) {
	if repository == nil {
		return nil, errors.New("price catalog repository is required")
	}
	service := &Service{repository: repository, now: func() time.Time { return time.Now().UTC() }}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service, nil
}

// NewRuntimeService restores all immutable catalogs and returns one object that
// can be shared by the gateway and pricing administration API. Imports and
// activations then update the in-memory Book immediately without dropping old
// settlement versions.
func NewRuntimeService(ctx context.Context, repository Repository, options ...ServiceOption) (*Service, error) {
	service, err := NewService(repository, options...)
	if err != nil {
		return nil, err
	}
	if _, err := service.EnsureBuiltinOfficialCatalog(ctx); err != nil {
		return nil, err
	}
	book, err := service.loadBook(ctx, false)
	if err != nil {
		return nil, err
	}
	service.book = book
	return service, nil
}

func (service *Service) List(ctx context.Context) ([]CatalogSummary, CatalogState, error) {
	items, err := service.repository.ListPriceCatalogs(ctx)
	if err != nil {
		return nil, CatalogState{}, err
	}
	state, err := service.repository.GetPriceCatalogState(ctx)
	if err != nil {
		return nil, CatalogState{}, err
	}
	return items, state, nil
}

func (service *Service) Get(ctx context.Context, version string) (Catalog, error) {
	return service.repository.GetPriceCatalog(ctx, strings.TrimSpace(version))
}

func (service *Service) State(ctx context.Context) (CatalogState, error) {
	return service.repository.GetPriceCatalogState(ctx)
}

type Preview struct {
	Candidate   Catalog      `json:"candidate"`
	State       CatalogState `json:"state"`
	Diff        CatalogDiff  `json:"diff"`
	CanActivate bool         `json:"can_activate"`
}

func (service *Service) Preview(ctx context.Context, input Catalog) (Preview, error) {
	candidate, err := service.prepare(input)
	if err != nil {
		return Preview{}, err
	}
	state, err := service.repository.GetPriceCatalogState(ctx)
	if err != nil {
		return Preview{}, err
	}
	var active *Catalog
	if state.ActiveVersion != "" {
		catalog, err := service.repository.GetPriceCatalog(ctx, state.ActiveVersion)
		if err != nil {
			return Preview{}, err
		}
		active = &catalog
	}
	diff := DiffCatalogs(active, candidate)
	return Preview{
		Candidate:   candidate,
		State:       state,
		Diff:        diff,
		CanActivate: !candidate.EffectiveAt.After(service.now().UTC()),
	}, nil
}

type ImportResult struct {
	Catalog  Catalog      `json:"catalog"`
	Imported bool         `json:"imported"`
	State    CatalogState `json:"state"`
}

type BuiltinBootstrapResult struct {
	CatalogVersion string       `json:"catalog_version"`
	CatalogDigest  string       `json:"catalog_digest"`
	Outcome        string       `json:"outcome"`
	Imported       bool         `json:"imported"`
	Activated      bool         `json:"activated"`
	State          CatalogState `json:"state"`
}

const (
	BuiltinOutcomeAlreadyCurrent    = "already_current"
	BuiltinOutcomeInstalled         = "installed"
	BuiltinOutcomeUpgraded          = "upgraded"
	BuiltinOutcomeActivatedExisting = "activated_existing"
	BuiltinOutcomeOperatorPreserved = "operator_catalog_preserved"
)

// EnsureBuiltinOfficialCatalog makes a brand-new store immediately usable and
// advances an active older JieShan snapshot to the bundled correction. An
// operator-selected catalog is never replaced automatically.
func (service *Service) EnsureBuiltinOfficialCatalog(ctx context.Context) (BuiltinBootstrapResult, error) {
	if service == nil {
		return BuiltinBootstrapResult{}, errors.New("price catalog service is unavailable")
	}
	candidate, err := PrepareOfficialCatalog(BuiltinOfficialUSDCatalog())
	if err != nil {
		return BuiltinBootstrapResult{}, fmt.Errorf("prepare built-in official catalog: %w", err)
	}
	result := BuiltinBootstrapResult{CatalogVersion: candidate.Version, CatalogDigest: candidate.Digest}
	items, state, err := service.List(ctx)
	if err != nil {
		return BuiltinBootstrapResult{}, err
	}
	result.State = state
	upgradingBuiltin := false
	upgradeFromVersion := ""
	if state.ActiveVersion != "" {
		if state.ActiveVersion == candidate.Version {
			result.Outcome = BuiltinOutcomeAlreadyCurrent
			return result, nil
		}
		active, loadErr := service.repository.GetPriceCatalog(ctx, state.ActiveVersion)
		if loadErr != nil {
			return BuiltinBootstrapResult{}, loadErr
		}
		if !olderBuiltinCatalog(active, candidate) {
			result.Outcome = BuiltinOutcomeOperatorPreserved
			return result, nil
		}
		upgradingBuiltin = true
		upgradeFromVersion = state.ActiveVersion
	}
	builtinPresent := false
	for _, item := range items {
		if item.Version != candidate.Version {
			if !upgradingBuiltin {
				result.Outcome = BuiltinOutcomeOperatorPreserved
				return result, nil
			}
			continue
		}
		if item.Digest != candidate.Digest {
			return BuiltinBootstrapResult{}, ErrCatalogVersionConflict
		}
		builtinPresent = true
	}
	if !builtinPresent {
		imported, err := service.Import(ctx, candidate, candidate.Digest)
		if err != nil {
			return BuiltinBootstrapResult{}, fmt.Errorf("import built-in official catalog: %w", err)
		}
		result.Imported = imported.Imported
		result.State = imported.State
	}
	state, err = service.State(ctx)
	if err != nil {
		return BuiltinBootstrapResult{}, err
	}
	if state.ActiveVersion != "" && state.ActiveVersion != upgradeFromVersion {
		result.State = state
		if state.ActiveVersion == candidate.Version {
			result.Outcome = BuiltinOutcomeAlreadyCurrent
		} else {
			result.Outcome = BuiltinOutcomeOperatorPreserved
		}
		return result, nil
	}
	state, err = service.Activate(ctx, candidate.Version, state.Revision)
	if errors.Is(err, ErrCatalogStateConflict) {
		current, stateErr := service.State(ctx)
		if stateErr != nil {
			return BuiltinBootstrapResult{}, stateErr
		}
		result.State = current
		if current.ActiveVersion != "" {
			if current.ActiveVersion == candidate.Version {
				result.Outcome = BuiltinOutcomeAlreadyCurrent
			} else {
				result.Outcome = BuiltinOutcomeOperatorPreserved
			}
			return result, nil
		}
		return BuiltinBootstrapResult{}, fmt.Errorf("activate built-in official catalog: %w", err)
	}
	if err != nil {
		return BuiltinBootstrapResult{}, fmt.Errorf("activate built-in official catalog: %w", err)
	}
	result.Activated = true
	result.State = state
	switch {
	case upgradingBuiltin:
		result.Outcome = BuiltinOutcomeUpgraded
	case result.Imported:
		result.Outcome = BuiltinOutcomeInstalled
	default:
		result.Outcome = BuiltinOutcomeActivatedExisting
	}
	return result, nil
}

func olderBuiltinCatalog(active, candidate Catalog) bool {
	if active.Source != builtinOfficialCatalogSource || candidate.Source != builtinOfficialCatalogSource {
		return false
	}
	activeDate, activeRevision, activeOK := builtinCatalogOrder(active.Version)
	candidateDate, candidateRevision, candidateOK := builtinCatalogOrder(candidate.Version)
	if !activeOK || !candidateOK {
		return false
	}
	return activeDate.Before(candidateDate) ||
		(activeDate.Equal(candidateDate) && activeRevision < candidateRevision)
}

func builtinCatalogOrder(version string) (time.Time, int, bool) {
	var year, month, day, revision int
	if _, err := fmt.Sscanf(strings.TrimSpace(version), "official-usd-%d-%d-%d-v%d", &year, &month, &day, &revision); err != nil || revision < 1 {
		return time.Time{}, 0, false
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day ||
		fmt.Sprintf("official-usd-%04d-%02d-%02d-v%d", year, month, day, revision) != strings.TrimSpace(version) {
		return time.Time{}, 0, false
	}
	return date, revision, true
}

// Import requires the digest returned by Preview. This makes the operator's
// review an explicit confirmation of the exact immutable catalog payload.
func (service *Service) Import(ctx context.Context, input Catalog, expectedDigest string) (ImportResult, error) {
	service.operations.Lock()
	defer service.operations.Unlock()
	catalog, err := service.prepare(input)
	if err != nil {
		return ImportResult{}, err
	}
	expectedDigest = strings.ToLower(strings.TrimSpace(expectedDigest))
	if !digestPattern.MatchString(expectedDigest) || expectedDigest != catalog.Digest {
		return ImportResult{}, ErrDigestConfirmation
	}
	catalog.ImportedAt = service.now().UTC().Round(0)
	result, err := service.repository.ImportPriceCatalog(ctx, catalog)
	if err != nil {
		return ImportResult{}, err
	}
	stored, err := service.repository.GetPriceCatalog(ctx, catalog.Version)
	if err != nil {
		return ImportResult{}, err
	}
	if service.book != nil {
		if err := service.book.Install(stored, false); err != nil {
			return ImportResult{}, fmt.Errorf("install imported catalog in runtime book: %w", err)
		}
	}
	state, err := service.repository.GetPriceCatalogState(ctx)
	if err != nil {
		return ImportResult{}, err
	}
	return ImportResult{Catalog: stored, Imported: result.Imported, State: state}, nil
}

func (service *Service) Activate(ctx context.Context, version string, expectedRevision int64) (CatalogState, error) {
	service.operations.Lock()
	defer service.operations.Unlock()
	catalog, err := service.repository.GetPriceCatalog(ctx, strings.TrimSpace(version))
	if err != nil {
		return CatalogState{}, err
	}
	prepared, err := service.prepare(catalog)
	if err != nil {
		return CatalogState{}, fmt.Errorf("stored catalog failed verification: %w", err)
	}
	if prepared.Digest != catalog.Digest {
		return CatalogState{}, errors.New("stored catalog digest is inconsistent")
	}
	if prepared.EffectiveAt.After(service.now().UTC()) {
		return CatalogState{}, ErrCatalogNotEffective
	}
	if service.book != nil {
		if err := service.book.Install(prepared, false); err != nil {
			return CatalogState{}, fmt.Errorf("install activation catalog in runtime book: %w", err)
		}
	}
	state, err := service.repository.ActivatePriceCatalog(ctx, prepared.Version, expectedRevision)
	if err != nil {
		return CatalogState{}, err
	}
	if service.book != nil {
		if err := service.book.Activate(prepared.Version); err != nil {
			return CatalogState{}, fmt.Errorf("activate runtime price book: %w", err)
		}
	}
	return state, nil
}

// BuildBook loads every immutable version, not only the active one. Historical
// request settlement therefore remains possible after restarts and activation.
func (service *Service) BuildBook(ctx context.Context) (*Book, error) {
	return service.loadBook(ctx, true)
}

func (service *Service) loadBook(ctx context.Context, requireActive bool) (*Book, error) {
	summaries, err := service.repository.ListPriceCatalogs(ctx)
	if err != nil {
		return nil, err
	}
	state, err := service.repository.GetPriceCatalogState(ctx)
	if err != nil {
		return nil, err
	}
	if requireActive && state.ActiveVersion == "" {
		return nil, ErrNoActiveCatalog
	}
	book := NewEmptyBook()
	for _, summary := range summaries {
		catalog, err := service.repository.GetPriceCatalog(ctx, summary.Version)
		if err != nil {
			return nil, err
		}
		prepared, err := service.prepare(catalog)
		if err != nil || prepared.Digest != catalog.Digest {
			if err == nil {
				err = errors.New("digest is inconsistent")
			}
			return nil, fmt.Errorf("stored catalog %q failed verification: %w", summary.Version, err)
		}
		if err := book.Install(prepared, false); err != nil {
			return nil, err
		}
	}
	if state.ActiveVersion != "" {
		if err := book.Activate(state.ActiveVersion); err != nil {
			return nil, err
		}
	}
	return book, nil
}

func (service *Service) Quote(sku string, maximum Usage) (Quote, error) {
	if service == nil || service.book == nil {
		return Quote{}, errors.New("runtime price book is not attached")
	}
	service.operations.RLock()
	defer service.operations.RUnlock()
	return service.book.Quote(sku, maximum)
}

func (service *Service) Charge(catalogVersion, sku string, actual Usage) (Charge, error) {
	if service == nil || service.book == nil {
		return Charge{}, errors.New("runtime price book is not attached")
	}
	service.operations.RLock()
	defer service.operations.RUnlock()
	return service.book.Charge(catalogVersion, sku, actual)
}

func (service *Service) prepare(input Catalog) (Catalog, error) {
	catalog, err := PrepareOfficialCatalog(input)
	if err != nil {
		return Catalog{}, fmt.Errorf("%w: %v", ErrInvalidCatalog, err)
	}
	now := service.now().UTC().Add(5 * time.Minute)
	if catalog.FetchedAt.After(now) || catalog.VerifiedAt.After(now) {
		return Catalog{}, fmt.Errorf("%w: catalog source timestamps cannot be in the future", ErrInvalidCatalog)
	}
	if !catalog.FXVerifiedAt.IsZero() && catalog.FXVerifiedAt.After(now) {
		return Catalog{}, fmt.Errorf("%w: FX verification timestamp cannot be in the future", ErrInvalidCatalog)
	}
	for _, entry := range catalog.Entries {
		if entry.VerifiedAt.After(now) {
			return Catalog{}, fmt.Errorf("%w: entry %q verification timestamp cannot be in the future", ErrInvalidCatalog, entry.SKU)
		}
	}
	return catalog, nil
}
