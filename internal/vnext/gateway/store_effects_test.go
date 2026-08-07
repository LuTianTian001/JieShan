package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestStoreCredentialEffectsKeepGlobalAndModelScopedFailuresSeparate(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, t.TempDir()+"/vnext.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	siteID, err := storage.CreateSite(ctx, vnextstore.SiteWrite{Name: "Effects", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := storage.CreateSiteEndpoint(ctx, siteID, vnextstore.SiteEndpointWrite{
		Name: "Chat", BaseURL: "https://effects.example/v1",
		WireProtocol: string(protocol.OpenAI), Surface: string(protocol.OpenAIChatCompletions),
		AuthScheme: string(protocol.AuthBearer), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	credentialIDs := make([]int64, 4)
	for index := range credentialIDs {
		credentialIDs[index], err = storage.CreateSiteCredential(ctx, siteID, vnextstore.SiteCredentialWrite{
			Name: "Key " + string(rune('A'+index)), SecretCipher: []byte{byte(index + 1)}, CipherVersion: 1, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	endpoint, err := storage.GetSiteEndpoint(ctx, endpointID)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.ReplaceEndpointCredentialBindings(ctx, siteID, endpointID, endpoint.Revision, credentialIDs); err != nil {
		t.Fatal(err)
	}
	targetID, err := storage.CreateProviderModelTarget(ctx, vnextstore.ProviderModelTargetWrite{
		SiteID: siteID, EndpointID: endpointID, SourceModel: "gpt-effects", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	effects, err := NewStoreCredentialEffects(storage, 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	base := CredentialEffectEvent{
		RequestID: "req-effects", SiteID: siteID, EndpointID: endpointID,
		TargetID: routing.TargetID(targetID), OccurredAt: now,
	}

	invalid := base
	invalid.CredentialID = routing.CredentialID(credentialIDs[0])
	invalid.Effect = routing.CredentialEffectInvalidate
	invalid.HTTPStatus = 401
	invalid.ErrorCode = "invalid_api_key"
	if err := effects.ApplyCredentialEffect(ctx, invalid); err != nil {
		t.Fatal(err)
	}
	invalidState, err := storage.GetCredentialRuntimeState(ctx, credentialIDs[0])
	if err != nil || invalidState.State != "invalid" || invalidState.LastHTTPStatus == nil || *invalidState.LastHTTPStatus != 401 {
		t.Fatalf("invalid state = %+v, %v", invalidState, err)
	}
	revision := invalidState.Revision
	if err := effects.ApplyCredentialEffect(ctx, invalid); err != nil {
		t.Fatal(err)
	}
	invalidState, _ = storage.GetCredentialRuntimeState(ctx, credentialIDs[0])
	if invalidState.Revision != revision {
		t.Fatalf("idempotent invalidation revision = %d, want %d", invalidState.Revision, revision)
	}

	denied := base
	denied.CredentialID = routing.CredentialID(credentialIDs[1])
	denied.Effect = routing.CredentialEffectDenyTargetAccess
	denied.HTTPStatus = 403
	denied.ErrorCode = "permission_denied"
	if err := effects.ApplyCredentialEffect(ctx, denied); err != nil {
		t.Fatal(err)
	}
	activeState, err := storage.GetCredentialRuntimeState(ctx, credentialIDs[1])
	if err != nil || activeState.State != "active" {
		t.Fatalf("403 changed global state = %+v, %v", activeState, err)
	}
	var availability string
	if err := storage.DB.QueryRowContext(ctx, `SELECT availability FROM credential_target_access
WHERE credential_id=? AND provider_model_target_id=?`, credentialIDs[1], targetID).Scan(&availability); err != nil {
		t.Fatal(err)
	}
	if availability != "forbidden" {
		t.Fatalf("target access = %q", availability)
	}

	cooling := base
	cooling.CredentialID = routing.CredentialID(credentialIDs[2])
	cooling.Effect = routing.CredentialEffectCooldown
	cooling.HTTPStatus = 429
	cooling.ErrorCode = "rate_limit"
	if err := effects.ApplyCredentialEffect(ctx, cooling); err != nil {
		t.Fatal(err)
	}
	coolingState, err := storage.GetCredentialRuntimeState(ctx, credentialIDs[2])
	wantUntil := now.Add(90 * time.Second).UnixMilli()
	if err != nil || coolingState.State != "cooling" || coolingState.CoolingUntil == nil || *coolingState.CoolingUntil != wantUntil {
		t.Fatalf("cooling state = %+v, %v", coolingState, err)
	}

	exhausted := base
	exhausted.CredentialID = routing.CredentialID(credentialIDs[3])
	exhausted.Effect = routing.CredentialEffectExhaust
	exhausted.HTTPStatus = 402
	exhausted.ErrorCode = "insufficient_balance"
	if err := effects.ApplyCredentialEffect(ctx, exhausted); err != nil {
		t.Fatal(err)
	}
	exhaustedState, err := storage.GetCredentialRuntimeState(ctx, credentialIDs[3])
	if err != nil || exhaustedState.State != "exhausted" {
		t.Fatalf("exhausted state = %+v, %v", exhaustedState, err)
	}
}
