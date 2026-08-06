package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

func TestSiteAccountLifecycleAndSyncData(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, s, "Account Site")

	accountID, err := s.CreateSiteAccount(ctx, SiteAccountWrite{
		SiteID: siteID, AdapterKind: " New_API ", APIOrigin: "https://panel.example.com/",
		AuthCipher: []byte("encrypted-token"), Enabled: true,
		Capabilities: json.RawMessage(`{"balance":true,"usage":true}`),
	})
	if err != nil || accountID == 0 {
		t.Fatalf("CreateSiteAccount() = %d, %v", accountID, err)
	}
	account, err := s.GetSiteAccount(ctx, siteID)
	if err != nil || account.AdapterKind != "new_api" || account.APIOrigin != "https://panel.example.com" || !account.AuthConfigured {
		t.Fatalf("GetSiteAccount() = %+v, %v", account, err)
	}
	secret, err := s.GetSiteAccountSecret(ctx, siteID)
	if err != nil || !bytes.Equal(secret.AuthCipher, []byte("encrypted-token")) {
		t.Fatalf("GetSiteAccountSecret() = %+v, %v", secret.SiteAccount, err)
	}

	occurredAt := int64(900)
	if err := s.UpdateSiteAccountSyncSuccess(ctx, siteID, SiteAccountSyncSuccess{
		AttemptedAt: 1_000, SucceededAt: 1_100, SnapshotAt: 1_050,
		Capabilities: json.RawMessage(`{"balance":true,"usage":true}`),
		Snapshot:     json.RawMessage(`{"version":1,"account":{"balance":"12","currency":"USD"}}`),
		Usage: []UpstreamAccountUsageWrite{{
			DedupeKey: "request-a", ExternalID: "a", ModelName: "model-a", Amount: "0.01", Unit: "USD",
			Raw: json.RawMessage(`{"id":"a"}`), OccurredAt: &occurredAt,
		}},
		RotatedAuthCipher: []byte("rotated-token"),
	}); err != nil {
		t.Fatal(err)
	}
	secret, err = s.GetSiteAccountSecret(ctx, siteID)
	if err != nil || secret.SyncState != "healthy" || !bytes.Equal(secret.AuthCipher, []byte("rotated-token")) {
		t.Fatalf("synced secret = %+v auth=%q, %v", secret.SiteAccount, secret.AuthCipher, err)
	}
	snapshot, err := s.GetLatestSiteAccountSnapshot(ctx, siteID)
	if err != nil || snapshot.CapturedAt != 1_050 {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	usage, err := s.ListSiteAccountUsage(ctx, siteID, UpstreamAccountUsageQuery{Limit: 10})
	if err != nil || len(usage) != 1 || usage[0].DedupeKey != "request-a" || usage[0].OccurredAt == nil || *usage[0].OccurredAt != occurredAt {
		t.Fatalf("usage = %+v, %v", usage, err)
	}

	if err := s.UpdateSiteAccountSyncFailure(ctx, siteID, SiteAccountSyncFailure{
		AttemptedAt: 1_200, State: "error", ErrorCode: "upstream_error", ErrorMessage: "Bearer sk-secret-value",
	}); err != nil {
		t.Fatal(err)
	}
	account, err = s.GetSiteAccount(ctx, siteID)
	if err != nil || account.SyncState != "error" || account.LastSuccessAt == nil || *account.LastSuccessAt != 1_100 {
		t.Fatalf("failed account state = %+v, %v", account, err)
	}
	if account.LastErrorMessage == "Bearer sk-secret-value" {
		t.Fatal("site account error was not redacted")
	}

	if err := s.DeleteSiteAccount(ctx, siteID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSiteAccount(ctx, siteID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetSiteAccount() after delete = %v", err)
	}
}
