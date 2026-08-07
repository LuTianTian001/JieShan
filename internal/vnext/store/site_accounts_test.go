package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestSiteAccountPersistsExactBalanceAndDeduplicatedSourceUsage(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, storage, "Account source")
	connection, err := storage.CreateSealedSiteAccountConnection(ctx, siteID, SealedSiteAccountConnectionInput{
		AdapterKind: "ciii", Origin: "https://account.example/", CipherVersion: 1, Enabled: true,
	}, func(connectionID, ownerSiteID int64) ([]byte, error) {
		if connectionID <= 0 || ownerSiteID != siteID {
			t.Fatalf("sealer identity = %d/%d", connectionID, ownerSiteID)
		}
		return []byte("sealed-account-secret"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Origin != "https://account.example" || !connection.SecretConfigured || connection.Revision != 1 {
		t.Fatalf("connection = %+v", connection)
	}

	usedValue, usedUnit := "2.75", "USD"
	balance, err := storage.SaveSiteBalanceSnapshot(ctx, siteID, "ciii", SiteBalanceSnapshotWrite{
		AccountRemoteID: "319", AccountName: "owner@example.com",
		AvailableValue: "12.5000", AvailableUnit: "USD", UsedValue: &usedValue, UsedUnit: &usedUnit,
		CapturedAt: 1_786_000_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if balance.AvailableValue != "12.5000" || balance.UsedValue == nil || *balance.UsedValue != "2.75" {
		t.Fatalf("balance = %+v", balance)
	}

	inputTokens, outputTokens := int64(100), int64(20)
	chargeValue, chargeUnit := "0.004200", "USD"
	record := SiteUsageRecordWrite{
		DedupKey: "remote:abc", RemoteID: "abc", RequestID: "req-1", OccurredAt: 1_786_000_000_001,
		Model: "claude-sonnet", Status: "succeeded", InputTokens: &inputTokens, OutputTokens: &outputTokens,
		ChargeValue: &chargeValue, ChargeUnit: &chargeUnit, APIKeyName: "primary", SourceFetchedAt: 1_786_000_000_100,
	}
	saved, err := storage.SaveSiteUsageRecords(ctx, siteID, "ciii", []SiteUsageRecordWrite{record, record}, record.SourceFetchedAt)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Inserted != 1 || saved.Deduplicated != 1 {
		t.Fatalf("saved = %+v", saved)
	}
	page, err := storage.ListSiteUsageRecords(ctx, siteID, SiteUsageListFilter{Limit: 50, Search: "req-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].ChargeValue == nil || *page.Records[0].ChargeValue != "0.004200" {
		t.Fatalf("usage page = %+v", page)
	}
	latest, err := storage.GetLatestSiteBalance(ctx, siteID)
	if err != nil || latest.ID != balance.ID {
		t.Fatalf("latest balance = %+v, %v", latest, err)
	}
	refreshed, err := storage.GetSiteAccountConnection(ctx, siteID)
	if err != nil || refreshed.LastBalanceRefreshAt == nil || refreshed.LastUsageRefreshAt == nil {
		t.Fatalf("refreshed connection = %+v, %v", refreshed, err)
	}
}

func TestSiteUsageStatusClassesIncludeAdapterVariants(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, storage, "Usage status source")
	_, err := storage.CreateSealedSiteAccountConnection(ctx, siteID, SealedSiteAccountConnectionInput{
		AdapterKind: "ciii", Origin: "https://usage-status.example", CipherVersion: 1, Enabled: true,
	}, func(int64, int64) ([]byte, error) { return []byte("sealed"), nil })
	if err != nil {
		t.Fatal(err)
	}
	records := []SiteUsageRecordWrite{
		{DedupKey: "success", Status: "success", OccurredAt: 1_786_000_000_004, SourceFetchedAt: 1_786_000_000_100},
		{DedupKey: "succeeded", Status: "succeeded", OccurredAt: 1_786_000_000_003, SourceFetchedAt: 1_786_000_000_100},
		{DedupKey: "failed", Status: "failed", OccurredAt: 1_786_000_000_002, SourceFetchedAt: 1_786_000_000_100},
		{DedupKey: "error", Status: "error", OccurredAt: 1_786_000_000_001, SourceFetchedAt: 1_786_000_000_100},
	}
	if _, err := storage.SaveSiteUsageRecords(ctx, siteID, "ciii", records, 1_786_000_000_100); err != nil {
		t.Fatal(err)
	}
	for status, expected := range map[string][]string{
		"success": {"success", "succeeded"},
		"failed":  {"failed", "error"},
	} {
		page, err := storage.ListSiteUsageRecords(ctx, siteID, SiteUsageListFilter{Limit: 10, Status: status})
		if err != nil {
			t.Fatalf("list %s status: %v", status, err)
		}
		if len(page.Records) != len(expected) {
			t.Fatalf("%s records = %+v", status, page.Records)
		}
		for index, record := range page.Records {
			if record.Status != expected[index] {
				t.Fatalf("%s record %d status = %q, want %q", status, index, record.Status, expected[index])
			}
		}
	}
}

func TestSiteAccountSessionCASAndDeleteCascade(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, storage, "Session source")
	connection, err := storage.CreateSealedSiteAccountConnection(ctx, siteID, SealedSiteAccountConnectionInput{
		AdapterKind: "ciii", Origin: "https://session.example", CipherVersion: 1, Enabled: true,
	}, func(int64, int64) ([]byte, error) { return []byte("sealed"), nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.PersistSiteAccountSession(ctx, siteID, connection.Revision, 1, []byte("rotated"), 1_786_000_000_000); err != nil {
		t.Fatal(err)
	}
	if err := storage.PersistSiteAccountSession(ctx, siteID, connection.Revision, 1, []byte("stale"), 1_786_000_000_001); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale session update = %v", err)
	}
	current, err := storage.GetSiteAccountConnection(ctx, siteID)
	if err != nil || current.Revision != connection.Revision+1 || current.LastSessionRefreshAt == nil {
		t.Fatalf("current = %+v, %v", current, err)
	}
	if err := storage.DeleteSiteAccountConnection(ctx, siteID, current.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.GetSiteAccountConnection(ctx, siteID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted connection error = %v", err)
	}
}
