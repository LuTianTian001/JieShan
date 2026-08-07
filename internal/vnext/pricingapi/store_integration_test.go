package pricingapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestPricingAPIRequiresPreviewedImportAndExplicitCASActivation(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "pricing-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	handler, err := NewStoreHandler(storage, pricing.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	catalog := apiOfficialCatalog(now)

	previewResponse := pricingRequest(t, handler, http.MethodPost, apiPrefix+"/catalogs/preview", previewRequest{Catalog: catalog}, "")
	if previewResponse.Code != http.StatusOK || previewResponse.Header().Get("ETag") != `"0"` {
		t.Fatalf("preview status=%d body=%s headers=%v", previewResponse.Code, previewResponse.Body.String(), previewResponse.Header())
	}
	var preview pricing.Preview
	decodeResponse(t, previewResponse, &preview)
	if preview.Candidate.Digest == "" || preview.Diff.Summary.AddedEntries != 1 || !preview.CanActivate {
		t.Fatalf("preview = %+v", preview)
	}

	badDigest := pricingRequest(t, handler, http.MethodPost, apiPrefix+"/catalogs", importRequest{
		Catalog: catalog, ExpectedDigest: apiDigest("f"),
	}, "")
	if badDigest.Code != http.StatusConflict {
		t.Fatalf("bad digest status=%d body=%s", badDigest.Code, badDigest.Body.String())
	}
	importResponse := pricingRequest(t, handler, http.MethodPost, apiPrefix+"/catalogs", importRequest{
		Catalog: catalog, ExpectedDigest: preview.Candidate.Digest,
	}, "")
	if importResponse.Code != http.StatusCreated || importResponse.Header().Get("ETag") != `"0"` {
		t.Fatalf("import status=%d body=%s headers=%v", importResponse.Code, importResponse.Body.String(), importResponse.Header())
	}
	var imported pricing.ImportResult
	decodeResponse(t, importResponse, &imported)
	if !imported.Imported || imported.State.ActiveVersion != "" {
		t.Fatalf("import = %+v", imported)
	}

	missingPrecondition := pricingRequest(t, handler, http.MethodPost, apiPrefix+"/catalogs/api-prices-1/activate", nil, "")
	if missingPrecondition.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing precondition status=%d body=%s", missingPrecondition.Code, missingPrecondition.Body.String())
	}
	activation := pricingRequest(t, handler, http.MethodPost, apiPrefix+"/catalogs/api-prices-1/activate", nil, `"0"`)
	if activation.Code != http.StatusOK || activation.Header().Get("ETag") != `"1"` {
		t.Fatalf("activation status=%d body=%s headers=%v", activation.Code, activation.Body.String(), activation.Header())
	}
	var activated stateResponse
	decodeResponse(t, activation, &activated)
	if activated.State.ActiveVersion != "api-prices-1" || activated.State.Revision != 1 {
		t.Fatalf("activated state = %+v", activated.State)
	}

	list := pricingRequest(t, handler, http.MethodGet, apiPrefix+"/catalogs", nil, "")
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var listed listResponse
	decodeResponse(t, list, &listed)
	if len(listed.Items) != 1 || !listed.Items[0].Active || listed.State.Revision != 1 {
		t.Fatalf("list = %+v", listed)
	}
	get := pricingRequest(t, handler, http.MethodGet, apiPrefix+"/catalogs/api-prices-1", nil, "")
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), preview.Candidate.Digest) {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}
}

func TestPricingAPIEnsuresBundledCatalogWithoutNetworkOrOperatorOverwrite(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "pricing-builtin-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	handler, err := NewStoreHandler(storage)
	if err != nil {
		t.Fatal(err)
	}

	response := pricingRequest(t, handler, http.MethodPost, apiPrefix+"/builtin/ensure", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("ensure status=%d body=%s", response.Code, response.Body.String())
	}
	var result pricing.BuiltinBootstrapResult
	decodeResponse(t, response, &result)
	if result.Outcome != pricing.BuiltinOutcomeInstalled || !result.Imported || !result.Activated {
		t.Fatalf("ensure result = %+v", result)
	}
	if result.State.ActiveVersion != pricing.BuiltinOfficialCatalogVersion || response.Header().Get("ETag") == "" {
		t.Fatalf("ensure state=%+v headers=%v", result.State, response.Header())
	}
	repeated := pricingRequest(t, handler, http.MethodPost, apiPrefix+"/builtin/ensure", nil, "")
	var repeatedResult pricing.BuiltinBootstrapResult
	decodeResponse(t, repeated, &repeatedResult)
	if repeated.Code != http.StatusOK || repeatedResult.Outcome != pricing.BuiltinOutcomeAlreadyCurrent || repeatedResult.Imported || repeatedResult.Activated {
		t.Fatalf("repeated ensure status=%d result=%+v", repeated.Code, repeatedResult)
	}

	wrongMethod := pricingRequest(t, handler, http.MethodGet, apiPrefix+"/builtin/ensure", nil, "")
	if wrongMethod.Code != http.StatusMethodNotAllowed || wrongMethod.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("wrong method status=%d body=%s headers=%v", wrongMethod.Code, wrongMethod.Body.String(), wrongMethod.Header())
	}
}

func TestPricingAPIRejectsUnverifiedOrStructurallyUnknownCatalogs(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "invalid-pricing-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	handler, err := NewStoreHandler(storage, pricing.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	unverified := apiOfficialCatalog(now)
	unverified.Entries[0].VerificationStatus = "estimated"
	response := pricingRequest(t, handler, http.MethodPost, apiPrefix+"/catalogs/preview", previewRequest{Catalog: unverified}, "")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_catalog") {
		t.Fatalf("unverified status=%d body=%s", response.Code, response.Body.String())
	}

	unknownField := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, apiPrefix+"/catalogs/preview", strings.NewReader(`{"catalog":{},"guess_price":true}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(unknownField, request)
	if unknownField.Code != http.StatusBadRequest || !strings.Contains(unknownField.Body.String(), "unknown field") {
		t.Fatalf("unknown field status=%d body=%s", unknownField.Code, unknownField.Body.String())
	}
}

func apiOfficialCatalog(now time.Time) pricing.Catalog {
	return pricing.Catalog{
		Version: "api-prices-1", Source: "operator-verified official pages", SourceDigest: apiDigest("a"),
		FetchedAt: now, VerifiedAt: now, EffectiveAt: now,
		Entries: []pricing.Entry{{
			SKU: "model-a", Provider: "provider-a", ModelPattern: "model-a",
			SourceURL: "https://provider.example/pricing", NativeCurrency: "USD", USDPerNativeUnit: "1",
			Rates: []pricing.Rate{
				{Class: pricing.TokenInput, NativePricePerMillion: "2"},
				{Class: pricing.TokenOutput, NativePricePerMillion: "6"},
			},
		}},
	}
}

func apiDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func pricingRequest(t *testing.T, handler http.Handler, method, path string, body any, ifMatch string) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
}
