package protocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync"
)

type DiscoveryInput struct {
	BaseURL string
	Auth    AuthInput
}

type DiscoveryResult struct {
	Models   []string
	Complete bool
}

type EncodedRequest struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

type ResponseInput struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

type DecodedResponse struct {
	Model string
	Body  []byte
}

type StreamInput struct {
	StatusCode int
	Header     http.Header
	Body       io.Reader
}

type StreamEvent struct {
	Body     []byte
	Semantic bool
	Terminal bool
}

type StreamResult struct {
	Model    string
	Terminal bool
}

type UsageInput struct {
	Body   []byte
	Events []StreamEvent
}

type Usage struct {
	InputTokens        *int64
	OutputTokens       *int64
	CacheReadTokens    *int64
	CacheWriteTokens   *int64
	CacheWrite5MTokens *int64
	CacheWrite1HTokens *int64
	ReasoningTokens    *int64
}

type ErrorInput struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

type DecodedError struct {
	Code              string
	Message           string
	Class             string
	Retryable         bool
	CredentialFailure bool
}

type Discoverer interface {
	DiscoverModels(context.Context, DiscoveryInput) (DiscoveryResult, error)
}

type RequestEncoder interface {
	EncodeRequest(context.Context, RequestBuildInput) (EncodedRequest, error)
}

type ResponseDecoder interface {
	DecodeResponse(context.Context, ResponseInput) (DecodedResponse, error)
}

type StreamDecoder interface {
	DecodeStream(context.Context, StreamInput, func(StreamEvent) error) (StreamResult, error)
}

type UsageExtractor interface {
	ExtractUsage(context.Context, UsageInput) (Usage, error)
}

type ErrorDecoder interface {
	DecodeError(context.Context, ErrorInput) (DecodedError, error)
}

// AdapterComponents is derived through interface assertions against one
// concrete adapter value. This keeps every capability tied to executable code.
type AdapterComponents struct {
	Discoverer      Discoverer
	RequestEncoder  RequestEncoder
	ResponseDecoder ResponseDecoder
	StreamDecoder   StreamDecoder
	UsageExtractor  UsageExtractor
	ErrorDecoder    ErrorDecoder
}

func ComponentsOf(adapter any) AdapterComponents {
	if nilLike(adapter) {
		return AdapterComponents{}
	}
	components := AdapterComponents{}
	components.Discoverer, _ = adapter.(Discoverer)
	components.RequestEncoder, _ = adapter.(RequestEncoder)
	components.ResponseDecoder, _ = adapter.(ResponseDecoder)
	components.StreamDecoder, _ = adapter.(StreamDecoder)
	components.UsageExtractor, _ = adapter.(UsageExtractor)
	components.ErrorDecoder, _ = adapter.(ErrorDecoder)
	return components
}

func (components AdapterComponents) Capabilities() Capabilities {
	return Capabilities{
		Discovery: !nilLike(components.Discoverer),
		Request:   !nilLike(components.RequestEncoder),
		Response:  !nilLike(components.ResponseDecoder),
		Stream:    !nilLike(components.StreamDecoder),
		Usage:     !nilLike(components.UsageExtractor),
		Error:     !nilLike(components.ErrorDecoder),
	}
}

func nilLike(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

type surfaceKey struct {
	protocol Protocol
	surface  Surface
}

// Registry is the sole source of runtime capability truth. Known surface names
// remain non-routable until a concrete adapter is registered for that exact
// protocol/surface pair.
type Registry struct {
	mu       sync.RWMutex
	adapters map[surfaceKey]AdapterComponents
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[surfaceKey]AdapterComponents)}
}

func (registry *Registry) Register(protocol Protocol, surface Surface, adapter any) (Contract, error) {
	known, err := lookupKnown(protocol, surface)
	if err != nil {
		return Contract{}, err
	}
	components := ComponentsOf(adapter)
	capabilities := components.Capabilities()
	if len(capabilities.MissingRouteStages()) == 6 {
		return Contract{}, errors.New("adapter implements no protocol capability interfaces")
	}
	if registry == nil {
		return Contract{}, errors.New("protocol registry is unavailable")
	}
	key := surfaceKey{protocol: protocol, surface: surface}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.adapters == nil {
		registry.adapters = make(map[surfaceKey]AdapterComponents)
	}
	if _, exists := registry.adapters[key]; exists {
		return Contract{}, fmt.Errorf("adapter already registered for %q/%q", protocol, surface)
	}
	registry.adapters[key] = components
	known.Capabilities = capabilities
	return known, nil
}

func (registry *Registry) Lookup(protocol Protocol, surface Surface) (Contract, error) {
	known, err := lookupKnown(protocol, surface)
	if err != nil {
		return Contract{}, err
	}
	if registry == nil {
		return known, nil
	}
	key := surfaceKey{protocol: protocol, surface: surface}
	registry.mu.RLock()
	components, exists := registry.adapters[key]
	registry.mu.RUnlock()
	if exists {
		known.Capabilities = components.Capabilities()
	}
	return known, nil
}

func (registry *Registry) Components(protocol Protocol, surface Surface) (AdapterComponents, error) {
	if _, err := lookupKnown(protocol, surface); err != nil {
		return AdapterComponents{}, err
	}
	if registry == nil {
		return AdapterComponents{}, errors.New("protocol registry is unavailable")
	}
	key := surfaceKey{protocol: protocol, surface: surface}
	registry.mu.RLock()
	components, exists := registry.adapters[key]
	registry.mu.RUnlock()
	if !exists {
		return AdapterComponents{}, fmt.Errorf("no adapter registered for %q/%q", protocol, surface)
	}
	return components, nil
}

func (registry *Registry) Contracts() []Contract {
	contracts := Catalog()
	if registry == nil {
		return contracts
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for index := range contracts {
		key := surfaceKey{protocol: contracts[index].Protocol, surface: contracts[index].Surface}
		if components, exists := registry.adapters[key]; exists {
			contracts[index].Capabilities = components.Capabilities()
		}
	}
	return contracts
}
