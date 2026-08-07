package pricing

import "errors"

var (
	ErrCatalogNotFound        = errors.New("official price catalog not found")
	ErrCatalogVersionConflict = errors.New("official price catalog version conflicts with immutable data")
	ErrCatalogStateConflict   = errors.New("official price catalog state revision conflict")
	ErrNoActiveCatalog        = errors.New("active official price catalog is unavailable")
	ErrPriceUnavailable       = errors.New("verified official price is unavailable")
	ErrCatalogNotEffective    = errors.New("official price catalog is not effective yet")
	ErrDigestConfirmation     = errors.New("catalog digest confirmation failed")
	ErrInvalidCatalog         = errors.New("official price catalog is invalid")
)
