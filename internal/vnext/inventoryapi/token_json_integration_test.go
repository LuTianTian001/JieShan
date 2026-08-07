package inventoryapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/adminauth"
	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestTokenJSONPreviewImportIsSessionBoundOneTimeAndSecretSafe(t *testing.T) {
	storage, err := vnextstore.Open(context.Background(), filepath.Join(t.TempDir(), "token-import.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{0x52}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewStoreHandler(storage, box, vnextprotocol.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	siteID, err := storage.CreateSite(context.Background(), vnextstore.SiteWrite{Name: "Token relay", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.CreateSiteCredential(context.Background(), siteID, vnextstore.SiteCredentialWrite{
		Name: "Existing", SecretCipher: []byte("existing-ciphertext"), CipherVersion: 1, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	const importedSecret = "sk-alpha-secret-1234567890"
	rawJSON := `{"accounts":[
		{"name":"Alpha","credentialName":"alpha","platform":"openai","base_url":"https://relay.example/v1","access_token":"` + importedSecret + `","scopes":["models:read"]},
		{"name":"Beta","credentialName":"beta","platform":"openai","base_url":"https://relay.example/v1","api_key":"` + importedSecret + `"},
		{"name":"Refresh only","platform":"openai","base_url":"https://relay.example/v1","refresh_token":"must-not-import"},
		{"name":"Existing","platform":"anthropic","endpoint":"https://claude.example/v1","token":"anthropic-secret-value"}
	]}`
	previewBody, err := json.Marshal(map[string]string{"rawJson": rawJSON})
	if err != nil {
		t.Fatal(err)
	}
	previewRecorder := tokenJSONRequest(handler, http.MethodPost,
		tokenJSONSitePath(siteID, "preview"), previewBody, "session-a")
	requireStatus(t, previewRecorder, http.StatusOK)
	if strings.Contains(previewRecorder.Body.String(), importedSecret) || strings.Contains(previewRecorder.Body.String(), "must-not-import") {
		t.Fatalf("preview leaked secret material: %s", previewRecorder.Body.String())
	}
	var preview tokenJSONImportPreview
	decodeRecorder(t, previewRecorder, &preview)
	if preview.SiteID != siteID || preview.DetectedFormat != "accounts_envelope" || preview.ReadyCount != 1 ||
		preview.DuplicateCount != 2 || preview.InvalidCount != 1 || len(preview.Items) != 4 {
		t.Fatalf("preview = %#v", preview)
	}
	if preview.Items[0].TokenHint != "sk-a...7890" || preview.Items[0].Status != "ready" ||
		preview.Items[1].Status != "duplicate" || preview.Items[2].Status != "invalid" || preview.Items[3].Status != "duplicate" {
		t.Fatalf("preview items = %#v", preview.Items)
	}

	importBody, err := json.Marshal(map[string]any{"previewId": preview.PreviewID, "indices": []int{0}})
	if err != nil {
		t.Fatal(err)
	}
	wrongSession := tokenJSONRequest(handler, http.MethodPost,
		tokenJSONSitePath(siteID, "import"), importBody, "session-b")
	requireStatus(t, wrongSession, http.StatusConflict)

	importRecorder := tokenJSONRequest(handler, http.MethodPost,
		tokenJSONSitePath(siteID, "import"), importBody, "session-a")
	requireStatus(t, importRecorder, http.StatusCreated)
	if strings.Contains(importRecorder.Body.String(), importedSecret) {
		t.Fatalf("import response leaked secret material: %s", importRecorder.Body.String())
	}
	var result tokenJSONImportResponse
	decodeRecorder(t, importRecorder, &result)
	if result.ImportedCount != 1 || result.SkippedCount != 3 || len(result.CredentialIDs) != 1 || len(result.EndpointIDs) != 1 {
		t.Fatalf("import result = %#v", result)
	}
	secondImport := tokenJSONRequest(handler, http.MethodPost,
		tokenJSONSitePath(siteID, "import"), importBody, "session-a")
	requireStatus(t, secondImport, http.StatusOK)
	var replay tokenJSONImportResponse
	decodeRecorder(t, secondImport, &replay)
	if replay.ImportedCount != result.ImportedCount || replay.SkippedCount != result.SkippedCount ||
		len(replay.CredentialIDs) != 1 || replay.CredentialIDs[0] != result.CredentialIDs[0] ||
		len(replay.EndpointIDs) != 1 || replay.EndpointIDs[0] != result.EndpointIDs[0] {
		t.Fatalf("idempotent replay = %#v, original = %#v", replay, result)
	}
	differentImportBody, err := json.Marshal(map[string]any{"previewId": preview.PreviewID, "indices": []int{0, 2}})
	if err != nil {
		t.Fatal(err)
	}
	differentImport := tokenJSONRequest(handler, http.MethodPost,
		tokenJSONSitePath(siteID, "import"), differentImportBody, "session-a")
	requireStatus(t, differentImport, http.StatusConflict)
	if !strings.Contains(differentImport.Body.String(), `"code":"token_preview_consumed"`) {
		t.Fatalf("different selection error = %s", differentImport.Body.String())
	}

	var ciphertext []byte
	if err := storage.DB.QueryRow(`SELECT secret_cipher FROM site_credentials WHERE id=?`, result.CredentialIDs[0]).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(importedSecret)) {
		t.Fatalf("stored credential contains plaintext: %q", ciphertext)
	}
	plaintext, err := box.Open(secretbox.PurposeSiteCredential, secretbox.Identity{
		RecordID: result.CredentialIDs[0], OwnerID: siteID,
	}, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plaintext)
	if string(plaintext) != importedSecret {
		t.Fatalf("decrypted credential = %q", plaintext)
	}
	bindings, err := storage.ListEndpointCredentialBindings(context.Background(), result.EndpointIDs[0])
	if err != nil || len(bindings) != 1 || bindings[0].CredentialID != result.CredentialIDs[0] {
		t.Fatalf("bindings = %#v, %v", bindings, err)
	}

	legacyBody := []byte(`{"previewId":"abcdefghijklmnopqrstuvwx","selectedIndexes":[0]}`)
	legacy := tokenJSONRequest(handler, http.MethodPost, tokenJSONSitePath(siteID, "import"), legacyBody, "session-a")
	requireStatus(t, legacy, http.StatusBadRequest)
}

func TestTokenJSONImportRejectsInvalidSelectionWithoutConsumingPreview(t *testing.T) {
	storage, err := vnextstore.Open(context.Background(), filepath.Join(t.TempDir(), "token-selection.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{0x39}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewStoreHandler(storage, box, vnextprotocol.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	siteID, err := storage.CreateSite(context.Background(), vnextstore.SiteWrite{Name: "Selection relay", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	previewRecorder := tokenJSONPreviewRequest(t, handler, siteID,
		`{"name":"one","platform":"openai","base_url":"https://relay.example/v1","token":"secret-value-123"}`,
		"session-a")
	requireStatus(t, previewRecorder, http.StatusOK)
	var preview tokenJSONImportPreview
	decodeRecorder(t, previewRecorder, &preview)

	invalidBody, err := json.Marshal(map[string]any{"previewId": preview.PreviewID, "indices": []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	invalid := tokenJSONRequest(handler, http.MethodPost, tokenJSONSitePath(siteID, "import"), invalidBody, "session-a")
	requireStatus(t, invalid, http.StatusBadRequest)

	duplicateFieldBody := []byte(`{"previewId":"` + preview.PreviewID + `","previewId":"` + preview.PreviewID + `","indices":[0]}`)
	duplicateField := tokenJSONRequest(handler, http.MethodPost,
		tokenJSONSitePath(siteID, "import"), duplicateFieldBody, "session-a")
	requireStatus(t, duplicateField, http.StatusBadRequest)

	validBody, err := json.Marshal(map[string]any{"previewId": preview.PreviewID, "indices": []int{0}})
	if err != nil {
		t.Fatal(err)
	}
	valid := tokenJSONRequest(handler, http.MethodPost, tokenJSONSitePath(siteID, "import"), validBody, "session-a")
	requireStatus(t, valid, http.StatusCreated)
}

func TestTokenJSONImportConflictRollsBackAndConsumesPreview(t *testing.T) {
	storage, err := vnextstore.Open(context.Background(), filepath.Join(t.TempDir(), "token-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{0x71}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewStoreHandler(storage, box, vnextprotocol.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	siteID, err := storage.CreateSite(context.Background(), vnextstore.SiteWrite{Name: "Conflict relay", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	previewRecorder := tokenJSONPreviewRequest(t, handler, siteID,
		`{"name":"alpha","platform":"openai","base_url":"https://relay.example/v1","token":"secret-value-123"}`,
		"session-a")
	requireStatus(t, previewRecorder, http.StatusOK)
	var preview tokenJSONImportPreview
	decodeRecorder(t, previewRecorder, &preview)
	if _, err := storage.CreateSiteCredential(context.Background(), siteID, vnextstore.SiteCredentialWrite{
		Name: "alpha", SecretCipher: []byte("existing-ciphertext"), CipherVersion: 1, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	importBody, err := json.Marshal(map[string]any{"previewId": preview.PreviewID, "indices": []int{0}})
	if err != nil {
		t.Fatal(err)
	}
	conflict := tokenJSONRequest(handler, http.MethodPost, tokenJSONSitePath(siteID, "import"), importBody, "session-a")
	requireStatus(t, conflict, http.StatusConflict)
	retry := tokenJSONRequest(handler, http.MethodPost, tokenJSONSitePath(siteID, "import"), importBody, "session-a")
	requireStatus(t, retry, http.StatusConflict)
	if !strings.Contains(retry.Body.String(), `"code":"token_preview_consumed"`) {
		t.Fatalf("retry error = %s", retry.Body.String())
	}

	for table, want := range map[string]int{"site_credentials": 1, "site_endpoints": 0, "credential_endpoint_bindings": 0} {
		var count int
		if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE site_id=?`, siteID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s rows after conflict = %d, want %d", table, count, want)
		}
	}
}

func TestTokenJSONPreviewStoreReportsConcurrentImportState(t *testing.T) {
	store := newTokenJSONPreviewStore()
	entry := &tokenJSONPreviewEntry{
		SiteID:       7,
		SessionScope: "session:test",
		Items: []tokenJSONPreviewItem{{
			Preview: tokenJSONAccountPreview{Index: 0, Status: "ready"},
			Import:  TokenJSONImportItem{Secret: []byte("secret-value")},
		}},
	}
	if err := store.put(entry); err != nil {
		t.Fatal(err)
	}
	claimed, replay, err := store.claim(entry.ID, entry.SiteID, entry.SessionScope, []int{0})
	if err != nil || claimed != entry || replay != nil {
		t.Fatalf("first claim = entry %p replay %#v error %v", claimed, replay, err)
	}
	if _, _, err := store.claim(entry.ID, entry.SiteID, entry.SessionScope, []int{0}); !errors.Is(err, errTokenJSONImportInProgress) {
		t.Fatalf("same concurrent claim error = %v", err)
	}
	if _, _, err := store.claim(entry.ID, entry.SiteID, entry.SessionScope, []int{0, 1}); !errors.Is(err, errTokenJSONPreviewConsumed) {
		t.Fatalf("different concurrent claim error = %v", err)
	}
	store.fail(entry)
}

func TestTokenJSONPreviewRejectsAmbiguityAndExpires(t *testing.T) {
	storage, err := vnextstore.Open(context.Background(), filepath.Join(t.TempDir(), "token-preview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{0x61}, 32))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewStoreHandler(storage, box, vnextprotocol.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	siteID, err := storage.CreateSite(context.Background(), vnextstore.SiteWrite{Name: "Expiry relay", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	handler.tokenJSONPreviews.now = func() time.Time { return now }

	duplicateKey := tokenJSONPreviewRequest(t, handler, siteID,
		`{"name":"one","name":"two","platform":"openai","base_url":"https://relay.example/v1","token":"secret-value-123"}`,
		"session-a")
	requireStatus(t, duplicateKey, http.StatusBadRequest)
	if strings.Contains(duplicateKey.Body.String(), "secret-value-123") {
		t.Fatalf("strict JSON error leaked token: %s", duplicateKey.Body.String())
	}
	ambiguousWrapper := tokenJSONPreviewRequest(t, handler, siteID, `{"accounts":[],"tokens":[]}`, "session-a")
	requireStatus(t, ambiguousWrapper, http.StatusBadRequest)

	previewRecorder := tokenJSONPreviewRequest(t, handler, siteID,
		`{"name":"one","platform":"openai","base_url":"https://relay.example/v1","token":"secret-value-123"}`,
		"session-a")
	requireStatus(t, previewRecorder, http.StatusOK)
	var preview tokenJSONImportPreview
	decodeRecorder(t, previewRecorder, &preview)
	now = now.Add(tokenJSONPreviewTTL)
	importBody, err := json.Marshal(map[string]any{"previewId": preview.PreviewID, "indices": []int{0}})
	if err != nil {
		t.Fatal(err)
	}
	expired := tokenJSONRequest(handler, http.MethodPost, tokenJSONSitePath(siteID, "import"), importBody, "session-a")
	requireStatus(t, expired, http.StatusConflict)
	var credentialCount int
	if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM site_credentials WHERE site_id=?`, siteID).Scan(&credentialCount); err != nil {
		t.Fatal(err)
	}
	if credentialCount != 0 {
		t.Fatalf("credentials after expired import = %d", credentialCount)
	}
}

func TestParseTokenJSONSupportsApprovedRootShapes(t *testing.T) {
	account := `{"name":"one","platform":"openai","base_url":"https://relay.example/v1","token":"secret-value-123"}`
	tests := []struct {
		name       string
		raw        string
		wantFormat string
	}{
		{name: "single", raw: account, wantFormat: "single_account"},
		{name: "array", raw: `[` + account + `]`, wantFormat: "account_array"},
		{name: "accounts", raw: `{"accounts":[` + account + `]}`, wantFormat: "accounts_envelope"},
		{name: "tokens", raw: `{"tokens":[` + account + `]}`, wantFormat: "tokens_envelope"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			format, items, err := parseTokenJSON([]byte(test.raw), nil, nil)
			if err != nil || format != test.wantFormat || len(items) != 1 || items[0].Preview.Status != "ready" {
				t.Fatalf("parse = format %q items %#v error %v", format, items, err)
			}
			clear(items[0].Import.Secret)
			items[0].Import.Secret = nil
		})
	}
}

func tokenJSONPreviewRequest(t *testing.T, handler http.Handler, siteID int64, rawJSON, session string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"rawJson": rawJSON})
	if err != nil {
		t.Fatal(err)
	}
	return tokenJSONRequest(handler, http.MethodPost, tokenJSONSitePath(siteID, "preview"), body, session)
}

func tokenJSONRequest(handler http.Handler, method, path string, body []byte, session string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if session != "" {
		request.AddCookie(&http.Cookie{Name: adminauth.SessionCookieName, Value: session})
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func tokenJSONSitePath(siteID int64, operation string) string {
	return "/api/vnext/inventory/sites/" + strconv.FormatInt(siteID, 10) + "/token-json/" + operation
}
