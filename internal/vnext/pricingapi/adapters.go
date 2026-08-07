package pricingapi

import (
	"errors"

	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

// NewStoreHandler exposes the independent pricing administration surface. The
// runtime must mount it behind the same administrator authentication as other
// control-plane APIs.
func NewStoreHandler(store *vnextstore.Store, options ...pricing.ServiceOption) (*Handler, error) {
	if store == nil {
		return nil, errors.New("VNext store is required")
	}
	service, err := pricing.NewService(store, options...)
	if err != nil {
		return nil, err
	}
	return New(service)
}
