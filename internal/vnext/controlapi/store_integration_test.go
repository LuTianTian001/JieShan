package controlapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/vnext/downstreamkeys"
	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestStoreHandlerKeyLifecycleAndExplicitModelProjection(t *testing.T) {
	handler, store := newStoreHandlerFixture(t)
	ctx := context.Background()

	response := performRequest(handler, http.MethodPost, apiPrefix,
		`{"name":"Client","quotaNanoUSD":5000,"hourlyQuotaNanoUSD":1500,"billingMultiplierBPS":12500,"enabled":true}`, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	var created struct {
		Item   keyResponse `json:"item"`
		Secret string      `json:"secret"`
	}
	decodeResponse(t, response, &created)
	if created.Item.ID <= 0 || !strings.HasPrefix(created.Secret, "js_") || created.Item.KeyPrefix == created.Secret {
		t.Fatalf("created key response = %+v", created)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("create cache/ETag headers = %v", response.Header())
	}

	var storedDigest, storedCiphertext []byte
	if err := store.DB.QueryRowContext(ctx, `SELECT key_digest,encrypted_secret FROM downstream_keys WHERE id=?`, created.Item.ID).
		Scan(&storedDigest, &storedCiphertext); err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256([]byte(created.Secret))
	if !bytes.Equal(storedDigest, wantDigest[:]) || bytes.Contains(storedCiphertext, []byte(created.Secret)) {
		t.Fatal("created key was not stored as digest plus encrypted reveal copy")
	}

	targetA := mustCreateControlTarget(t, store, "Alpha", "https://alpha.example/v1", "gpt-5")
	targetB := mustCreateControlTarget(t, store, "Beta", "https://beta.example/v1", "gpt-5")
	model, err := store.CreatePublishedModel(ctx, vnextstore.PublishedModelWrite{
		PublicName: "gpt-5", OfficialPriceSKU: "gpt-5", Enabled: true,
	}, []int64{targetA, targetB})
	if err != nil {
		t.Fatal(err)
	}
	if model.ID <= 0 || len(model.Targets) != 2 {
		t.Fatalf("published model = %+v", model)
	}

	response = performRequest(handler, http.MethodGet, apiPrefix, "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), created.Secret) {
		t.Fatal("list returned the one-time raw key")
	}
	for _, forbidden := range []string{"keyDigest", "encryptedSecret", "allowedModels", `"rpm"`} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("list leaked legacy or secret field %q: %s", forbidden, response.Body.String())
		}
	}
	var listed struct {
		Items []keyResponse `json:"items"`
	}
	decodeResponse(t, response, &listed)
	if len(listed.Items) != 1 || !reflect.DeepEqual(listed.Items[0].Models, []string{"gpt-5"}) {
		t.Fatalf("listed keys = %+v", listed.Items)
	}

	response = performRequest(handler, http.MethodGet, apiPrefix+"/"+itoa(created.Item.ID), "", "")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), created.Secret) {
		t.Fatalf("get key status/body = %d/%s", response.Code, response.Body.String())
	}

	for _, body := range []string{
		`{"name":"Old allowlist","allowedModels":["gpt-5"]}`,
		`{"name":"Caller digest","keyDigest":"secret"}`,
		`{"name":"Caller cipher","encryptedSecret":"secret"}`,
	} {
		response = performRequest(handler, http.MethodPost, apiPrefix, body, "")
		assertError(t, response, http.StatusBadRequest, "invalid_request")
	}

	response = performRequest(handler, http.MethodPost, apiPrefix, `{"name":"Client"}`, "")
	assertError(t, response, http.StatusConflict, "conflict")
	if strings.Contains(response.Body.String(), "UNIQUE") || strings.Contains(response.Body.String(), created.Secret) {
		t.Fatalf("conflict leaked storage details: %s", response.Body.String())
	}

	response = performRequest(handler, http.MethodPatch, apiPrefix+"/"+itoa(created.Item.ID),
		`{"name":"Renamed","quotaNanoUSD":7000,"hourlyQuotaNanoUSD":2000,"billingMultiplierBPS":8000,"expires":null,"enabled":false}`, `"1"`)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("CAS update status/ETag = %d/%q, body = %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	var updated struct {
		Item keyResponse `json:"item"`
	}
	decodeResponse(t, response, &updated)
	if updated.Item.Name != "Renamed" || updated.Item.Enabled || updated.Item.BillingMultiplierBPS != 8_000 ||
		updated.Item.QuotaNanoUSD == nil || *updated.Item.QuotaNanoUSD != 7000 ||
		updated.Item.HourlyQuotaNanoUSD == nil || *updated.Item.HourlyQuotaNanoUSD != 2000 ||
		!reflect.DeepEqual(updated.Item.Models, []string{"gpt-5"}) {
		t.Fatalf("updated key = %+v", updated.Item)
	}
	var digestAfterUpdate []byte
	if err := store.DB.QueryRowContext(ctx, `SELECT key_digest FROM downstream_keys WHERE id=?`, created.Item.ID).Scan(&digestAfterUpdate); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedDigest, digestAfterUpdate) {
		t.Fatal("metadata update changed downstream key digest")
	}

	response = performRequest(handler, http.MethodPatch, apiPrefix+"/"+itoa(created.Item.ID), `{"enabled":true}`, `"1"`)
	assertError(t, response, http.StatusConflict, "revision_conflict")
	response = performRequest(handler, http.MethodPatch, apiPrefix+"/999999", `{"enabled":true}`, `"1"`)
	assertError(t, response, http.StatusNotFound, "not_found")
	response = performRequest(handler, http.MethodPatch, apiPrefix+"/"+itoa(created.Item.ID), `{"allowedModels":[]}`, `"2"`)
	assertError(t, response, http.StatusBadRequest, "invalid_request")
	response = performRequest(handler, http.MethodPatch, apiPrefix+"/"+itoa(created.Item.ID), `{"rpm":12}`, `"2"`)
	assertError(t, response, http.StatusBadRequest, "invalid_request")
	takenRaw := "js_taken000_secret"
	if _, err := store.ImportDigestOnlyDownstreamKey(ctx, vnextstore.DownstreamKeyWrite{
		Name: "Taken", KeyPrefix: "js_taken000", KeyDigest: vnextstore.DigestDownstreamKey(takenRaw), Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	response = performRequest(handler, http.MethodPatch, apiPrefix+"/"+itoa(created.Item.ID), `{"name":"Taken"}`, `"2"`)
	assertError(t, response, http.StatusConflict, "conflict")
	if strings.Contains(response.Body.String(), "UNIQUE") || strings.Contains(response.Body.String(), takenRaw) {
		t.Fatalf("update conflict leaked storage details: %s", response.Body.String())
	}

	if _, err := store.DB.ExecContext(ctx, `UPDATE downstream_keys SET used_nano_usd=6000 WHERE id=?`, created.Item.ID); err != nil {
		t.Fatal(err)
	}
	response = performRequest(handler, http.MethodPatch, apiPrefix+"/"+itoa(created.Item.ID), `{"quotaNanoUSD":5000}`, `"2"`)
	assertError(t, response, http.StatusConflict, "quota_conflict")

}

func TestStoreHandlerRevealAndRotateAreEncryptedAndRevisionGuarded(t *testing.T) {
	handler, store := newStoreHandlerFixture(t)
	ctx := context.Background()

	createdResponse := performRequest(handler, http.MethodPost, apiPrefix, `{"name":"Secret lifecycle"}`, "")
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created struct {
		Item   keyResponse `json:"item"`
		Secret string      `json:"secret"`
	}
	decodeResponse(t, createdResponse, &created)

	reveal := performRequest(handler, http.MethodPost, apiPrefix+"/"+itoa(created.Item.ID)+"/reveal", "", "")
	if reveal.Code != http.StatusOK || reveal.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(reveal.Body.String(), created.Secret) {
		t.Fatalf("reveal status/headers/body = %d %v %s", reveal.Code, reveal.Header(), reveal.Body.String())
	}

	missingCAS := performRequest(handler, http.MethodPost, apiPrefix+"/"+itoa(created.Item.ID)+"/rotate", "", "")
	assertError(t, missingCAS, http.StatusPreconditionRequired, "precondition_required")
	stale := performRequest(handler, http.MethodPost, apiPrefix+"/"+itoa(created.Item.ID)+"/rotate", "", `"2"`)
	assertError(t, stale, http.StatusConflict, "revision_conflict")

	var digestAfterStale []byte
	if err := store.DB.QueryRowContext(ctx, `SELECT key_digest FROM downstream_keys WHERE id=?`, created.Item.ID).Scan(&digestAfterStale); err != nil {
		t.Fatal(err)
	}
	wantOldDigest := sha256.Sum256([]byte(created.Secret))
	if !bytes.Equal(digestAfterStale, wantOldDigest[:]) {
		t.Fatal("stale rotation changed the active digest")
	}

	rotatedResponse := performRequest(handler, http.MethodPost, apiPrefix+"/"+itoa(created.Item.ID)+"/rotate", "", `"1"`)
	if rotatedResponse.Code != http.StatusOK || rotatedResponse.Header().Get("ETag") != `"2"` ||
		rotatedResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("rotate status/headers/body = %d %v %s", rotatedResponse.Code, rotatedResponse.Header(), rotatedResponse.Body.String())
	}
	var rotated struct {
		Item   keyResponse `json:"item"`
		Secret string      `json:"secret"`
	}
	decodeResponse(t, rotatedResponse, &rotated)
	if rotated.Item.ID != created.Item.ID || rotated.Item.Revision != 2 || rotated.Secret == "" || rotated.Secret == created.Secret {
		t.Fatalf("rotated response = %+v", rotated)
	}

	var digestAfterRotate, ciphertext []byte
	if err := store.DB.QueryRowContext(ctx, `SELECT key_digest,encrypted_secret FROM downstream_keys WHERE id=?`, created.Item.ID).
		Scan(&digestAfterRotate, &ciphertext); err != nil {
		t.Fatal(err)
	}
	wantNewDigest := sha256.Sum256([]byte(rotated.Secret))
	if !bytes.Equal(digestAfterRotate, wantNewDigest[:]) || bytes.Equal(digestAfterRotate, wantOldDigest[:]) ||
		bytes.Contains(ciphertext, []byte(rotated.Secret)) {
		t.Fatal("rotation did not atomically replace digest and encrypted reveal copy")
	}
	revealedRotated := performRequest(handler, http.MethodPost, apiPrefix+"/"+itoa(created.Item.ID)+"/reveal", "", "")
	if revealedRotated.Code != http.StatusOK || !strings.Contains(revealedRotated.Body.String(), rotated.Secret) ||
		strings.Contains(revealedRotated.Body.String(), created.Secret) {
		t.Fatalf("rotated reveal = %d %s", revealedRotated.Code, revealedRotated.Body.String())
	}
}

func TestCommittedCreateAndRotateSurviveModelProjectionFailure(t *testing.T) {
	ctx := context.Background()
	store, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "vnext.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{0x62}, 32))
	if err != nil {
		t.Fatal(err)
	}
	keyService, err := downstreamkeys.New(store, box, allowRecentReauth{})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewStoreRepository(store)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := NewKeyIssuerAdapter(keyService)
	if err != nil {
		t.Fatal(err)
	}
	fault := &failingRouteProjectionRepository{
		Repository: repository,
		err:        errors.New("forced route projection failure"),
	}
	handler := New(fault, secrets, secrets)

	createResponse := performRequest(handler, http.MethodPost, apiPrefix, `{"name":"Projection failure"}`, "")
	if createResponse.Code != http.StatusCreated || createResponse.Header().Get("Cache-Control") != "no-store" ||
		createResponse.Header().Get("ETag") != `"1"` {
		t.Fatalf("create after projection failure = %d %v %s", createResponse.Code, createResponse.Header(), createResponse.Body.String())
	}
	var created struct {
		Item   keyResponse `json:"item"`
		Secret string      `json:"secret"`
	}
	decodeResponse(t, createResponse, &created)
	if created.Secret == "" || len(created.Item.Models) != 0 || !strings.Contains(createResponse.Body.String(), `"models":[]`) {
		t.Fatalf("create degraded payload = %+v, body = %s", created, createResponse.Body.String())
	}
	if authenticated, err := keyService.Authenticate(ctx, created.Secret); err != nil || authenticated.ID != created.Item.ID {
		t.Fatalf("committed created secret = %+v, %v", authenticated, err)
	}

	rotateResponse := performRequest(handler, http.MethodPost,
		apiPrefix+"/"+itoa(created.Item.ID)+"/rotate", "", `"1"`)
	if rotateResponse.Code != http.StatusOK || rotateResponse.Header().Get("Cache-Control") != "no-store" ||
		rotateResponse.Header().Get("ETag") != `"2"` {
		t.Fatalf("rotate after projection failure = %d %v %s", rotateResponse.Code, rotateResponse.Header(), rotateResponse.Body.String())
	}
	var rotated struct {
		Item   keyResponse `json:"item"`
		Secret string      `json:"secret"`
	}
	decodeResponse(t, rotateResponse, &rotated)
	if rotated.Secret == "" || rotated.Secret == created.Secret || rotated.Item.Revision != 2 ||
		len(rotated.Item.Models) != 0 || !strings.Contains(rotateResponse.Body.String(), `"models":[]`) {
		t.Fatalf("rotate degraded payload = %+v, body = %s", rotated, rotateResponse.Body.String())
	}
	if _, err := keyService.Authenticate(ctx, created.Secret); !errors.Is(err, downstreamkeys.ErrInvalidKey) {
		t.Fatalf("old secret after committed rotation error = %v", err)
	}
	if authenticated, err := keyService.Authenticate(ctx, rotated.Secret); err != nil || authenticated.ID != created.Item.ID {
		t.Fatalf("new secret after committed rotation = %+v, %v", authenticated, err)
	}
	if fault.calls != 2 {
		t.Fatalf("route projection calls = %d, want create and rotate", fault.calls)
	}
}

func TestStoreDownstreamKeyUpdateRequiresCASAndLeavesGlobalModelUntouched(t *testing.T) {
	_, store := newStoreHandlerFixture(t)
	ctx := context.Background()
	id, err := store.ImportDigestOnlyDownstreamKey(ctx, vnextstore.DownstreamKeyWrite{
		Name: "Imported", KeyPrefix: "js_imported0", KeyDigest: vnextstore.DigestDownstreamKey("js_imported0_secret"), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDownstreamKey(ctx, id, vnextstore.DownstreamKeyUpdate{
		Name: "Missing CAS", Enabled: true,
	}); err == nil {
		t.Fatal("UpdateDownstreamKey accepted a missing expected revision")
	}
	target := mustCreateControlTarget(t, store, "Gamma", "https://gamma.example/v1", "model-a")
	model, err := store.CreatePublishedModel(ctx, vnextstore.PublishedModelWrite{
		PublicName: "model-a", OfficialPriceSKU: "model-a", Enabled: true,
	}, []int64{target})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateDownstreamKey(ctx, id, vnextstore.DownstreamKeyUpdate{
		ExpectedRevision: 2, Name: "Stale", Enabled: true,
	}); !errors.Is(err, vnextstore.ErrRevisionConflict) {
		t.Fatalf("stale UpdateDownstreamKey() error = %v", err)
	}
	item, err := store.UpdateDownstreamKey(ctx, id, vnextstore.DownstreamKeyUpdate{
		ExpectedRevision: 1, Name: "Updated", Enabled: false,
		HourlyQuotaNanoUSD: int64Pointer(1_000), BillingMultiplierBPS: 15_000,
	})
	if err != nil || item.Revision != 2 || item.Name != "Updated" || item.Enabled || item.RPMLimit != 0 ||
		item.HourlyQuotaNanoUSD == nil || *item.HourlyQuotaNanoUSD != 1_000 || item.BillingMultiplierBPS != 15_000 {
		t.Fatalf("UpdateDownstreamKey() = %+v, %v", item, err)
	}
	storedModel, err := store.GetPublishedModel(ctx, model.ID)
	if err != nil || len(storedModel.Targets) != 1 || storedModel.Targets[0].ProviderModelTargetID != target || storedModel.Revision != model.Revision {
		t.Fatalf("metadata update changed global model = %+v, %v", storedModel, err)
	}
}

func newStoreHandlerFixture(t *testing.T) (*Handler, *vnextstore.Store) {
	t.Helper()
	store, err := vnextstore.Open(context.Background(), filepath.Join(t.TempDir(), "vnext.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{0x51}, 32))
	if err != nil {
		t.Fatal(err)
	}
	keyService, err := downstreamkeys.New(store, box, allowRecentReauth{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewStoreHandler(store, keyService)
	if err != nil {
		t.Fatal(err)
	}
	return handler, store
}

type allowRecentReauth struct{}

func (allowRecentReauth) VerifyRecentReauthentication(context.Context) error { return nil }

type failingRouteProjectionRepository struct {
	Repository
	err   error
	calls int
}

func (repository *failingRouteProjectionRepository) ListRoutingProfileRoutes(
	context.Context, int64,
) ([]vnextstore.RoutingProfileRoute, error) {
	repository.calls++
	return nil, repository.err
}

func mustCreateControlTarget(t *testing.T, store *vnextstore.Store, siteName, baseURL, model string) int64 {
	t.Helper()
	ctx := context.Background()
	siteID, err := store.CreateSite(ctx, vnextstore.SiteWrite{Name: siteName, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := store.CreateSiteEndpoint(ctx, siteID, vnextstore.SiteEndpointWrite{
		Name: "Primary", BaseURL: baseURL, WireProtocol: "openai", Surface: "openai.chat_completions", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := store.CreateProviderModelTarget(ctx, vnextstore.ProviderModelTargetWrite{
		SiteID: siteID, EndpointID: endpointID, SourceModel: model, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return targetID
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
