package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
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

func TestUpstreamBalanceAndUsageRemainDisplayOnlyForDownstreamQuota(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	quota := int64(1_000)
	fixture := createAccountingFixture(t, storage, &quota)
	if _, err := storage.CreateSealedSiteAccountConnection(ctx, fixture.siteID, SealedSiteAccountConnectionInput{
		AdapterKind: "ciii", Origin: "https://display-only.example", CipherVersion: 1, Enabled: true,
	}, func(int64, int64) ([]byte, error) { return []byte("sealed"), nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.SaveSiteBalanceSnapshot(ctx, fixture.siteID, "ciii", SiteBalanceSnapshotWrite{
		AvailableValue: "999.50", AvailableUnit: "USD", CapturedAt: 1_786_000_000_000,
	}); err != nil {
		t.Fatal(err)
	}
	chargeValue, chargeUnit := "500.00", "USD"
	if _, err := storage.SaveSiteUsageRecords(ctx, fixture.siteID, "ciii", []SiteUsageRecordWrite{{
		DedupKey: "remote:display-only", RemoteID: "display-only", OccurredAt: 1_786_000_000_001,
		ChargeValue: &chargeValue, ChargeUnit: &chargeUnit, SourceFetchedAt: 1_786_000_000_100,
	}}, 1_786_000_000_100); err != nil {
		t.Fatal(err)
	}

	key, err := storage.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedNanoUSD != 0 || key.ReservedNanoUSD != 0 || key.UsedThisHourNanoUSD != 0 || key.ReservedThisHourNanoUSD != 0 {
		t.Fatalf("upstream display data changed downstream accounting: %+v", key)
	}
	assertTableCount(t, storage, "quota_ledger", 0)
}

func TestSiteUsageSyncWindowProgressIsDurableAndAtomic(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage-sync.db")
	storage, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	siteID := mustCreateSite(t, storage, "Durable usage source")
	if _, err := storage.CreateSealedSiteAccountConnection(ctx, siteID, SealedSiteAccountConnectionInput{
		AdapterKind: "ciii", Origin: "https://durable-usage.example", CipherVersion: 1, Enabled: true,
	}, func(int64, int64) ([]byte, error) { return []byte("sealed"), nil }); err != nil {
		t.Fatal(err)
	}
	throughAt := int64(1_786_000_000_000)
	planned, err := storage.PlanSiteUsageSyncWindow(ctx, siteID, throughAt, 24*60*60*1000, 60*1000)
	if err != nil || !planned {
		t.Fatalf("plan usage window = %v, %v", planned, err)
	}
	window, ok, err := storage.NextSiteUsageSyncWindow(ctx, siteID)
	if err != nil || !ok || window.FromAt != throughAt-24*60*60*1000 || window.ToAt != throughAt || window.Cursor != "" {
		t.Fatalf("planned usage window = %+v, %v, %v", window, ok, err)
	}
	chargeValue, chargeUnit := "0.004200", "credits"
	first := SiteUsageRecordWrite{
		DedupKey: "remote:first", RemoteID: "first", OccurredAt: throughAt - 1,
		ChargeValue: &chargeValue, ChargeUnit: &chargeUnit,
	}
	if saved, err := storage.SaveSiteUsageWindowPage(ctx, siteID, "ciii", []SiteUsageRecordWrite{first},
		throughAt+100, SiteUsageSyncProgress{WindowID: window.ID, NextCursor: "2", HasMore: true}); err != nil || saved.Inserted != 1 {
		t.Fatalf("save first usage page = %+v, %v", saved, err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}

	storage, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	window, ok, err = storage.NextSiteUsageSyncWindow(ctx, siteID)
	if err != nil || !ok || window.Cursor != "2" {
		t.Fatalf("reopened usage window = %+v, %v, %v", window, ok, err)
	}
	stale := SiteUsageRecordWrite{DedupKey: "remote:stale", RemoteID: "stale", OccurredAt: throughAt - 2}
	_, err = storage.SaveSiteUsageWindowPage(ctx, siteID, "ciii", []SiteUsageRecordWrite{stale}, throughAt+200,
		SiteUsageSyncProgress{WindowID: window.ID, ExpectedCursor: "", NextCursor: "2", HasMore: true})
	if !errors.Is(err, ErrSiteUsageSyncConflict) {
		t.Fatalf("stale usage page error = %v", err)
	}
	page, err := storage.ListSiteUsageRecords(ctx, siteID, SiteUsageListFilter{Limit: 10})
	if err != nil || len(page.Records) != 1 || page.Records[0].RemoteID != "first" ||
		page.Records[0].ChargeValue == nil || *page.Records[0].ChargeValue != chargeValue ||
		page.Records[0].ChargeUnit == nil || *page.Records[0].ChargeUnit != chargeUnit {
		t.Fatalf("records after stale page = %+v, %v", page.Records, err)
	}
	second := SiteUsageRecordWrite{DedupKey: "remote:second", RemoteID: "second", OccurredAt: throughAt - 3}
	if saved, err := storage.SaveSiteUsageWindowPage(ctx, siteID, "ciii", []SiteUsageRecordWrite{second}, throughAt+300,
		SiteUsageSyncProgress{WindowID: window.ID, ExpectedCursor: "2"}); err != nil || saved.Inserted != 1 {
		t.Fatalf("complete usage window = %+v, %v", saved, err)
	}
	if _, ok, err := storage.NextSiteUsageSyncWindow(ctx, siteID); err != nil || ok {
		t.Fatalf("completed usage window still pending: %v, %v", ok, err)
	}
	states, err := storage.ListSiteUsageSyncStates(ctx)
	if err != nil || len(states) != 1 || states[0].ThroughAt == nil || *states[0].ThroughAt != throughAt || states[0].HasPending {
		t.Fatalf("usage sync states = %+v, %v", states, err)
	}
	newThroughAt := throughAt + 5*60*1000
	if planned, err := storage.PlanSiteUsageSyncWindow(ctx, siteID, newThroughAt, 24*60*60*1000, 60*1000); err != nil || !planned {
		t.Fatalf("plan incremental window = %v, %v", planned, err)
	}
	incremental, ok, err := storage.NextSiteUsageSyncWindow(ctx, siteID)
	if err != nil || !ok || incremental.FromAt != throughAt-60*1000 || incremental.ToAt != newThroughAt {
		t.Fatalf("incremental usage window = %+v, %v, %v", incremental, ok, err)
	}
}

func TestSiteAccountSourceChangeResetsUsageSyncState(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, storage, "Changed usage source")
	connection, err := storage.CreateSealedSiteAccountConnection(ctx, siteID, SealedSiteAccountConnectionInput{
		AdapterKind: "ciii", Origin: "https://old-source.example", CipherVersion: 1, Enabled: true,
	}, func(int64, int64) ([]byte, error) { return []byte("sealed"), nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.PlanSiteUsageSyncWindow(ctx, siteID, 1_786_000_000_000, 60_000, 1_000); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.UpdateSiteAccountConnection(ctx, siteID, SiteAccountConnectionUpdate{
		ExpectedRevision: connection.Revision, AdapterKind: "ciii", Origin: "https://new-source.example", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := storage.NextSiteUsageSyncWindow(ctx, siteID); err != nil || ok {
		t.Fatalf("old source window survived update: %v, %v", ok, err)
	}
	states, err := storage.ListSiteUsageSyncStates(ctx)
	if err != nil || len(states) != 1 || states[0].ThroughAt != nil || states[0].HasPending {
		t.Fatalf("reset usage sync state = %+v, %v", states, err)
	}
}

func TestSiteUsageSyncPlanningRecoversFromLegacyRefreshWatermark(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, storage, "Legacy usage watermark")
	if _, err := storage.CreateSealedSiteAccountConnection(ctx, siteID, SealedSiteAccountConnectionInput{
		AdapterKind: "ciii", Origin: "https://legacy-usage.example", CipherVersion: 1, Enabled: true,
	}, func(int64, int64) ([]byte, error) { return []byte("sealed"), nil }); err != nil {
		t.Fatal(err)
	}
	legacyRefresh := int64(1_785_000_000_000)
	if _, err := storage.DB.ExecContext(ctx, `UPDATE site_account_connections SET last_usage_refresh_at=? WHERE site_id=?`,
		legacyRefresh, siteID); err != nil {
		t.Fatal(err)
	}
	throughAt := legacyRefresh + 48*60*60*1000
	if planned, err := storage.PlanSiteUsageSyncWindow(ctx, siteID, throughAt, 24*60*60*1000, 60*1000); err != nil || !planned {
		t.Fatalf("plan legacy recovery window = %v, %v", planned, err)
	}
	window, ok, err := storage.NextSiteUsageSyncWindow(ctx, siteID)
	if err != nil || !ok || window.FromAt != legacyRefresh-60*1000 || window.ToAt != throughAt {
		t.Fatalf("legacy recovery window = %+v, %v, %v", window, ok, err)
	}
}

func TestDuplicateSiteUsageRefreshEnrichesButNeverRegressesARecord(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, storage, "Usage reconciliation source")
	if _, err := storage.CreateSealedSiteAccountConnection(ctx, siteID, SealedSiteAccountConnectionInput{
		AdapterKind: "ciii", Origin: "https://usage-refresh.example", CipherVersion: 1, Enabled: true,
	}, func(int64, int64) ([]byte, error) { return []byte("sealed"), nil }); err != nil {
		t.Fatal(err)
	}
	initialFetchedAt := int64(1_786_000_000_100)
	initialInput := int64(10)
	initial := SiteUsageRecordWrite{
		DedupKey: "remote:refresh-1", RemoteID: "refresh-1", OccurredAt: 1_786_000_000_000,
		Model: "model-a", Status: "processing", InputTokens: &initialInput, SourceFetchedAt: initialFetchedAt,
	}
	if saved, err := storage.SaveSiteUsageRecords(ctx, siteID, "ciii", []SiteUsageRecordWrite{initial}, initialFetchedAt); err != nil || saved.Inserted != 1 {
		t.Fatalf("initial save = %+v, %v", saved, err)
	}

	output, total, duration := int64(4), int64(14), int64(900)
	chargeValue, chargeUnit := "0.0014", "USD"
	enriched := initial
	enriched.Status = "succeeded"
	enriched.OutputTokens = &output
	enriched.TotalTokens = &total
	enriched.DurationMS = &duration
	enriched.ChargeValue = &chargeValue
	enriched.ChargeUnit = &chargeUnit
	enriched.SourceFetchedAt = initialFetchedAt + 100
	if saved, err := storage.SaveSiteUsageRecords(ctx, siteID, "ciii", []SiteUsageRecordWrite{enriched}, enriched.SourceFetchedAt); err != nil || saved.Deduplicated != 1 {
		t.Fatalf("enriched save = %+v, %v", saved, err)
	}

	older := initial
	older.Model = "stale-model"
	older.Status = "processing"
	older.SourceFetchedAt = initialFetchedAt - 1
	if _, err := storage.SaveSiteUsageRecords(ctx, siteID, "ciii", []SiteUsageRecordWrite{older}, older.SourceFetchedAt); err != nil {
		t.Fatal(err)
	}
	page, err := storage.ListSiteUsageRecords(ctx, siteID, SiteUsageListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].Status != "succeeded" || page.Records[0].Model != "model-a" ||
		page.Records[0].OutputTokens == nil || *page.Records[0].OutputTokens != output ||
		page.Records[0].ChargeValue == nil || *page.Records[0].ChargeValue != chargeValue ||
		page.Records[0].SourceFetchedAt != enriched.SourceFetchedAt {
		t.Fatalf("reconciled usage record = %+v", page.Records)
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
