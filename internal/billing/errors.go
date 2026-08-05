package billing

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidCatalog    = errors.New("invalid price catalog")
	ErrInvalidUsage      = errors.New("invalid token usage")
	ErrModelNotFound     = errors.New("model not found in price catalog")
	ErrModelUnpriced     = errors.New("model is unpriced")
	ErrCategoryUnpriced  = errors.New("usage category is unpriced")
	ErrOutsidePriceRange = errors.New("token usage is outside priced range")
	ErrAmountOverflow    = errors.New("micro-USD amount overflows int64")
)

type PricingError struct {
	Kind     error
	Model    string
	Category string
	Reason   string
}

func (e *PricingError) Error() string {
	message := e.Kind.Error()
	if e.Model != "" {
		message += fmt.Sprintf(" for %q", e.Model)
	}
	if e.Category != "" {
		message += fmt.Sprintf(" (%s)", e.Category)
	}
	if e.Reason != "" {
		message += ": " + e.Reason
	}
	return message
}

func (e *PricingError) Unwrap() error {
	return e.Kind
}
