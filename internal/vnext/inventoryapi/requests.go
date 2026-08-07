package inventoryapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

type optional[T any] struct {
	Set   bool
	Null  bool
	Value T
}

func (value *optional[T]) UnmarshalJSON(data []byte) error {
	value.Set = true
	if string(data) == "null" {
		value.Null = true
		return nil
	}
	return json.Unmarshal(data, &value.Value)
}

type createSiteRequest struct {
	Name         optional[string] `json:"name"`
	DashboardURL optional[string] `json:"dashboardUrl"`
	Enabled      optional[bool]   `json:"enabled"`
	MaxInFlight  optional[int]    `json:"maxConcurrency"`
}

func (body createSiteRequest) validate() (vnextstore.SiteWrite, error) {
	name, err := requiredName(body.Name, "name")
	if err != nil {
		return vnextstore.SiteWrite{}, err
	}
	dashboardURL := ""
	if body.DashboardURL.Set && !body.DashboardURL.Null {
		dashboardURL = strings.TrimRight(strings.TrimSpace(body.DashboardURL.Value), "/")
		if err := validateOptionalHTTPURL(dashboardURL, "dashboardUrl"); err != nil {
			return vnextstore.SiteWrite{}, err
		}
	}
	enabled, err := defaultBool(body.Enabled, true, "enabled")
	if err != nil {
		return vnextstore.SiteWrite{}, err
	}
	maxInFlight := vnextstore.DefaultSiteMaxInFlight
	if body.MaxInFlight.Set {
		if body.MaxInFlight.Null || body.MaxInFlight.Value <= 0 {
			return vnextstore.SiteWrite{}, errors.New("maxConcurrency must be a positive integer")
		}
		maxInFlight = body.MaxInFlight.Value
	}
	return vnextstore.SiteWrite{Name: name, DashboardURL: dashboardURL, Enabled: enabled, MaxInFlight: maxInFlight}, nil
}

type updateSiteRequest struct {
	Name         optional[string] `json:"name"`
	DashboardURL optional[string] `json:"dashboardUrl"`
	Enabled      optional[bool]   `json:"enabled"`
	MaxInFlight  optional[int]    `json:"maxConcurrency"`
}

func (body updateSiteRequest) apply(current vnextstore.Site, revision int64) (vnextstore.SiteUpdate, error) {
	if !body.Name.Set && !body.DashboardURL.Set && !body.Enabled.Set && !body.MaxInFlight.Set {
		return vnextstore.SiteUpdate{}, errors.New("at least one mutable field is required")
	}
	result := vnextstore.SiteUpdate{
		ExpectedRevision: revision,
		Name:             current.Name,
		DashboardURL:     current.DashboardURL,
		Enabled:          current.Enabled,
		MaxInFlight:      current.MaxInFlight,
	}
	if body.Name.Set {
		name, err := requiredName(body.Name, "name")
		if err != nil {
			return vnextstore.SiteUpdate{}, err
		}
		result.Name = name
	}
	if body.DashboardURL.Set {
		if body.DashboardURL.Null {
			result.DashboardURL = ""
		} else {
			result.DashboardURL = strings.TrimRight(strings.TrimSpace(body.DashboardURL.Value), "/")
			if err := validateOptionalHTTPURL(result.DashboardURL, "dashboardUrl"); err != nil {
				return vnextstore.SiteUpdate{}, err
			}
		}
	}
	if body.Enabled.Set {
		value, err := requiredBool(body.Enabled, "enabled")
		if err != nil {
			return vnextstore.SiteUpdate{}, err
		}
		result.Enabled = value
	}
	if body.MaxInFlight.Set {
		if body.MaxInFlight.Null || body.MaxInFlight.Value <= 0 {
			return vnextstore.SiteUpdate{}, errors.New("maxConcurrency must be a positive integer")
		}
		result.MaxInFlight = body.MaxInFlight.Value
	}
	return result, nil
}

type createEndpointRequest struct {
	Name         optional[string]          `json:"name"`
	BaseURL      optional[string]          `json:"baseUrl"`
	WireProtocol optional[string]          `json:"wireProtocol"`
	Surface      optional[string]          `json:"surface"`
	AdapterKind  optional[string]          `json:"adapterKind"`
	AuthScheme   optional[string]          `json:"authScheme"`
	Headers      optional[json.RawMessage] `json:"headers"`
	Enabled      optional[bool]            `json:"enabled"`
}

func (body createEndpointRequest) validate() (vnextstore.SiteEndpointWrite, error) {
	name, err := requiredName(body.Name, "name")
	if err != nil {
		return vnextstore.SiteEndpointWrite{}, err
	}
	baseURL, err := requiredString(body.BaseURL, "baseUrl", 2048)
	if err != nil {
		return vnextstore.SiteEndpointWrite{}, err
	}
	wireProtocol, err := requiredString(body.WireProtocol, "wireProtocol", 64)
	if err != nil {
		return vnextstore.SiteEndpointWrite{}, err
	}
	surface, err := requiredString(body.Surface, "surface", 128)
	if err != nil {
		return vnextstore.SiteEndpointWrite{}, err
	}
	adapterKind := "generic"
	if body.AdapterKind.Set {
		adapterKind, err = requiredString(body.AdapterKind, "adapterKind", 64)
		if err != nil {
			return vnextstore.SiteEndpointWrite{}, err
		}
	}
	authScheme := ""
	if body.AuthScheme.Set {
		authScheme, err = requiredString(body.AuthScheme, "authScheme", 64)
		if err != nil {
			return vnextstore.SiteEndpointWrite{}, err
		}
	}
	headers, err := requestHeaders(body.Headers)
	if err != nil {
		return vnextstore.SiteEndpointWrite{}, err
	}
	enabled, err := defaultBool(body.Enabled, true, "enabled")
	if err != nil {
		return vnextstore.SiteEndpointWrite{}, err
	}
	return validateEndpointSnapshot(vnextstore.SiteEndpointWrite{
		Name: name, BaseURL: baseURL, WireProtocol: wireProtocol, Surface: surface,
		AdapterKind: adapterKind, AuthScheme: authScheme, HeaderTemplate: headers, Enabled: enabled,
	})
}

type updateEndpointRequest struct {
	Name         optional[string]          `json:"name"`
	BaseURL      optional[string]          `json:"baseUrl"`
	WireProtocol optional[string]          `json:"wireProtocol"`
	Surface      optional[string]          `json:"surface"`
	AdapterKind  optional[string]          `json:"adapterKind"`
	AuthScheme   optional[string]          `json:"authScheme"`
	Headers      optional[json.RawMessage] `json:"headers"`
	Enabled      optional[bool]            `json:"enabled"`
}

func (body updateEndpointRequest) apply(current vnextstore.SiteEndpoint, revision int64) (vnextstore.SiteEndpointUpdate, error) {
	if !body.Name.Set && !body.BaseURL.Set && !body.WireProtocol.Set && !body.Surface.Set && !body.AdapterKind.Set &&
		!body.AuthScheme.Set && !body.Headers.Set && !body.Enabled.Set {
		return vnextstore.SiteEndpointUpdate{}, errors.New("at least one mutable field is required")
	}
	snapshot := vnextstore.SiteEndpointWrite{
		Name: current.Name, BaseURL: current.BaseURL, WireProtocol: current.WireProtocol, Surface: current.Surface,
		AdapterKind: current.AdapterKind, AuthScheme: current.AuthScheme, HeaderTemplate: append([]byte(nil), current.HeaderTemplate...),
		Enabled: current.Enabled,
	}
	var err error
	if body.Name.Set {
		snapshot.Name, err = requiredName(body.Name, "name")
	}
	if err == nil && body.BaseURL.Set {
		snapshot.BaseURL, err = requiredString(body.BaseURL, "baseUrl", 2048)
	}
	if err == nil && body.WireProtocol.Set {
		snapshot.WireProtocol, err = requiredString(body.WireProtocol, "wireProtocol", 64)
	}
	if err == nil && body.Surface.Set {
		snapshot.Surface, err = requiredString(body.Surface, "surface", 128)
	}
	if err == nil && body.AdapterKind.Set {
		snapshot.AdapterKind, err = requiredString(body.AdapterKind, "adapterKind", 64)
	}
	if err == nil && body.AuthScheme.Set {
		snapshot.AuthScheme, err = requiredString(body.AuthScheme, "authScheme", 64)
	}
	if err == nil && !body.AuthScheme.Set && (body.WireProtocol.Set || body.Surface.Set) {
		snapshot.AuthScheme = ""
	}
	if err == nil && body.Headers.Set {
		snapshot.HeaderTemplate, err = requestHeaders(body.Headers)
	}
	if err == nil && body.Enabled.Set {
		snapshot.Enabled, err = requiredBool(body.Enabled, "enabled")
	}
	if err != nil {
		return vnextstore.SiteEndpointUpdate{}, err
	}
	normalized, err := validateEndpointSnapshot(snapshot)
	if err != nil {
		return vnextstore.SiteEndpointUpdate{}, err
	}
	return vnextstore.SiteEndpointUpdate{
		ExpectedRevision: revision,
		Name:             normalized.Name,
		BaseURL:          normalized.BaseURL,
		WireProtocol:     normalized.WireProtocol,
		Surface:          normalized.Surface,
		AdapterKind:      normalized.AdapterKind,
		AuthScheme:       normalized.AuthScheme,
		HeaderTemplate:   append([]byte(nil), normalized.HeaderTemplate...),
		Enabled:          normalized.Enabled,
	}, nil
}

type credentialRequest struct {
	Name    optional[string] `json:"name"`
	Secret  optional[string] `json:"secret"`
	Enabled optional[bool]   `json:"enabled"`
}

func (body credentialRequest) create() (CredentialCreate, error) {
	name, err := requiredName(body.Name, "name")
	if err != nil {
		return CredentialCreate{}, err
	}
	secret, err := requiredSecret(body.Secret)
	if err != nil {
		return CredentialCreate{}, err
	}
	enabled, err := defaultBool(body.Enabled, true, "enabled")
	if err != nil {
		clear(secret)
		return CredentialCreate{}, err
	}
	return CredentialCreate{Name: name, Secret: secret, Enabled: enabled}, nil
}

type updateCredentialRequest struct {
	Name    optional[string] `json:"name"`
	Enabled optional[bool]   `json:"enabled"`
}

func (body updateCredentialRequest) apply(current vnextstore.SiteCredential, revision int64) (vnextstore.SiteCredentialUpdate, error) {
	if !body.Name.Set && !body.Enabled.Set {
		return vnextstore.SiteCredentialUpdate{}, errors.New("at least one mutable field is required")
	}
	result := vnextstore.SiteCredentialUpdate{ExpectedRevision: revision, Name: current.Name, Enabled: current.Enabled}
	var err error
	if body.Name.Set {
		result.Name, err = requiredName(body.Name, "name")
	}
	if err == nil && body.Enabled.Set {
		result.Enabled, err = requiredBool(body.Enabled, "enabled")
	}
	return result, err
}

type replaceSecretRequest struct {
	Secret optional[string] `json:"secret"`
}

func (body replaceSecretRequest) validate(revision int64) (CredentialSecretUpdate, error) {
	secret, err := requiredSecret(body.Secret)
	if err != nil {
		return CredentialSecretUpdate{}, err
	}
	return CredentialSecretUpdate{ExpectedRevision: revision, Secret: secret}, nil
}

type createProviderModelRequest struct {
	SourceModel optional[string] `json:"sourceModel"`
	DisplayName optional[string] `json:"displayName"`
	Enabled     optional[bool]   `json:"enabled"`
}

func (body createProviderModelRequest) validate(siteID, endpointID int64) (vnextstore.ProviderModelTargetWrite, error) {
	sourceModel, err := requiredString(body.SourceModel, "sourceModel", 512)
	if err != nil {
		return vnextstore.ProviderModelTargetWrite{}, err
	}
	displayName := ""
	if body.DisplayName.Set && !body.DisplayName.Null {
		displayName = strings.TrimSpace(body.DisplayName.Value)
		if len(displayName) > 120 {
			return vnextstore.ProviderModelTargetWrite{}, errors.New("displayName must not exceed 120 characters")
		}
	}
	enabled, err := defaultBool(body.Enabled, true, "enabled")
	if err != nil {
		return vnextstore.ProviderModelTargetWrite{}, err
	}
	return vnextstore.ProviderModelTargetWrite{
		SiteID: siteID, EndpointID: endpointID, SourceModel: sourceModel, DisplayName: displayName, Enabled: enabled,
	}, nil
}

type updateProviderModelRequest struct {
	SourceModel optional[string] `json:"sourceModel"`
	DisplayName optional[string] `json:"displayName"`
	Enabled     optional[bool]   `json:"enabled"`
}

func (body updateProviderModelRequest) apply(current vnextstore.ProviderModelTarget, revision int64) (vnextstore.ProviderModelTargetUpdate, error) {
	if !body.SourceModel.Set && !body.DisplayName.Set && !body.Enabled.Set {
		return vnextstore.ProviderModelTargetUpdate{}, errors.New("at least one mutable field is required")
	}
	result := vnextstore.ProviderModelTargetUpdate{
		ExpectedRevision: revision, SourceModel: current.SourceModel, DisplayName: current.DisplayName, Enabled: current.Enabled,
	}
	var err error
	if body.SourceModel.Set {
		result.SourceModel, err = requiredString(body.SourceModel, "sourceModel", 512)
	}
	if err == nil && body.DisplayName.Set {
		if body.DisplayName.Null {
			result.DisplayName = ""
		} else {
			result.DisplayName = strings.TrimSpace(body.DisplayName.Value)
			if len(result.DisplayName) > 120 {
				err = errors.New("displayName must not exceed 120 characters")
			}
		}
	}
	if err == nil && body.Enabled.Set {
		result.Enabled, err = requiredBool(body.Enabled, "enabled")
	}
	return result, err
}

type discoverModelsRequest struct {
	CredentialID optional[int64] `json:"credentialId"`
}

func (body discoverModelsRequest) validate() (int64, error) {
	if !body.CredentialID.Set || body.CredentialID.Null || body.CredentialID.Value <= 0 {
		return 0, errors.New("credentialId must be a positive integer")
	}
	return body.CredentialID.Value, nil
}

type previewModelsRequest struct {
	BaseURL      optional[string] `json:"baseUrl"`
	WireProtocol optional[string] `json:"wireProtocol"`
	Surface      optional[string] `json:"surface"`
	AuthScheme   optional[string] `json:"authScheme"`
	APIKey       optional[string] `json:"apiKey"`
}

func (body previewModelsRequest) validate() (ModelDiscoveryPreviewInput, error) {
	baseURL, err := requiredString(body.BaseURL, "baseUrl", 2048)
	if err != nil {
		return ModelDiscoveryPreviewInput{}, err
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if err := validateOptionalHTTPURL(baseURL, "baseUrl"); err != nil {
		return ModelDiscoveryPreviewInput{}, err
	}
	parsedURL, _ := url.Parse(baseURL)
	if parsedURL.RawQuery != "" {
		return ModelDiscoveryPreviewInput{}, errors.New("baseUrl must not contain a query string")
	}
	protocolName, err := requiredString(body.WireProtocol, "wireProtocol", 64)
	if err != nil {
		return ModelDiscoveryPreviewInput{}, err
	}
	protocolID, err := vnextprotocol.ParseProtocol(protocolName)
	if err != nil {
		return ModelDiscoveryPreviewInput{}, err
	}
	surface, err := previewSurface(protocolID, body.Surface)
	if err != nil {
		return ModelDiscoveryPreviewInput{}, err
	}
	authName, err := requiredString(body.AuthScheme, "authScheme", 64)
	if err != nil {
		return ModelDiscoveryPreviewInput{}, err
	}
	authScheme, err := vnextprotocol.ParseAuthScheme(authName)
	if err != nil {
		return ModelDiscoveryPreviewInput{}, err
	}
	if err := validatePreviewAuthScheme(protocolID, authScheme); err != nil {
		return ModelDiscoveryPreviewInput{}, err
	}
	secret, err := requiredPreviewAPIKey(body.APIKey)
	if err != nil {
		return ModelDiscoveryPreviewInput{}, err
	}
	return ModelDiscoveryPreviewInput{
		BaseURL: baseURL, Protocol: protocolID, Surface: surface, AuthScheme: authScheme, Secret: secret,
	}, nil
}

func validatePreviewAuthScheme(protocolID vnextprotocol.Protocol, scheme vnextprotocol.AuthScheme) error {
	switch protocolID {
	case vnextprotocol.OpenAI:
		return nil
	case vnextprotocol.Anthropic:
		if scheme == vnextprotocol.AuthXAPIKey {
			return nil
		}
		return errors.New("Anthropic model discovery requires the x-api-key auth scheme")
	case vnextprotocol.Gemini:
		if scheme == vnextprotocol.AuthXGoogAPIKey {
			return nil
		}
		return errors.New("Gemini model discovery requires the x-goog-api-key auth scheme")
	default:
		return fmt.Errorf("unsupported inference protocol %q", protocolID)
	}
}

func previewSurface(protocolID vnextprotocol.Protocol, value optional[string]) (vnextprotocol.Surface, error) {
	if value.Set {
		name, err := requiredString(value, "surface", 128)
		if err != nil {
			return "", err
		}
		surface, err := vnextprotocol.ParseSurface(name)
		if err != nil {
			return "", err
		}
		if err := vnextprotocol.ValidatePair(protocolID, surface); err != nil {
			return "", err
		}
		return surface, nil
	}
	switch protocolID {
	case vnextprotocol.OpenAI:
		return vnextprotocol.OpenAIChatCompletions, nil
	case vnextprotocol.Anthropic:
		return vnextprotocol.AnthropicMessages, nil
	case vnextprotocol.Gemini:
		return vnextprotocol.GeminiGenerateContent, nil
	default:
		return "", fmt.Errorf("unsupported inference protocol %q", protocolID)
	}
}

func requiredPreviewAPIKey(value optional[string]) ([]byte, error) {
	if !value.Set || value.Null || strings.TrimSpace(value.Value) == "" {
		return nil, errors.New("apiKey is required")
	}
	if len(value.Value) > 32<<10 {
		return nil, errors.New("apiKey exceeds the safety limit")
	}
	return []byte(strings.TrimSpace(value.Value)), nil
}

type importModelsRequest struct {
	CredentialID optional[int64] `json:"credentialId"`
	Models       []string        `json:"models"`
}

func (body importModelsRequest) validate() (int64, []string, error) {
	if !body.CredentialID.Set || body.CredentialID.Null || body.CredentialID.Value <= 0 {
		return 0, nil, errors.New("credentialId must be a positive integer")
	}
	models, err := validateModelNames(body.Models)
	return body.CredentialID.Value, models, err
}

type replaceIDsRequest struct {
	IDs []int64 `json:"ids"`
}

func (body replaceIDsRequest) validate(label string, allowEmpty bool) ([]int64, error) {
	if len(body.IDs) == 0 && !allowEmpty {
		return nil, fmt.Errorf("%s must contain at least one item", label)
	}
	seen := make(map[int64]struct{}, len(body.IDs))
	result := make([]int64, 0, len(body.IDs))
	for _, id := range body.IDs {
		if id <= 0 {
			return nil, fmt.Errorf("%s must contain only positive integers", label)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("%s must not contain duplicates", label)
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

type replaceCredentialBindingsRequest struct {
	CredentialIDs []int64 `json:"credentialIds"`
}

func requiredName(value optional[string], field string) (string, error) {
	return requiredString(value, field, 120)
}

func requiredString(value optional[string], field string, limit int) (string, error) {
	if !value.Set || value.Null {
		return "", fmt.Errorf("%s is required", field)
	}
	result := strings.TrimSpace(value.Value)
	if result == "" {
		return "", fmt.Errorf("%s must be a non-empty string", field)
	}
	if len(result) > limit {
		return "", fmt.Errorf("%s must not exceed %d characters", field, limit)
	}
	return result, nil
}

func requiredSecret(value optional[string]) ([]byte, error) {
	if !value.Set || value.Null || strings.TrimSpace(value.Value) == "" {
		return nil, errors.New("secret is required")
	}
	if len(value.Value) > 32<<10 {
		return nil, errors.New("secret exceeds the safety limit")
	}
	return []byte(strings.TrimSpace(value.Value)), nil
}

func defaultBool(value optional[bool], fallback bool, field string) (bool, error) {
	if !value.Set {
		return fallback, nil
	}
	return requiredBool(value, field)
}

func requiredBool(value optional[bool], field string) (bool, error) {
	if value.Null {
		return false, fmt.Errorf("%s must be a boolean", field)
	}
	return value.Value, nil
}

func validateOptionalHTTPURL(value, field string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL without credentials or fragment", field)
	}
	return nil
}

func validateEndpointSnapshot(input vnextstore.SiteEndpointWrite) (vnextstore.SiteEndpointWrite, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.AdapterKind = strings.ToLower(strings.TrimSpace(input.AdapterKind))
	if err := validateOptionalHTTPURL(input.BaseURL, "baseUrl"); err != nil {
		return vnextstore.SiteEndpointWrite{}, err
	}
	parsedURL, _ := url.Parse(input.BaseURL)
	if parsedURL.RawQuery != "" {
		return vnextstore.SiteEndpointWrite{}, errors.New("baseUrl must not contain a query string")
	}
	protocolID, err := vnextprotocol.ParseProtocol(input.WireProtocol)
	if err != nil {
		return vnextstore.SiteEndpointWrite{}, err
	}
	surface, err := vnextprotocol.ParseSurface(input.Surface)
	if err != nil {
		return vnextstore.SiteEndpointWrite{}, err
	}
	if err := vnextprotocol.ValidatePair(protocolID, surface); err != nil {
		return vnextstore.SiteEndpointWrite{}, err
	}
	input.WireProtocol = string(protocolID)
	input.Surface = string(surface)
	if input.AuthScheme == "" {
		scheme, err := vnextprotocol.DefaultAuthScheme(protocolID)
		if err != nil {
			return vnextstore.SiteEndpointWrite{}, err
		}
		input.AuthScheme = string(scheme)
	}
	authScheme, err := vnextprotocol.ParseAuthScheme(input.AuthScheme)
	if err != nil {
		return vnextstore.SiteEndpointWrite{}, err
	}
	input.AuthScheme = string(authScheme)
	if input.AdapterKind == "" {
		input.AdapterKind = "generic"
	}
	if len(input.HeaderTemplate) == 0 {
		input.HeaderTemplate = json.RawMessage(`{}`)
	}
	return input, nil
}

func requestHeaders(value optional[json.RawMessage]) (json.RawMessage, error) {
	if !value.Set || value.Null {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value.Value, &object); err != nil || object == nil || len(object) > 64 {
		return nil, errors.New("headers must be a JSON object with at most 64 fields")
	}
	for name := range object {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key", "x-goog-api-key":
			return nil, fmt.Errorf("sensitive header %q belongs in credential configuration", name)
		}
	}
	compact, err := json.Marshal(object)
	if err != nil {
		return nil, errors.New("headers are invalid")
	}
	return compact, nil
}

func validateModelNames(models []string) ([]string, error) {
	if len(models) == 0 || len(models) > 5000 {
		return nil, errors.New("models must contain between 1 and 5000 items")
	}
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, raw := range models {
		model := strings.TrimSpace(raw)
		if model == "" || len(model) > 512 {
			return nil, errors.New("models must contain only non-empty names of at most 512 characters")
		}
		if _, duplicate := seen[model]; duplicate {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	return result, nil
}
