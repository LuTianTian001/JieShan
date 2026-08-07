package siteadmin

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

func (registry *Registry) Register(adapter Adapter) error {
	if registry == nil {
		return errors.New("site administration registry is unavailable")
	}
	if nilAdapter(adapter) {
		return errors.New("site administration adapter is required")
	}
	if err := ValidateAdapter(adapter); err != nil {
		return err
	}
	kind := strings.ToLower(strings.TrimSpace(adapter.Kind()))
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.adapters[kind]; exists {
		return fmt.Errorf("site administration adapter %q is already registered", kind)
	}
	registry.adapters[kind] = adapter
	return nil
}

func (registry *Registry) Lookup(kind string) (Adapter, error) {
	if registry == nil {
		return nil, errors.New("site administration registry is unavailable")
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	registry.mu.RLock()
	adapter, exists := registry.adapters[kind]
	registry.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("site administration adapter %q is not registered", kind)
	}
	return adapter, nil
}

func nilAdapter(adapter Adapter) bool {
	if adapter == nil {
		return true
	}
	value := reflect.ValueOf(adapter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
