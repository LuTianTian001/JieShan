package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/downstreamkeys"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

var allRouteCapabilities = protocol.Capabilities{
	Discovery: true,
	Request:   true,
	Response:  true,
	Stream:    true,
	Usage:     true,
	Error:     true,
}

type capabilityLookup map[string]protocol.Capabilities

func (lookup capabilityLookup) Lookup(wireProtocol protocol.Protocol, surface protocol.Surface) (protocol.Contract, error) {
	if err := protocol.ValidatePair(wireProtocol, surface); err != nil {
		return protocol.Contract{}, err
	}
	return protocol.Contract{
		Protocol:     wireProtocol,
		Surface:      surface,
		Capabilities: lookup[string(wireProtocol)+"/"+string(surface)],
	}, nil
}

type fixture struct {
	t       *testing.T
	ctx     context.Context
	store   *vnextstore.Store
	keys    *downstreamkeys.Service
	resolve *Resolver
}

type testCipher struct{}

func (testCipher) Seal(_ secretbox.Purpose, _ secretbox.Identity, plaintext []byte) ([]byte, error) {
	return append([]byte("sealed:"), plaintext...), nil
}

func (testCipher) Open(_ secretbox.Purpose, _ secretbox.Identity, ciphertext []byte) ([]byte, error) {
	return append([]byte(nil), ciphertext...), nil
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "vnext.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	keyService, err := downstreamkeys.New(storage, testCipher{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := New(storage, keyService, capabilityLookup{
		"openai/openai.chat_completions": allRouteCapabilities,
		"openai/openai.responses":        allRouteCapabilities,
		"anthropic/anthropic.messages": {
			Discovery: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, ctx: ctx, store: storage, keys: keyService, resolve: resolver}
}

func (fixture *fixture) resolveChat(rawKey, publicModel string) (Resolution, error) {
	return fixture.resolve.Resolve(
		fixture.ctx,
		rawKey,
		publicModel,
		protocol.OpenAI,
		protocol.OpenAIChatCompletions,
	)
}

func (fixture *fixture) listOpenAIModels(rawKey string) ([]Model, error) {
	return fixture.resolve.ListModels(fixture.ctx, rawKey, protocol.OpenAI)
}

func (fixture *fixture) key(name string, enabled bool, quota, expiresAt *int64) (int64, string) {
	return fixture.keyInProfile(name, nil, enabled, quota, expiresAt)
}

func (fixture *fixture) keyInProfile(name string, profileID *int64, enabled bool, quota, expiresAt *int64) (int64, string) {
	fixture.t.Helper()
	issued, err := fixture.keys.Create(fixture.ctx, downstreamkeys.CreateInput{
		Name: name, RoutingProfileID: profileID, Enabled: enabled,
		QuotaNanoUSD: quota, ExpiresAt: expiresAt,
	})
	if err != nil {
		fixture.t.Fatal(err)
	}
	return issued.Key.ID, issued.RawSecret
}

func (fixture *fixture) profile(name string) int64 {
	fixture.t.Helper()
	profile, err := fixture.store.CreateRoutingProfile(fixture.ctx, name)
	if err != nil {
		fixture.t.Fatal(err)
	}
	return profile.ID
}

type targetSpec struct {
	siteID        int64
	endpointID    int64
	targetID      int64
	credentialIDs []int64
}

func (fixture *fixture) site(name string) int64 {
	fixture.t.Helper()
	id, err := fixture.store.CreateSite(fixture.ctx, vnextstore.SiteWrite{Name: name, Enabled: true})
	if err != nil {
		fixture.t.Fatal(err)
	}
	return id
}

func (fixture *fixture) target(siteID int64, endpointName, baseURL string, wireProtocol protocol.Protocol, surface protocol.Surface, sourceModel string, credentialCount int) targetSpec {
	fixture.t.Helper()
	endpointID, err := fixture.store.CreateSiteEndpoint(fixture.ctx, siteID, vnextstore.SiteEndpointWrite{
		Name:                       endpointName,
		BaseURL:                    baseURL,
		WireProtocol:               string(wireProtocol),
		Surface:                    string(surface),
		AuthScheme:                 string(protocol.AuthBearer),
		HeaderTemplate:             []byte(`{"X-Client":"JieShan"}`),
		SecretHeadersCipher:        []byte("top-secret-header:" + endpointName),
		SecretHeadersCipherVersion: 1,
		Enabled:                    true,
	})
	if err != nil {
		fixture.t.Fatal(err)
	}
	credentialIDs := make([]int64, 0, credentialCount)
	for index := 0; index < credentialCount; index++ {
		credentialID, err := fixture.store.CreateSiteCredential(fixture.ctx, siteID, vnextstore.SiteCredentialWrite{
			Name:          fmt.Sprintf("%s-key-%d", endpointName, index),
			SecretCipher:  []byte(fmt.Sprintf("cipher-%s-%d", endpointName, index)),
			CipherVersion: 1,
			Enabled:       true,
		})
		if err != nil {
			fixture.t.Fatal(err)
		}
		credentialIDs = append(credentialIDs, credentialID)
	}
	if credentialCount > 0 {
		endpoint, err := fixture.store.GetSiteEndpoint(fixture.ctx, endpointID)
		if err != nil {
			fixture.t.Fatal(err)
		}
		if err := fixture.store.ReplaceEndpointCredentialBindings(fixture.ctx, siteID, endpointID, endpoint.Revision, credentialIDs); err != nil {
			fixture.t.Fatal(err)
		}
	}
	targetID, err := fixture.store.CreateProviderModelTarget(fixture.ctx, vnextstore.ProviderModelTargetWrite{
		SiteID:      siteID,
		EndpointID:  endpointID,
		SourceModel: sourceModel,
		Enabled:     true,
	})
	if err != nil {
		fixture.t.Fatal(err)
	}
	return targetSpec{siteID: siteID, endpointID: endpointID, targetID: targetID, credentialIDs: credentialIDs}
}

func (fixture *fixture) route(keyID int64, publicModel string, targetIDs ...int64) int64 {
	fixture.t.Helper()
	key, err := fixture.store.GetDownstreamKey(fixture.ctx, keyID)
	if err != nil {
		fixture.t.Fatal(err)
	}
	if !key.UsesDefaultRoutingProfile {
		fixture.t.Fatal("route helper requires a key using the default profile")
	}
	model := fixture.publish(publicModel, targetIDs...)
	return model.ID
}

func (fixture *fixture) publish(publicModel string, targetIDs ...int64) vnextstore.PublishedModel {
	fixture.t.Helper()
	model, err := fixture.store.CreatePublishedModel(fixture.ctx, vnextstore.PublishedModelWrite{
		PublicName: publicModel, OfficialPriceSKU: publicModel, Enabled: true,
	}, targetIDs)
	if err != nil {
		fixture.t.Fatal(err)
	}
	return model
}

func (fixture *fixture) override(profileID int64, model vnextstore.PublishedModel, enabled bool, providerTargetIDs ...int64) {
	fixture.t.Helper()
	profile, err := fixture.store.GetRoutingProfile(fixture.ctx, profileID)
	if err != nil {
		fixture.t.Fatal(err)
	}
	targetIDs := make([]int64, 0, len(providerTargetIDs))
	for _, providerTargetID := range providerTargetIDs {
		found := false
		for _, target := range model.Targets {
			if target.ProviderModelTargetID == providerTargetID {
				targetIDs = append(targetIDs, target.ID)
				found = true
				break
			}
		}
		if !found {
			fixture.t.Fatalf("provider target %d is not published by model %q", providerTargetID, model.PublicName)
		}
	}
	if _, err := fixture.store.CreateRoutingProfileRoute(fixture.ctx, profileID, profile.Revision,
		vnextstore.RoutingProfileRouteWrite{PublishedModelID: model.ID, Enabled: enabled, TargetIDs: targetIDs}); err != nil {
		fixture.t.Fatal(err)
	}
}

func TestResolveUsesTheAuthenticatedKeysEffectiveOrderedProfileRoute(t *testing.T) {
	fixture := newFixture(t)
	profileOne := fixture.profile("Key One Profile")
	profileTwo := fixture.profile("Key Two Profile")
	keyOne, keyOneRaw := fixture.keyInProfile("Key One", &profileOne, true, nil, nil)
	_, keyTwoRaw := fixture.keyInProfile("Key Two", &profileTwo, true, nil, nil)

	sharedSite := fixture.site("Shared site")
	endpointOne := fixture.target(sharedSite, "chat-a", "https://shared.example/v1", protocol.OpenAI, protocol.OpenAIChatCompletions, "gpt-source-a", 1)
	endpointTwo := fixture.target(sharedSite, "chat-b", "https://shared.example/gateway", protocol.OpenAI, protocol.OpenAIChatCompletions, "gpt-source-b", 2)
	otherSite := fixture.site("Other site")
	endpointThree := fixture.target(otherSite, "chat-c", "https://other.example/v1", protocol.OpenAI, protocol.OpenAIChatCompletions, "gpt-source-c", 1)

	sharedModel := fixture.publish("gpt-public", endpointOne.targetID, endpointTwo.targetID, endpointThree.targetID)
	fixture.override(profileOne, sharedModel, true, endpointTwo.targetID, endpointThree.targetID, endpointOne.targetID)
	fixture.override(profileTwo, sharedModel, true, endpointOne.targetID)
	keyTwoOnly := fixture.publish("key-two-only", endpointThree.targetID)
	fixture.override(profileOne, keyTwoOnly, false)

	resolution, err := fixture.resolveChat(keyOneRaw, "gpt-public")
	if err != nil {
		t.Fatal(err)
	}
	targets := resolution.Plan.Targets()
	wantIDs := []routing.TargetID{
		routing.TargetID(endpointTwo.targetID),
		routing.TargetID(endpointThree.targetID),
		routing.TargetID(endpointOne.targetID),
	}
	if len(targets) != len(wantIDs) {
		t.Fatalf("target count = %d, want %d", len(targets), len(wantIDs))
	}
	if resolution.DownstreamKeyID != keyOne || resolution.RoutingProfileID != profileOne ||
		resolution.SourceProfileID != profileOne {
		t.Fatalf("effective profile snapshot = %+v", resolution)
	}
	for index, wantID := range wantIDs {
		if targets[index].ID != wantID || targets[index].Position != index {
			t.Fatalf("target[%d] = %+v, want ID %d position %d", index, targets[index], wantID, index)
		}
	}
	metadata := resolution.Endpoints[routing.TargetID(endpointTwo.targetID)]
	if metadata.EndpointID != endpointTwo.endpointID || metadata.BaseURL != "https://shared.example/gateway" ||
		metadata.Protocol != protocol.OpenAI || metadata.Surface != protocol.OpenAIChatCompletions ||
		metadata.SourceModel != "gpt-source-b" || !metadata.SecretHeadersConfigured ||
		metadata.SecretHeadersCipherVersion != 1 {
		t.Fatalf("endpoint metadata = %+v", metadata)
	}
	encodedMetadata, err := json.Marshal(resolution.Endpoints)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encodedMetadata, []byte("X-Client")) || bytes.Contains(encodedMetadata, []byte("top-secret-header")) {
		t.Fatalf("resolver metadata leaked or omitted headers: %s", encodedMetadata)
	}
	if len(metadata.CredentialIDs) != 2 || metadata.CredentialIDs[0] != routing.CredentialID(endpointTwo.credentialIDs[0]) ||
		metadata.CredentialIDs[1] != routing.CredentialID(endpointTwo.credentialIDs[1]) {
		t.Fatalf("explicit credential IDs = %v", metadata.CredentialIDs)
	}

	keyTwoResolution, err := fixture.resolveChat(keyTwoRaw, "gpt-public")
	if err != nil {
		t.Fatal(err)
	}
	keyTwoTargets := keyTwoResolution.Plan.Targets()
	if len(keyTwoTargets) != 1 || keyTwoTargets[0].ID != routing.TargetID(endpointOne.targetID) {
		t.Fatalf("key two route leaked key one targets: %+v", keyTwoTargets)
	}
	keyOneModels, err := fixture.listOpenAIModels(keyOneRaw)
	if err != nil || len(keyOneModels) != 1 || keyOneModels[0].ID != "gpt-public" {
		t.Fatalf("key one models leaked key two route: %+v, %v", keyOneModels, err)
	}
	if _, err := fixture.resolveChat(keyOneRaw, "key-two-only"); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("key one resolved key two model: %v", err)
	}
}

func TestResolveFiltersToTheExactIngressSurfaceWithoutReordering(t *testing.T) {
	fixture := newFixture(t)
	keyID, rawKey := fixture.key("Surface key", true, nil, nil)
	siteID := fixture.site("Mixed OpenAI surfaces")
	responses := fixture.target(
		siteID,
		"responses",
		"https://mixed.example/v1",
		protocol.OpenAI,
		protocol.OpenAIResponses,
		"gpt-responses",
		1,
	)
	chat := fixture.target(
		siteID,
		"chat",
		"https://mixed.example/openai",
		protocol.OpenAI,
		protocol.OpenAIChatCompletions,
		"gpt-chat",
		1,
	)
	fixture.route(keyID, "gpt-public", responses.targetID, chat.targetID)

	chatResolution, err := fixture.resolveChat(rawKey, "gpt-public")
	if err != nil {
		t.Fatal(err)
	}
	chatTargets := chatResolution.Plan.Targets()
	if len(chatTargets) != 1 || chatTargets[0].ID != routing.TargetID(chat.targetID) || chatTargets[0].Position != 1 {
		t.Fatalf("chat targets = %+v", chatTargets)
	}

	responsesResolution, err := fixture.resolve.Resolve(
		fixture.ctx,
		rawKey,
		"gpt-public",
		protocol.OpenAI,
		protocol.OpenAIResponses,
	)
	if err != nil {
		t.Fatal(err)
	}
	responsesTargets := responsesResolution.Plan.Targets()
	if len(responsesTargets) != 1 || responsesTargets[0].ID != routing.TargetID(responses.targetID) || responsesTargets[0].Position != 0 {
		t.Fatalf("responses targets = %+v", responsesTargets)
	}

	models, err := fixture.listOpenAIModels(rawKey)
	if err != nil || len(models) != 1 || models[0].ID != "gpt-public" {
		t.Fatalf("OpenAI models = %+v, %v", models, err)
	}
	geminiModels, err := fixture.resolve.ListModels(fixture.ctx, rawKey, protocol.Gemini)
	if err != nil || len(geminiModels) != 0 {
		t.Fatalf("Gemini models = %+v, %v", geminiModels, err)
	}
	if _, err := fixture.resolve.Resolve(
		fixture.ctx,
		rawKey,
		"gpt-public",
		protocol.OpenAI,
		protocol.AnthropicMessages,
	); !errors.Is(err, ErrUnsupportedIngress) {
		t.Fatalf("mismatched ingress error = %v", err)
	}
}

func TestListModelsHidesDiscoveryOnlyAndCredentiallessTargets(t *testing.T) {
	fixture := newFixture(t)
	keyID, rawKey := fixture.key("Visibility", true, nil, nil)

	anthropicSite := fixture.site("Anthropic discovery")
	discoveryOnly := fixture.target(anthropicSite, "messages", "https://anthropic.example", protocol.Anthropic, protocol.AnthropicMessages, "claude-source", 1)
	fixture.route(keyID, "claude-hidden", discoveryOnly.targetID)

	openAISite := fixture.site("Credentialless")
	credentialless := fixture.target(openAISite, "chat", "https://empty.example/v1", protocol.OpenAI, protocol.OpenAIChatCompletions, "gpt-empty", 0)
	fixture.route(keyID, "gpt-empty", credentialless.targetID)
	valid := fixture.target(openAISite, "valid", "https://valid.example/v1", protocol.OpenAI, protocol.OpenAIChatCompletions, "gpt-valid", 1)
	fixture.route(keyID, "mixed-visible", discoveryOnly.targetID, valid.targetID)

	models, err := fixture.listOpenAIModels(rawKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "mixed-visible" {
		t.Fatalf("structurally visible models = %+v", models)
	}
	if _, err := fixture.resolveChat(rawKey, "claude-hidden"); !errors.Is(err, ErrNoRoutableTargets) {
		t.Fatalf("discovery-only resolve error = %v", err)
	}
	if _, err := fixture.resolveChat(rawKey, "gpt-empty"); !errors.Is(err, ErrNoRoutableTargets) {
		t.Fatalf("credentialless resolve error = %v", err)
	}
	mixed, err := fixture.resolveChat(rawKey, "mixed-visible")
	if err != nil {
		t.Fatal(err)
	}
	mixedTargets := mixed.Plan.Targets()
	if len(mixedTargets) != 1 || mixedTargets[0].ID != routing.TargetID(valid.targetID) || mixedTargets[0].Position != 1 {
		t.Fatalf("structural filtering changed persisted position: %+v", mixedTargets)
	}
}

func TestCredentialTargetAccessFiltersOnlyDeniedBindings(t *testing.T) {
	fixture := newFixture(t)
	keyID, rawKey := fixture.key("Access", true, nil, nil)
	siteID := fixture.site("Access site")
	target := fixture.target(siteID, "chat", "https://access.example/v1", protocol.OpenAI, protocol.OpenAIChatCompletions, "gpt-access", 3)
	for index, availability := range []string{"forbidden", "unsupported", "supported"} {
		if err := fixture.store.UpsertCredentialTargetAccess(fixture.ctx, vnextstore.CredentialTargetAccessWrite{
			SiteID:                siteID,
			EndpointID:            target.endpointID,
			CredentialID:          target.credentialIDs[index],
			ProviderModelTargetID: target.targetID,
			Availability:          availability,
		}); err != nil {
			t.Fatal(err)
		}
	}
	fixture.route(keyID, "gpt-access", target.targetID)

	resolution, err := fixture.resolveChat(rawKey, "gpt-access")
	if err != nil {
		t.Fatal(err)
	}
	credentials := resolution.Plan.Targets()[0].Credentials
	if len(credentials) != 1 || credentials[0].ID != routing.CredentialID(target.credentialIDs[2]) {
		t.Fatalf("eligible credentials = %+v", credentials)
	}
}

func TestCredentialRuntimeStateControlsAttemptsWithoutHidingTheModel(t *testing.T) {
	fixture := newFixture(t)
	keyID, rawKey := fixture.key("Runtime state", true, nil, nil)
	siteID := fixture.site("Runtime state site")
	target := fixture.target(siteID, "chat", "https://runtime-state.example/v1", protocol.OpenAI, protocol.OpenAIChatCompletions, "gpt-runtime", 3)
	fixture.route(keyID, "gpt-runtime", target.targetID)
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	coolingUntil := now.Add(5 * time.Minute).UnixMilli()
	if _, err := fixture.store.DB.ExecContext(fixture.ctx, `UPDATE credential_runtime_state SET
state='invalid',last_http_status=401,last_error_code='invalid_api_key',revision=revision+1,updated_at=? WHERE credential_id=?`, now.UnixMilli(), target.credentialIDs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB.ExecContext(fixture.ctx, `UPDATE credential_runtime_state SET
state='exhausted',last_http_status=402,last_error_code='insufficient_balance',revision=revision+1,updated_at=? WHERE credential_id=?`, now.UnixMilli(), target.credentialIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB.ExecContext(fixture.ctx, `UPDATE credential_runtime_state SET
state='cooling',cooling_until=?,last_http_status=429,last_error_code='rate_limit',revision=revision+1,updated_at=? WHERE credential_id=?`, coolingUntil, now.UnixMilli(), target.credentialIDs[2]); err != nil {
		t.Fatal(err)
	}

	models, err := fixture.listOpenAIModels(rawKey)
	if err != nil || len(models) != 1 || models[0].ID != "gpt-runtime" {
		t.Fatalf("models = %+v, %v", models, err)
	}
	resolution, err := fixture.resolveChat(rawKey, "gpt-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if candidate, ok := resolution.NewCursor(now).First(); ok {
		t.Fatalf("cooling route unexpectedly selected %+v", candidate)
	}
	candidate, ok := resolution.NewCursor(now.Add(6 * time.Minute)).First()
	if !ok || candidate.Credential.ID != routing.CredentialID(target.credentialIDs[2]) {
		t.Fatalf("recovered credential = %+v, %v", candidate, ok)
	}
}

func TestCooldownAffectsCursorButNotPlanOrderOrModelVisibility(t *testing.T) {
	fixture := newFixture(t)
	keyID, rawKey := fixture.key("Cooldown", true, nil, nil)
	siteOne := fixture.site("Cooling site")
	first := fixture.target(siteOne, "first", "https://first.example/v1", protocol.OpenAI, protocol.OpenAIChatCompletions, "gpt-cool", 1)
	siteTwo := fixture.site("Healthy site")
	second := fixture.target(siteTwo, "second", "https://second.example/v1", protocol.OpenAI, protocol.OpenAIChatCompletions, "gpt-cool", 1)
	fixture.route(keyID, "gpt-cool", first.targetID, second.targetID)

	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	if _, err := fixture.store.DB.ExecContext(fixture.ctx, `INSERT INTO target_health(
provider_model_target_id,config_revision,state_version,phase,capability,consecutive_failures,
cooldown_until,last_event_sequence,half_open_sequence,created_at,updated_at)
VALUES (?,1,1,'open','unknown',2,?,2,0,?,?)`, first.targetID, now.Add(5*time.Minute).UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	models, err := fixture.listOpenAIModels(rawKey)
	if err != nil || len(models) != 1 || models[0].ID != "gpt-cool" {
		t.Fatalf("ListModels during cooldown = %+v, %v", models, err)
	}
	resolution, err := fixture.resolveChat(rawKey, "gpt-cool")
	if err != nil {
		t.Fatal(err)
	}
	targets := resolution.Plan.Targets()
	if len(targets) != 2 || targets[0].ID != routing.TargetID(first.targetID) || targets[1].ID != routing.TargetID(second.targetID) {
		t.Fatalf("cooldown reordered plan = %+v", targets)
	}
	candidate, ok := resolution.NewCursor(now).First()
	if !ok || candidate.Target.ID != routing.TargetID(second.targetID) {
		t.Fatalf("cursor first candidate = %+v, %v", candidate, ok)
	}
}

func TestAuthenticationRejectsWrongDisabledAndExpiredKeysButNotQuotaExhaustion(t *testing.T) {
	fixture := newFixture(t)
	past := time.Now().UTC().Add(-time.Minute).UnixMilli()
	quota := int64(100)
	_, disabledRaw := fixture.key("Disabled", false, nil, nil)
	_, expiredRaw := fixture.key("Expired", true, nil, &past)
	exhaustedID, exhaustedRaw := fixture.key("Exhausted", true, &quota, nil)
	if _, err := fixture.store.DB.ExecContext(fixture.ctx, `UPDATE downstream_keys SET used_nano_usd=60,reserved_nano_usd=40 WHERE id=?`, exhaustedID); err != nil {
		t.Fatal(err)
	}

	for _, rawKey := range []string{"js_wrong_secret", disabledRaw, expiredRaw} {
		if _, err := fixture.listOpenAIModels(rawKey); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("ListModels(%q) error = %v, want ErrInvalidKey", rawKey, err)
		}
		if _, err := fixture.resolveChat(rawKey, "anything"); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("Resolve(%q) error = %v, want ErrInvalidKey", rawKey, err)
		}
	}
	if key, err := fixture.keys.Authenticate(fixture.ctx, exhaustedRaw); err != nil || key.ID != exhaustedID {
		t.Fatalf("quota-exhausted key authentication = %+v, %v", key, err)
	}
	if _, err := fixture.listOpenAIModels(exhaustedRaw); err != nil {
		t.Fatalf("ListModels(quota-exhausted key) error = %v", err)
	}
}
