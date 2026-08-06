package accountsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/secrets"
	"github.com/LuTianTian001/JieShan/internal/store"
)

func TestConfigureDoesNotEchoCredentials(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	service, _, upstreamID := newAccountSyncTestService(t, server)
	const apiToken = "management-token-that-must-not-be-returned"

	view, err := service.Configure(context.Background(), upstreamID, ConfigureInput{
		AdapterKey: "new_api", DashboardURL: server.URL, Enabled: true,
		Auth: AuthInput{Kind: "api_token", APIToken: apiToken},
	})
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if !view.Configured || view.Auth == nil || !view.Auth.HasAPIToken || view.Auth.Kind != "api_token" {
		t.Fatalf("configured view = %+v", view)
	}
	assertViewDoesNotContainSecrets(t, view, apiToken)

	storedView, err := service.Get(context.Background(), upstreamID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	assertViewDoesNotContainSecrets(t, storedView, apiToken)
}

func TestCiiiRefreshPersistsSnapshotUsageAndRotatedTokens(t *testing.T) {
	scenario := newCiiiScenarioServer(t)
	defer scenario.server.Close()
	service, database, upstreamID := newAccountSyncTestService(t, scenario.server)
	configureCiiiAccount(t, service, upstreamID, scenario)

	view, err := service.Refresh(context.Background(), upstreamID)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if scenario.refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", scenario.refreshCalls.Load())
	}
	if view.Snapshot == nil || view.Snapshot.Balance == nil || view.Snapshot.Balance.Value != "12.50" || view.Snapshot.Balance.Currency != "USD" {
		t.Fatalf("balance snapshot = %+v", view.Snapshot)
	}
	if view.Snapshot.Subscription == nil || view.Snapshot.Subscription.PlanName != "Pro plan" ||
		view.Snapshot.Subscription.Remaining == nil || view.Snapshot.Subscription.Remaining.Value != "75" {
		t.Fatalf("subscription snapshot = %+v", view.Snapshot)
	}

	usage, err := service.Usage(context.Background(), upstreamID, "30d", 20)
	if err != nil {
		t.Fatalf("Usage() error = %v", err)
	}
	if len(usage.Items) != 1 || usage.Items[0].Amount == nil || usage.Items[0].Amount.Value != "0.42" || usage.Items[0].Amount.Currency != "USD" {
		t.Fatalf("usage view = %+v", usage)
	}
	if usage.Items[0].InputTokens == nil || *usage.Items[0].InputTokens != 120 ||
		usage.Items[0].OutputTokens == nil || *usage.Items[0].OutputTokens != 30 {
		t.Fatalf("usage tokens = %+v", usage.Items[0])
	}

	secret, err := database.GetUpstreamAccountSecret(context.Background(), upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := service.decryptEnvelope(secret.AuthCipher)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Credentials.AccessToken != scenario.newAccess || envelope.Credentials.RefreshToken != scenario.newRefresh {
		t.Fatalf("rotated credentials were not persisted: %+v", authSummary(envelope))
	}
	assertViewDoesNotContainSecrets(t, view, scenario.oldAccess, scenario.oldRefresh, scenario.newAccess, scenario.newRefresh)
}

func TestRefreshFailurePreservesLastSnapshotAndRedactsError(t *testing.T) {
	scenario := newCiiiScenarioServer(t)
	defer scenario.server.Close()
	service, database, upstreamID := newAccountSyncTestService(t, scenario.server)
	configureCiiiAccount(t, service, upstreamID, scenario)
	if _, err := service.Refresh(context.Background(), upstreamID); err != nil {
		t.Fatalf("initial Refresh() error = %v", err)
	}
	beforeAccount, err := database.GetUpstreamAccount(context.Background(), upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	beforeSnapshot, err := database.GetLatestUpstreamAccountSnapshot(context.Background(), upstreamID)
	if err != nil {
		t.Fatal(err)
	}

	scenario.failSnapshot.Store(true)
	_, err = service.Refresh(context.Background(), upstreamID)
	if err == nil {
		t.Fatal("failed Refresh() returned nil error")
	}
	for _, secret := range []string{scenario.newAccess, scenario.newRefresh, "Bearer " + scenario.newAccess} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("Refresh() error leaked %q: %v", secret, err)
		}
	}

	afterAccount, err := database.GetUpstreamAccount(context.Background(), upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	afterSnapshot, err := database.GetLatestUpstreamAccountSnapshot(context.Background(), upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if beforeAccount.LastSuccessAt == nil || afterAccount.LastSuccessAt == nil || *afterAccount.LastSuccessAt != *beforeAccount.LastSuccessAt {
		t.Fatalf("last success changed after failure: before=%+v after=%+v", beforeAccount, afterAccount)
	}
	if afterSnapshot.ID != beforeSnapshot.ID || !bytes.Equal(afterSnapshot.Snapshot, beforeSnapshot.Snapshot) {
		t.Fatalf("snapshot changed after failure: before=%+v after=%+v", beforeSnapshot, afterSnapshot)
	}
	if afterAccount.SyncState != "error" || afterAccount.LastErrorCode != "upstream_unavailable" {
		t.Fatalf("failure state = %+v", afterAccount)
	}
	for _, secret := range []string{scenario.newAccess, scenario.newRefresh, "Bearer " + scenario.newAccess} {
		if strings.Contains(afterAccount.LastErrorMessage, secret) {
			t.Fatalf("stored error leaked %q: %s", secret, afterAccount.LastErrorMessage)
		}
	}

	view, err := service.Get(context.Background(), upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Snapshot == nil || view.Snapshot.Balance == nil || view.Snapshot.Balance.Value != "12.50" {
		t.Fatalf("last good snapshot is not visible: %+v", view.Snapshot)
	}
	if view.Sync.Error == nil || view.Sync.Error.Code != "upstream_unavailable" {
		t.Fatalf("sync error view = %+v", view.Sync)
	}
	assertViewDoesNotContainSecrets(t, view, scenario.newAccess, scenario.newRefresh)
}

func TestRefreshRotationPersistsWhenSnapshotRetryFails(t *testing.T) {
	const (
		oldAccess  = "rotation-old-access"
		oldRefresh = "rotation-old-refresh"
		newAccess  = "rotation-new-access"
		newRefresh = "rotation-new-refresh"
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/auth/refresh":
			fmt.Fprintf(writer, `{"code":0,"data":{"access_token":%q,"refresh_token":%q,"expires_in":3600}}`, newAccess, newRefresh)
		case "/api/v1/auth/me":
			switch request.Header.Get("Authorization") {
			case "Bearer " + oldAccess:
				writer.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(writer, `{"code":"TOKEN_EXPIRED","message":"expired"}`)
			case "Bearer " + newAccess:
				writer.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(writer, `{"code":"BROKEN","message":"retry failed"}`)
			default:
				t.Errorf("unexpected Authorization header %q", request.Header.Get("Authorization"))
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	service, database, upstreamID := newAccountSyncTestService(t, server)
	if _, err := service.Configure(context.Background(), upstreamID, ConfigureInput{
		AdapterKey: "ciii", DashboardURL: server.URL, Enabled: true,
		Auth: AuthInput{Kind: "access_refresh", AccessToken: oldAccess, RefreshToken: oldRefresh},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(context.Background(), upstreamID); err == nil {
		t.Fatal("Refresh() unexpectedly succeeded")
	}
	secret, err := database.GetUpstreamAccountSecret(context.Background(), upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := service.decryptEnvelope(secret.AuthCipher)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Credentials.AccessToken != newAccess || envelope.Credentials.RefreshToken != newRefresh {
		t.Fatalf("rotated credentials were lost after snapshot failure: %+v", authSummary(envelope))
	}
}

func TestRefreshRotationPersistsWhenSuccessTransactionFails(t *testing.T) {
	scenario := newCiiiScenarioServer(t)
	defer scenario.server.Close()
	service, database, upstreamID := newAccountSyncTestService(t, scenario.server)
	configureCiiiAccount(t, service, upstreamID, scenario)
	if _, err := database.DB.Exec(`CREATE TRIGGER reject_account_snapshot BEFORE INSERT ON upstream_account_snapshots
BEGIN SELECT RAISE(ABORT, 'forced snapshot failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(context.Background(), upstreamID); err == nil {
		t.Fatal("Refresh() unexpectedly succeeded")
	}
	secret, err := database.GetUpstreamAccountSecret(context.Background(), upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := service.decryptEnvelope(secret.AuthCipher)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Credentials.AccessToken != scenario.newAccess || envelope.Credentials.RefreshToken != scenario.newRefresh {
		t.Fatalf("rotated credentials were lost after transaction failure: %+v", authSummary(envelope))
	}
}

func TestRefreshIsNonBlockingAndConfigureCannotBeOverwritten(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/status":
			fmt.Fprint(writer, `{"success":true,"data":{"quota_per_unit":500000}}`)
		case "/api/user/self":
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			fmt.Fprint(writer, `{"success":true,"data":{"id":1,"username":"sync-user","quota":1}}`)
		case "/api/subscription/self":
			fmt.Fprint(writer, `{"success":true,"data":{"all_subscriptions":[]}}`)
		case "/api/log/self":
			fmt.Fprint(writer, `{"success":true,"data":{"page":1,"page_size":100,"total":0,"items":[]}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	service, database, upstreamID := newAccountSyncTestService(t, server)
	if _, err := service.Configure(context.Background(), upstreamID, ConfigureInput{
		AdapterKey: "new_api", DashboardURL: server.URL, Enabled: true,
		Auth: AuthInput{Kind: "api_token", APIToken: "old-account-token"},
	}); err != nil {
		t.Fatal(err)
	}
	refreshDone := make(chan error, 1)
	go func() {
		_, err := service.Refresh(context.Background(), upstreamID)
		refreshDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not reach the blocking snapshot")
	}
	started := time.Now()
	if _, err := service.Refresh(context.Background(), upstreamID); !errors.Is(err, ErrSyncInProgress) {
		t.Fatalf("second Refresh() error = %v, want ErrSyncInProgress", err)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("second Refresh() waited instead of returning immediately")
	}

	configureDone := make(chan error, 1)
	go func() {
		_, err := service.Configure(context.Background(), upstreamID, ConfigureInput{
			AdapterKey: "new_api", DashboardURL: server.URL, Enabled: true,
			Auth: AuthInput{Kind: "api_token", APIToken: "new-account-token"},
		})
		configureDone <- err
	}()
	select {
	case err := <-configureDone:
		t.Fatalf("Configure() bypassed the refresh lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-refreshDone; err != nil {
		t.Fatalf("first Refresh() error = %v", err)
	}
	if err := <-configureDone; err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	secret, err := database.GetUpstreamAccountSecret(context.Background(), upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := service.decryptEnvelope(secret.AuthCipher)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Credentials.Authorization != "new-account-token" {
		t.Fatalf("stale refresh overwrote the new account token: %+v", authSummary(envelope))
	}
}

func TestUsageSyncUsesInitialThenIncrementalWindow(t *testing.T) {
	starts := make(chan int64, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/status":
			fmt.Fprint(writer, `{"success":true,"data":{"quota_per_unit":500000}}`)
		case "/api/user/self":
			fmt.Fprint(writer, `{"success":true,"data":{"id":1,"username":"window-user","quota":1}}`)
		case "/api/subscription/self":
			fmt.Fprint(writer, `{"success":true,"data":{"all_subscriptions":[]}}`)
		case "/api/log/self":
			start, _ := strconv.ParseInt(request.URL.Query().Get("start_timestamp"), 10, 64)
			starts <- start
			fmt.Fprint(writer, `{"success":true,"data":{"page":1,"page_size":100,"total":0,"items":[]}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	service, database, upstreamID := newAccountSyncTestService(t, server)
	if _, err := service.Configure(context.Background(), upstreamID, ConfigureInput{
		AdapterKey: "new_api", DashboardURL: server.URL, Enabled: true,
		Auth: AuthInput{Kind: "api_token", APIToken: "window-token"},
	}); err != nil {
		t.Fatal(err)
	}
	beforeFirst := time.Now()
	if _, err := service.Refresh(context.Background(), upstreamID); err != nil {
		t.Fatal(err)
	}
	firstStart := <-starts
	if firstStart < beforeFirst.Add(-usageInitialRange-time.Minute).Unix() || firstStart > beforeFirst.Add(-usageInitialRange+time.Minute).Unix() {
		t.Fatalf("initial usage start = %d, want about 30 days ago", firstStart)
	}
	account, err := database.GetUpstreamAccount(context.Background(), upstreamID)
	if err != nil || account.LastSuccessAt == nil {
		t.Fatalf("account after first refresh = %+v, %v", account, err)
	}
	expectedSecondStart := time.UnixMilli(*account.LastSuccessAt).Add(-usageSyncOverlap).Unix()
	if _, err := service.Refresh(context.Background(), upstreamID); err != nil {
		t.Fatal(err)
	}
	if secondStart := <-starts; secondStart != expectedSecondStart {
		t.Fatalf("incremental usage start = %d, want %d", secondStart, expectedSecondStart)
	}
}

func TestNewAndOneAPIKeepRawQuotaInAccountAndUsageViews(t *testing.T) {
	for _, adapterKey := range []string{"new_api", "one_api"} {
		t.Run(adapterKey, func(t *testing.T) {
			const token = "raw-quota-management-token"
			nowUnix := time.Now().UTC().Unix()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				expectedAuth := token
				if adapterKey == "new_api" {
					expectedAuth = "Bearer " + token
				}
				switch request.URL.Path {
				case "/api/status":
					fmt.Fprint(writer, `{"success":true,"message":"","data":{"quota_per_unit":500000}}`)
				case "/api/user/self":
					if got := request.Header.Get("Authorization"); got != expectedAuth {
						t.Errorf("Authorization = %q, want %q", got, expectedAuth)
					}
					fmt.Fprint(writer, `{"success":true,"message":"","data":{"id":7,"username":"quota-user","quota":250000.125,"used_quota":125000.25,"request_count":3}}`)
				case "/api/subscription/self":
					if adapterKey != "new_api" {
						http.NotFound(writer, request)
						return
					}
					fmt.Fprint(writer, `{"success":true,"message":"","data":{"all_subscriptions":[{"subscription":{"id":1,"status":"active","amount_total":1000000,"amount_used":250000},"plan":{"title":"Quota plan"}}]}}`)
				case "/api/log/self":
					if got := request.Header.Get("Authorization"); got != expectedAuth {
						t.Errorf("Authorization = %q, want %q", got, expectedAuth)
					}
					item := fmt.Sprintf(`{"id":9,"request_id":"raw-quota-request","model_name":"model-raw","quota":125000.5,"prompt_tokens":20,"completion_tokens":5,"created_at":%d}`, nowUnix)
					if adapterKey == "new_api" {
						fmt.Fprintf(writer, `{"success":true,"message":"","data":{"page":1,"page_size":100,"total":1,"items":[%s]}}`, item)
					} else {
						fmt.Fprintf(writer, `{"success":true,"message":"","data":[%s]}`, item)
					}
				default:
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()

			service, _, upstreamID := newAccountSyncTestService(t, server)
			if _, err := service.Configure(context.Background(), upstreamID, ConfigureInput{
				AdapterKey: adapterKey, DashboardURL: server.URL, Enabled: true,
				Auth: AuthInput{Kind: "api_token", APIToken: token},
			}); err != nil {
				t.Fatalf("Configure() error = %v", err)
			}
			view, err := service.Refresh(context.Background(), upstreamID)
			if err != nil {
				t.Fatalf("Refresh() error = %v", err)
			}
			if view.Snapshot == nil || view.Snapshot.Balance == nil {
				t.Fatalf("account snapshot = %+v", view.Snapshot)
			}
			balance := view.Snapshot.Balance
			if balance.Value != "250000.125" || balance.Currency != "quota" || balance.Display != "" || balance.SourceLabel != "站点原始 quota" {
				t.Fatalf("raw account quota was converted: %+v", balance)
			}

			usage, err := service.Usage(context.Background(), upstreamID, "30d", 10)
			if err != nil {
				t.Fatalf("Usage() error = %v", err)
			}
			if len(usage.Items) != 1 || usage.Items[0].Amount == nil {
				t.Fatalf("usage view = %+v", usage)
			}
			amount := usage.Items[0].Amount
			if amount.Value != "125000.5" || amount.Currency != "quota" || amount.Display != "" {
				t.Fatalf("raw usage quota was converted: %+v", amount)
			}
			assertViewDoesNotContainSecrets(t, view, token)
		})
	}
}

type ciiiScenario struct {
	server       *httptest.Server
	failSnapshot atomic.Bool
	refreshCalls atomic.Int32
	oldAccess    string
	oldRefresh   string
	newAccess    string
	newRefresh   string
}

func newCiiiScenarioServer(t *testing.T) *ciiiScenario {
	t.Helper()
	scenario := &ciiiScenario{
		oldAccess: "old-ciii-access-token", oldRefresh: "old-ciii-refresh-token",
		newAccess: "new-ciii-access-token", newRefresh: "new-ciii-refresh-token",
	}
	now := time.Now().UTC()
	scenario.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/auth/refresh":
			scenario.refreshCalls.Add(1)
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode refresh body: %v", err)
			}
			if body["refresh_token"] != scenario.oldRefresh {
				t.Errorf("refresh token = %q", body["refresh_token"])
			}
			fmt.Fprintf(writer, `{"code":0,"data":{"access_token":%q,"refresh_token":%q,"token_type":"Bearer","expires_in":3600}}`, scenario.newAccess, scenario.newRefresh)
		case "/api/v1/auth/me":
			authorization := request.Header.Get("Authorization")
			if scenario.failSnapshot.Load() {
				writer.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintf(writer, `{"code":"BROKEN","message":"access %s and refresh %s failed"}`, scenario.newAccess, scenario.newRefresh)
				return
			}
			if authorization == "Bearer "+scenario.oldAccess {
				writer.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(writer, `{"code":"TOKEN_EXPIRED","message":"expired"}`)
				return
			}
			if authorization != "Bearer "+scenario.newAccess {
				t.Errorf("snapshot Authorization = %q", authorization)
			}
			fmt.Fprint(writer, `{"code":0,"data":{"id":319,"email":"quota@example.com","status":"active","currency":"USD","balance":"12.50","used_balance":"3.25"}}`)
		case "/api/v1/subscriptions/active":
			if got := request.Header.Get("Authorization"); got != "Bearer "+scenario.newAccess {
				t.Errorf("subscription Authorization = %q", got)
			}
			fmt.Fprintf(writer, `{"code":0,"data":[{"id":"sub-1","name":"Pro plan","status":"active","currency":"USD","amount_total":"100","amount_used":"25","starts_at":%q,"expires_at":%q}]}`,
				now.Add(-24*time.Hour).Format(time.RFC3339), now.Add(29*24*time.Hour).Format(time.RFC3339))
		case "/api/v1/usage":
			if got := request.Header.Get("Authorization"); got != "Bearer "+scenario.newAccess {
				t.Errorf("usage Authorization = %q", got)
			}
			fmt.Fprintf(writer, `{"code":0,"data":{"page":1,"page_size":100,"pages":1,"total":1,"items":[{"id":"usage-1","request_id":"req-1","model":"gpt-test","input_tokens":120,"output_tokens":30,"actual_cost":"0.42","status_code":200,"created_at":%q}]}}`, now.Format(time.RFC3339))
		default:
			http.NotFound(writer, request)
		}
	}))
	return scenario
}

func newAccountSyncTestService(t *testing.T, server *httptest.Server) (*Service, *store.Store, int64) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "accountsync.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	upstreamID, err := database.CreateUpstream(ctx, store.UpstreamWrite{
		Name: "account-sync-test", Kind: "compatible", DashboardURL: server.URL, BaseURL: server.URL + "/v1",
		Enabled: true, CustomHeaders: json.RawMessage(`{}`), SecretCipher: []byte("inference-only-cipher"),
	})
	if err != nil {
		t.Fatalf("CreateUpstream() error = %v", err)
	}
	cipher, err := secrets.New(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(database, cipher, server.Client(), logger, time.Hour), database, upstreamID
}

func configureCiiiAccount(t *testing.T, service *Service, upstreamID int64, scenario *ciiiScenario) {
	t.Helper()
	view, err := service.Configure(context.Background(), upstreamID, ConfigureInput{
		AdapterKey: "ciii", DashboardURL: scenario.server.URL, Enabled: true,
		Auth: AuthInput{Kind: "access_refresh", AccessToken: scenario.oldAccess, RefreshToken: scenario.oldRefresh},
	})
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	assertViewDoesNotContainSecrets(t, view, scenario.oldAccess, scenario.oldRefresh)
}

func assertViewDoesNotContainSecrets(t *testing.T, view AccountView, values ...string) {
	t.Helper()
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value != "" && bytes.Contains(encoded, []byte(value)) {
			t.Fatalf("account view leaked credential %q: %s", value, encoded)
		}
	}
}
