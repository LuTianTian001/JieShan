package pricing

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Quote struct {
	CatalogVersion     string `json:"catalog_version"`
	SKU                string `json:"sku"`
	ReservationNanoUSD int64  `json:"reservation_nano_usd"`
}

// Book keeps immutable catalog versions in memory. Activating a version never
// changes the rates used by an already-started request.
type Book struct {
	mu       sync.RWMutex
	current  string
	catalogs map[string]catalogSnapshot
}

type catalogSnapshot struct {
	catalog       Catalog
	contentDigest string
	entries       map[string]Entry
}

func NewEmptyBook() *Book {
	return &Book{catalogs: make(map[string]catalogSnapshot)}
}

func NewBook(initial Catalog) (*Book, error) {
	book := NewEmptyBook()
	if err := book.Install(initial, true); err != nil {
		return nil, err
	}
	return book, nil
}

// NewBookFromCatalogs restores every immutable version so in-flight and
// historical requests can settle after a process restart.
func NewBookFromCatalogs(catalogs []Catalog, activeVersion string) (*Book, error) {
	book := NewEmptyBook()
	for _, catalog := range catalogs {
		if err := book.Install(catalog, false); err != nil {
			return nil, err
		}
	}
	if err := book.Activate(activeVersion); err != nil {
		return nil, err
	}
	return book, nil
}

func (book *Book) Install(catalog Catalog, makeCurrent bool) error {
	if book == nil {
		return errors.New("price book is unavailable")
	}
	if err := catalog.Validate(); err != nil {
		return err
	}
	snapshotCatalog := normalizeCatalog(catalog)
	contentDigest, err := CatalogDigest(snapshotCatalog)
	if err != nil {
		return err
	}
	entries := make(map[string]Entry, len(snapshotCatalog.Entries))
	for _, entry := range snapshotCatalog.Entries {
		entries[normalizeSKU(entry.SKU)] = entry
	}
	book.mu.Lock()
	defer book.mu.Unlock()
	if book.catalogs == nil {
		book.catalogs = make(map[string]catalogSnapshot)
	}
	if existing, exists := book.catalogs[snapshotCatalog.Version]; exists {
		if existing.contentDigest != contentDigest {
			return ErrCatalogVersionConflict
		}
		if makeCurrent {
			book.current = snapshotCatalog.Version
		}
		return nil
	}
	book.catalogs[snapshotCatalog.Version] = catalogSnapshot{
		catalog:       snapshotCatalog,
		contentDigest: contentDigest,
		entries:       entries,
	}
	if makeCurrent {
		book.current = snapshotCatalog.Version
	}
	return nil
}

func (book *Book) Activate(version string) error {
	if book == nil {
		return errors.New("price book is unavailable")
	}
	version = strings.TrimSpace(version)
	book.mu.Lock()
	defer book.mu.Unlock()
	if _, exists := book.catalogs[version]; !exists || version == "" {
		return fmt.Errorf("%w: %s", ErrCatalogNotFound, version)
	}
	book.current = version
	return nil
}

func (book *Book) CurrentVersion() string {
	if book == nil {
		return ""
	}
	book.mu.RLock()
	defer book.mu.RUnlock()
	return book.current
}

func (book *Book) Catalogs() []Catalog {
	if book == nil {
		return nil
	}
	book.mu.RLock()
	result := make([]Catalog, 0, len(book.catalogs))
	for _, snapshot := range book.catalogs {
		result = append(result, cloneCatalog(snapshot.catalog))
	}
	book.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Version < result[j].Version })
	return result
}

func (book *Book) Quote(sku string, maximum Usage) (Quote, error) {
	if book == nil {
		return Quote{}, errors.New("price book is unavailable")
	}
	book.mu.RLock()
	version := book.current
	snapshot, exists := book.catalogs[version]
	book.mu.RUnlock()
	if !exists || version == "" {
		return Quote{}, ErrNoActiveCatalog
	}
	entry, exists := snapshot.entries[normalizeSKU(sku)]
	if !exists {
		return Quote{}, fmt.Errorf("%w: SKU %q is not present in catalog %q", ErrPriceUnavailable, sku, version)
	}
	if entry.VerificationStatus != "" && entry.VerificationStatus != VerificationStatusVerified {
		return Quote{}, fmt.Errorf("%w: SKU %q is not verified", ErrPriceUnavailable, entry.SKU)
	}
	charge, err := CalculateCharge(version, entry, maximum)
	if err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrPriceUnavailable, err)
	}
	return Quote{CatalogVersion: version, SKU: entry.SKU, ReservationNanoUSD: charge.NanoUSD}, nil
}

func (book *Book) Charge(catalogVersion, sku string, actual Usage) (Charge, error) {
	if book == nil {
		return Charge{}, errors.New("price book is unavailable")
	}
	book.mu.RLock()
	snapshot, exists := book.catalogs[strings.TrimSpace(catalogVersion)]
	book.mu.RUnlock()
	if !exists {
		return Charge{}, fmt.Errorf("%w: version %q", ErrCatalogNotFound, catalogVersion)
	}
	entry, exists := snapshot.entries[normalizeSKU(sku)]
	if !exists {
		return Charge{}, fmt.Errorf("%w: SKU %q is not present in frozen catalog %q", ErrPriceUnavailable, sku, catalogVersion)
	}
	if entry.VerificationStatus != "" && entry.VerificationStatus != VerificationStatusVerified {
		return Charge{}, fmt.Errorf("%w: SKU %q is not verified", ErrPriceUnavailable, entry.SKU)
	}
	charge, err := CalculateCharge(snapshot.catalog.Version, entry, actual)
	if err != nil {
		return Charge{}, fmt.Errorf("%w: %v", ErrPriceUnavailable, err)
	}
	return charge, nil
}

func cloneCatalog(catalog Catalog) Catalog {
	copy := catalog
	copy.Entries = make([]Entry, len(catalog.Entries))
	for index, entry := range catalog.Entries {
		copy.Entries[index] = entry
		copy.Entries[index].Rates = append([]Rate(nil), entry.Rates...)
		if entry.LongContext != nil {
			tier := *entry.LongContext
			tier.Rates = append([]Rate(nil), entry.LongContext.Rates...)
			copy.Entries[index].LongContext = &tier
		}
	}
	return copy
}

func normalizeSKU(sku string) string {
	return strings.ToLower(strings.TrimSpace(sku))
}
