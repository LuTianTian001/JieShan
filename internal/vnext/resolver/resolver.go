package resolver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/downstreamkeys"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

var (
	ErrInvalidKey         = errors.New("invalid downstream API key")
	ErrModelNotFound      = errors.New("model is not enabled by the effective routing profile")
	ErrNoRoutableTargets  = errors.New("model has no structurally routable targets")
	ErrUnsupportedIngress = errors.New("downstream protocol surface is not supported")
	ErrUnavailable        = errors.New("route resolution is unavailable")
)

// Repository is the only data boundary used by VNext resolution. Each method
// reads canonical VNext tables and has no legacy compatibility fallback.
type Repository interface {
	LoadResolverRoute(context.Context, int64, string) (vnextstore.ResolverRouteSnapshot, error)
	ListResolverRoutes(context.Context, int64) ([]vnextstore.ResolverRouteSnapshot, error)
	LoadResolverTargetHealth(context.Context, []int64) (map[int64]routing.HealthState, error)
}

// KeyAuthenticator is injected independently from route storage so digest
// comparison and enabled/expiry/quota policy remain owned by one service.
type KeyAuthenticator interface {
	Authenticate(context.Context, string) (vnextstore.DownstreamKey, error)
}

type Resolver struct {
	repository    Repository
	authenticator KeyAuthenticator
	capabilities  protocol.CapabilityLookup
}

func New(repository Repository, authenticator KeyAuthenticator, capabilities protocol.CapabilityLookup) (*Resolver, error) {
	if repository == nil || authenticator == nil || capabilities == nil {
		return nil, errors.New("resolver repository, key authenticator, and capability lookup are required")
	}
	return &Resolver{repository: repository, authenticator: authenticator, capabilities: capabilities}, nil
}

// EndpointMetadata is immutable request-construction metadata for one
// physical provider target. CredentialIDs preserve the endpoint binding order
// and contain no site-wide or inferred credentials.
type EndpointMetadata struct {
	TargetID                     routing.TargetID
	PublishedModelTargetID       int64
	PublishedModelTargetRevision int64
	SiteID                       int64
	SiteName                     string
	EndpointID                   int64
	EndpointName                 string
	BaseURL                      string
	Protocol                     protocol.Protocol
	Surface                      protocol.Surface
	AuthScheme                   protocol.AuthScheme
	AdapterKind                  string
	SourceModel                  string
	HeaderTemplate               json.RawMessage
	SecretHeadersConfigured      bool
	SecretHeadersCipherVersion   int64
	CredentialIDs                []routing.CredentialID
	CredentialNames              map[routing.CredentialID]string
}

type Resolution struct {
	DownstreamKeyID        int64
	PublishedModelID       int64
	PublishedModelRevision int64
	RoutingProfileID       int64
	RoutingProfileName     string
	SourceProfileID        int64
	SourceProfileName      string
	RouteRevision          int64
	PublicModel            string
	OfficialPriceSKU       string
	Plan                   routing.Plan
	Endpoints              map[routing.TargetID]EndpointMetadata
	Health                 map[routing.TargetID]routing.HealthState
}

func (resolution Resolution) NewCursor(now time.Time) *routing.Cursor {
	return resolution.Plan.NewCursor(resolution.Health, now)
}

type Model struct {
	ID               string
	OfficialPriceSKU string
}

// Resolve authenticates the downstream key, loads the effective model route
// from its bound profile, compiles persisted target/credential positions, and
// then loads health separately for Cursor eligibility. Health never mutates
// the compiled plan.
func (resolver *Resolver) Resolve(
	ctx context.Context,
	rawKey, publicModel string,
	ingressProtocol protocol.Protocol,
	ingressSurface protocol.Surface,
) (Resolution, error) {
	if err := protocol.ValidatePair(ingressProtocol, ingressSurface); err != nil {
		return Resolution{}, fmt.Errorf("%w: %v", ErrUnsupportedIngress, err)
	}
	key, err := resolver.authenticate(ctx, rawKey)
	if err != nil {
		return Resolution{}, err
	}
	publicModel = strings.TrimSpace(publicModel)
	if publicModel == "" {
		return Resolution{}, ErrModelNotFound
	}
	route, err := resolver.repository.LoadResolverRoute(ctx, key.RoutingProfileID, publicModel)
	if errors.Is(err, sql.ErrNoRows) {
		return Resolution{}, ErrModelNotFound
	}
	if err != nil {
		return Resolution{}, fmt.Errorf("%w: load model route", ErrUnavailable)
	}
	plan, endpoints, targetIDs, err := resolver.compile(route, ingressProtocol, ingressSurface)
	if err != nil {
		return Resolution{}, err
	}
	healthByID, err := resolver.repository.LoadResolverTargetHealth(ctx, targetIDs)
	if err != nil {
		return Resolution{}, fmt.Errorf("%w: load target health", ErrUnavailable)
	}
	health := make(map[routing.TargetID]routing.HealthState, len(healthByID))
	for targetID, state := range healthByID {
		health[routing.TargetID(targetID)] = state
	}
	return Resolution{
		DownstreamKeyID: key.ID, PublishedModelID: route.PublishedModelID,
		PublishedModelRevision: route.PublishedModelRevision,
		RoutingProfileID:       route.RoutingProfileID, RoutingProfileName: route.RoutingProfileName,
		SourceProfileID: route.SourceProfileID, SourceProfileName: route.SourceProfileName,
		RouteRevision: route.RouteRevision, PublicModel: route.PublicModel,
		OfficialPriceSKU: route.OfficialPriceSKU, Plan: plan, Endpoints: endpoints, Health: health,
	}, nil
}

// ListModels reports enabled effective routes with at least one structurally
// usable target. It deliberately does not read target_health, so cooldowns and
// transient health observations cannot make /v1/models flicker.
func (resolver *Resolver) ListModels(ctx context.Context, rawKey string, ingressProtocol protocol.Protocol) ([]Model, error) {
	if _, err := protocol.ParseProtocol(string(ingressProtocol)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedIngress, err)
	}
	key, err := resolver.authenticate(ctx, rawKey)
	if err != nil {
		return nil, err
	}
	routes, err := resolver.repository.ListResolverRoutes(ctx, key.RoutingProfileID)
	if err != nil {
		return nil, fmt.Errorf("%w: list model routes", ErrUnavailable)
	}
	models := make([]Model, 0, len(routes))
	for _, route := range routes {
		if resolver.hasStructurallyUsableTarget(route, ingressProtocol) {
			models = append(models, Model{ID: route.PublicModel, OfficialPriceSKU: route.OfficialPriceSKU})
		}
	}
	return models, nil
}

func (resolver *Resolver) authenticate(ctx context.Context, rawKey string) (vnextstore.DownstreamKey, error) {
	item, err := resolver.authenticator.Authenticate(ctx, rawKey)
	if errors.Is(err, downstreamkeys.ErrInvalidKey) {
		return vnextstore.DownstreamKey{}, ErrInvalidKey
	}
	if err != nil {
		return vnextstore.DownstreamKey{}, fmt.Errorf("%w: authenticate downstream key", ErrUnavailable)
	}
	return item, nil
}

// compile retains persisted target positions while excluding incompatible
// protocol surfaces. A Chat Completions request can therefore never be sent to
// Responses, Anthropic, or Gemini merely because it shares a public model.
func (resolver *Resolver) compile(
	route vnextstore.ResolverRouteSnapshot,
	ingressProtocol protocol.Protocol,
	ingressSurface protocol.Surface,
) (
	routing.Plan,
	map[routing.TargetID]EndpointMetadata,
	[]int64,
	error,
) {
	targets := make([]routing.Target, 0, len(route.Targets))
	endpoints := make(map[routing.TargetID]EndpointMetadata, len(route.Targets))
	targetIDs := make([]int64, 0, len(route.Targets))
	for _, stored := range route.Targets {
		metadata, credentials, ok := resolver.structuralTarget(stored)
		if !ok || metadata.Protocol != ingressProtocol || metadata.Surface != ingressSurface {
			continue
		}
		targetID := routing.TargetID(stored.ProviderModelTargetID)
		targets = append(targets, routing.Target{
			ID:          targetID,
			Revision:    routing.Revision(stored.TargetRevision),
			Position:    stored.Position,
			Enabled:     true,
			Credentials: credentials,
		})
		endpoints[targetID] = metadata
		targetIDs = append(targetIDs, stored.ProviderModelTargetID)
	}
	if len(targets) == 0 {
		return routing.Plan{}, nil, nil, ErrNoRoutableTargets
	}
	plan, err := routing.CompilePlan(targets)
	if err != nil {
		return routing.Plan{}, nil, nil, fmt.Errorf("%w: compile effective route: %v", ErrUnavailable, err)
	}
	return plan, endpoints, targetIDs, nil
}

func (resolver *Resolver) hasStructurallyUsableTarget(route vnextstore.ResolverRouteSnapshot, ingressProtocol protocol.Protocol) bool {
	for _, target := range route.Targets {
		if metadata, _, ok := resolver.structuralTarget(target); ok && metadata.Protocol == ingressProtocol {
			return true
		}
	}
	return false
}

func (resolver *Resolver) structuralTarget(stored vnextstore.ResolverRouteTarget) (EndpointMetadata, []routing.Credential, bool) {
	wireProtocol, err := protocol.ParseProtocol(stored.WireProtocol)
	if err != nil {
		return EndpointMetadata{}, nil, false
	}
	surface, err := protocol.ParseSurface(stored.Surface)
	if err != nil || protocol.ValidatePair(wireProtocol, surface) != nil {
		return EndpointMetadata{}, nil, false
	}
	contract, err := resolver.capabilities.Lookup(wireProtocol, surface)
	if err != nil || contract.Protocol != wireProtocol || contract.Surface != surface || !contract.Routable() {
		return EndpointMetadata{}, nil, false
	}
	authScheme, err := protocol.ParseAuthScheme(stored.AuthScheme)
	if err != nil || !validBaseURL(stored.BaseURL) || strings.TrimSpace(stored.SourceModel) == "" {
		return EndpointMetadata{}, nil, false
	}
	credentials := make([]routing.Credential, 0, len(stored.Credentials))
	credentialIDs := make([]routing.CredentialID, 0, len(stored.Credentials))
	credentialNames := make(map[routing.CredentialID]string, len(stored.Credentials))
	for _, storedCredential := range stored.Credentials {
		if storedCredential.ID <= 0 || storedCredential.Position < 0 {
			continue
		}
		state, cooldownUntil, ok := runtimeCredentialState(storedCredential)
		if !ok {
			continue
		}
		credentialID := routing.CredentialID(storedCredential.ID)
		credentials = append(credentials, routing.Credential{
			ID: credentialID, Position: storedCredential.Position, Enabled: true,
			State: state, CooldownUntil: cooldownUntil,
		})
		credentialIDs = append(credentialIDs, credentialID)
		credentialNames[credentialID] = strings.TrimSpace(storedCredential.Name)
	}
	if len(credentials) == 0 {
		return EndpointMetadata{}, nil, false
	}
	targetID := routing.TargetID(stored.ProviderModelTargetID)
	return EndpointMetadata{
		TargetID:                     targetID,
		PublishedModelTargetID:       stored.PublishedModelTargetID,
		PublishedModelTargetRevision: stored.PublishedModelTargetRevision,
		SiteID:                       stored.SiteID,
		SiteName:                     stored.SiteName,
		EndpointID:                   stored.EndpointID,
		EndpointName:                 stored.EndpointName,
		BaseURL:                      strings.TrimRight(strings.TrimSpace(stored.BaseURL), "/"),
		Protocol:                     wireProtocol,
		Surface:                      surface,
		AuthScheme:                   authScheme,
		AdapterKind:                  stored.AdapterKind,
		SourceModel:                  strings.TrimSpace(stored.SourceModel),
		HeaderTemplate:               append(json.RawMessage(nil), stored.HeaderTemplate...),
		SecretHeadersConfigured:      stored.SecretHeadersConfigured,
		SecretHeadersCipherVersion:   stored.SecretHeadersCipherVersion,
		CredentialIDs:                credentialIDs,
		CredentialNames:              credentialNames,
	}, credentials, true
}

func runtimeCredentialState(stored vnextstore.ResolverCredential) (routing.CredentialState, time.Time, bool) {
	switch strings.ToLower(strings.TrimSpace(stored.RuntimeState)) {
	case "active":
		return routing.CredentialReady, time.Time{}, true
	case "invalid":
		return routing.CredentialInvalid, time.Time{}, true
	case "exhausted":
		return routing.CredentialExhausted, time.Time{}, true
	case "cooling":
		if stored.CoolingUntil == nil || *stored.CoolingUntil <= 0 {
			return "", time.Time{}, false
		}
		return routing.CredentialCooling, time.UnixMilli(*stored.CoolingUntil).UTC(), true
	default:
		return "", time.Time{}, false
	}
}

func validBaseURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" &&
		(parsed.Scheme == "http" || parsed.Scheme == "https")
}
