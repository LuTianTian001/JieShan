package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpstreamAccountCRUDKeepsManagementAuthSeparate(t *testing.T) {
	s, upstreamID := newUpstreamAccountTestStore(t)
	ctx := context.Background()

	accountID, err := s.CreateUpstreamAccount(ctx, UpstreamAccountWrite{
		UpstreamID:   upstreamID,
		AdapterKind:  " New_API ",
		APIOrigin:    "https://panel.example.com/",
		AuthCipher:   []byte("encrypted-pat"),
		Enabled:      true,
		Capabilities: json.RawMessage(`{ "account": "supported" }`),
	})
	if err != nil {
		t.Fatalf("CreateUpstreamAccount() error = %v", err)
	}
	if accountID == 0 {
		t.Fatal("CreateUpstreamAccount() returned zero id")
	}

	account, err := s.GetUpstreamAccount(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if account.AdapterKind != "new_api" || account.APIOrigin != "https://panel.example.com" || !account.AuthConfigured || !account.Enabled {
		t.Fatalf("created account = %+v", account)
	}
	if string(account.Capabilities) != `{"account":"supported"}` || account.SyncState != "pending" {
		t.Fatalf("created account capabilities/state = %s / %s", account.Capabilities, account.SyncState)
	}
	secret, err := s.GetUpstreamAccountSecret(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(secret.AuthCipher, []byte("encrypted-pat")) {
		t.Fatalf("auth cipher = %q", secret.AuthCipher)
	}
	if _, err := s.CreateUpstreamAccount(ctx, UpstreamAccountWrite{
		UpstreamID: upstreamID, AdapterKind: "one_api", APIOrigin: "https://other.example.com", AuthCipher: []byte("other"), Enabled: true,
	}); err == nil {
		t.Fatal("second account for the same upstream unexpectedly succeeded")
	}

	if err := s.UpdateUpstreamAccount(ctx, upstreamID, UpstreamAccountUpdate{
		AdapterKind: "one_api", APIOrigin: "https://panel.example.com/api/", Enabled: false,
	}); err != nil {
		t.Fatalf("UpdateUpstreamAccount() without auth replacement error = %v", err)
	}
	secret, err = s.GetUpstreamAccountSecret(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if secret.AdapterKind != "one_api" || secret.APIOrigin != "https://panel.example.com/api" || secret.Enabled {
		t.Fatalf("updated account = %+v", secret.UpstreamAccount)
	}
	if !bytes.Equal(secret.AuthCipher, []byte("encrypted-pat")) {
		t.Fatalf("auth changed without replacement: %q", secret.AuthCipher)
	}
	if string(secret.Capabilities) != `{"account":"supported"}` {
		t.Fatalf("capabilities changed without replacement: %s", secret.Capabilities)
	}

	if err := s.UpdateUpstreamAccount(ctx, upstreamID, UpstreamAccountUpdate{
		AdapterKind: "one_api", APIOrigin: "https://panel.example.com", AuthCipher: []byte("rotated"), ReplaceAuth: true,
		Enabled: true, Capabilities: json.RawMessage(`{"usage":"supported"}`),
	}); err != nil {
		t.Fatalf("UpdateUpstreamAccount() with auth replacement error = %v", err)
	}
	secret, err = s.GetUpstreamAccountSecret(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(secret.AuthCipher, []byte("rotated")) || string(secret.Capabilities) != `{"usage":"supported"}` {
		t.Fatalf("replacement update = %+v auth=%q", secret.UpstreamAccount, secret.AuthCipher)
	}
	accounts, err := s.ListUpstreamAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("ListUpstreamAccounts() = %+v, %v", accounts, err)
	}

	if err := s.DeleteUpstreamAccount(ctx, upstreamID); err != nil {
		t.Fatalf("DeleteUpstreamAccount() error = %v", err)
	}
	if _, err := s.GetUpstreamAccount(ctx, upstreamID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetUpstreamAccount() after delete error = %v", err)
	}
}

func TestUpdateSyncSuccessStoresSnapshotUsageAndRotatedAuthAtomically(t *testing.T) {
	s, upstreamID := newUpstreamAccountTestStore(t)
	ctx := context.Background()
	createTestUpstreamAccount(t, s, upstreamID, []byte("old-auth"))
	occurredFirst, occurredSecond := int64(900), int64(950)

	err := s.UpdateSyncSuccess(ctx, upstreamID, UpstreamAccountSyncSuccess{
		AttemptedAt:  1_000,
		SucceededAt:  1_100,
		SnapshotAt:   1_050,
		Capabilities: json.RawMessage(`{"account":"supported","usage":"supported"}`),
		Snapshot:     json.RawMessage(`{"balance":{"remaining":"12.3400","unit":"site_quota"}}`),
		Usage: []UpstreamAccountUsageWrite{
			{DedupeKey: "event-a", ExternalID: "a", ModelName: "gpt-test", Amount: "0.001234567890", Unit: "site_quota", Raw: json.RawMessage(`{"id":"a"}`), OccurredAt: &occurredFirst},
			{DedupeKey: "event-b", ModelName: "claude-test", Amount: "7", Unit: "credits", Raw: json.RawMessage(`{"id":"b"}`), OccurredAt: &occurredSecond},
		},
		RotatedAuthCipher: []byte("new-auth"),
	})
	if err != nil {
		t.Fatalf("UpdateSyncSuccess() error = %v", err)
	}

	account, err := s.GetUpstreamAccount(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if account.SyncState != "healthy" || account.LastAttemptAt == nil || *account.LastAttemptAt != 1_000 || account.LastSuccessAt == nil || *account.LastSuccessAt != 1_100 {
		t.Fatalf("successful account state = %+v", account)
	}
	if account.LastErrorCode != "" || account.LastErrorMessage != "" {
		t.Fatalf("successful account retained error: %+v", account)
	}
	secret, err := s.GetUpstreamAccountSecret(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(secret.AuthCipher, []byte("new-auth")) {
		t.Fatalf("rotated auth = %q", secret.AuthCipher)
	}
	snapshot, err := s.GetLatestUpstreamAccountSnapshot(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CapturedAt != 1_050 || string(snapshot.Snapshot) != `{"balance":{"remaining":"12.3400","unit":"site_quota"}}` {
		t.Fatalf("latest snapshot = %+v", snapshot)
	}
	usage, err := s.ListUsage(ctx, upstreamID, UpstreamAccountUsageQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 2 || usage[0].DedupeKey != "event-b" || usage[1].DedupeKey != "event-a" {
		t.Fatalf("usage = %+v", usage)
	}
	if usage[1].Amount != "0.001234567890" || usage[1].OccurredAt == nil || *usage[1].OccurredAt != occurredFirst {
		t.Fatalf("string amount or timestamp changed: %+v", usage[1])
	}
	page, err := s.ListUsage(ctx, upstreamID, UpstreamAccountUsageQuery{BeforeID: usage[0].ID, Limit: 1})
	if err != nil || len(page) != 1 || page[0].DedupeKey != "event-a" {
		t.Fatalf("usage cursor page = %+v, %v", page, err)
	}
	recent, err := s.ListUsage(ctx, upstreamID, UpstreamAccountUsageQuery{SinceAt: 925, Limit: 10})
	if err != nil || len(recent) != 1 || recent[0].DedupeKey != "event-b" {
		t.Fatalf("usage since filter = %+v, %v", recent, err)
	}

	if err := s.UpdateSyncSuccess(ctx, upstreamID, UpstreamAccountSyncSuccess{
		AttemptedAt: 1_200, SucceededAt: 1_250, SnapshotAt: 1_250,
		Snapshot: json.RawMessage(`{"balance":{"remaining":"11"}}`),
		Usage:    []UpstreamAccountUsageWrite{{DedupeKey: "event-a", Amount: "changed", Raw: json.RawMessage(`{"duplicate":true}`)}},
	}); err != nil {
		t.Fatalf("idempotent UpdateSyncSuccess() error = %v", err)
	}
	usage, err = s.ListUsage(ctx, upstreamID, UpstreamAccountUsageQuery{Limit: 10})
	if err != nil || len(usage) != 2 {
		t.Fatalf("deduplicated usage = %+v, %v", usage, err)
	}
	if usage[1].Amount != "changed" || usage[1].ModelName != "gpt-test" || usage[1].OccurredAt == nil || *usage[1].OccurredAt != occurredFirst || string(usage[1].Raw) != `{"duplicate":true}` {
		t.Fatalf("updated usage record = %+v", usage[1])
	}
	var snapshots int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM upstream_account_snapshots").Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 2 {
		t.Fatalf("snapshot count = %d, want 2", snapshots)
	}
}

func TestUpdateSyncFailurePreservesSuccessfulDataAndRedactsError(t *testing.T) {
	s, upstreamID := newUpstreamAccountTestStore(t)
	ctx := context.Background()
	createTestUpstreamAccount(t, s, upstreamID, []byte("account-auth"))
	if err := s.UpdateSyncSuccess(ctx, upstreamID, UpstreamAccountSyncSuccess{
		AttemptedAt: 100, SucceededAt: 110, Snapshot: json.RawMessage(`{"quota":"1000"}`),
	}); err != nil {
		t.Fatal(err)
	}

	message := `Get "https://panel.example.com/api/user/self?key=sk-secretabcdefgh": Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payloadpayload.signaturevalue`
	if err := s.UpdateSyncFailure(ctx, upstreamID, UpstreamAccountSyncFailure{
		AttemptedAt: 200, State: "auth_required", ErrorCode: "invalid_token", ErrorMessage: message,
		Capabilities:      json.RawMessage(`{"account":"supported","usage":"unsupported"}`),
		RotatedAuthCipher: []byte("refreshed-account-auth"),
	}); err != nil {
		t.Fatalf("UpdateSyncFailure() error = %v", err)
	}
	account, err := s.GetUpstreamAccount(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if account.SyncState != "auth_required" || account.LastAttemptAt == nil || *account.LastAttemptAt != 200 || account.LastSuccessAt == nil || *account.LastSuccessAt != 110 {
		t.Fatalf("failed account state = %+v", account)
	}
	if account.LastErrorCode != "invalid_token" || string(account.Capabilities) != `{"account":"supported","usage":"unsupported"}` {
		t.Fatalf("failed account metadata = %+v", account)
	}
	for _, secret := range []string{"sk-secret", "Bearer eyJ", "?key="} {
		if strings.Contains(account.LastErrorMessage, secret) {
			t.Fatalf("stored sync error contains %q: %s", secret, account.LastErrorMessage)
		}
	}
	snapshot, err := s.GetLatestUpstreamAccountSnapshot(ctx, upstreamID)
	if err != nil || string(snapshot.Snapshot) != `{"quota":"1000"}` {
		t.Fatalf("snapshot after failure = %+v, %v", snapshot, err)
	}
	secret, err := s.GetUpstreamAccountSecret(ctx, upstreamID)
	if err != nil || !bytes.Equal(secret.AuthCipher, []byte("refreshed-account-auth")) {
		t.Fatalf("auth after failure = %q, %v", secret.AuthCipher, err)
	}
}

func TestListUsageSinceFallsBackToSyncTime(t *testing.T) {
	s, upstreamID := newUpstreamAccountTestStore(t)
	ctx := context.Background()
	createTestUpstreamAccount(t, s, upstreamID, []byte("account-auth"))
	oldOccurredAt := int64(100)
	if err := s.UpdateSyncSuccess(ctx, upstreamID, UpstreamAccountSyncSuccess{
		SucceededAt: 1_000,
		Snapshot:    json.RawMessage(`{}`),
		Usage: []UpstreamAccountUsageWrite{
			{DedupeKey: "missing-time", Raw: json.RawMessage(`{}`), SyncedAt: 900},
			{DedupeKey: "old-occurred", Raw: json.RawMessage(`{}`), OccurredAt: &oldOccurredAt, SyncedAt: 950},
		},
	}); err != nil {
		t.Fatal(err)
	}

	items, err := s.ListUsage(ctx, upstreamID, UpstreamAccountUsageQuery{SinceAt: 800, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].DedupeKey != "missing-time" {
		t.Fatalf("usage since fallback = %+v", items)
	}
}

func TestDeleteOldAccountDataKeepsLatestSnapshotPerAccount(t *testing.T) {
	s, firstUpstreamID := newUpstreamAccountTestStore(t)
	ctx := context.Background()
	firstAccountID := createTestUpstreamAccount(t, s, firstUpstreamID, []byte("first-auth"))
	secondUpstreamID, err := s.CreateUpstream(ctx, UpstreamWrite{
		Name: "second-account-test", Kind: "compatible", BaseURL: "https://second.example.com", Enabled: true,
		CustomHeaders: json.RawMessage(`{}`), SecretCipher: []byte("inference-cipher"),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondAccountID := createTestUpstreamAccount(t, s, secondUpstreamID, []byte("second-auth"))

	insertSnapshot := func(accountID, capturedAt int64, marker string) {
		t.Helper()
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_account_snapshots(upstream_account_id,snapshot_json,captured_at)
VALUES (?,?,?)`, accountID, `{"marker":"`+marker+`"}`, capturedAt); err != nil {
			t.Fatal(err)
		}
	}
	insertUsage := func(accountID int64, key string, occurredAt *int64, syncedAt int64) {
		t.Helper()
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_account_usage_records(
upstream_account_id,dedupe_key,raw_json,occurred_at,synced_at) VALUES (?,?,?,?,?)`,
			accountID, key, `{}`, accountNullableInt64(occurredAt), syncedAt); err != nil {
			t.Fatal(err)
		}
	}
	assertCount := func(query string, want int, args ...any) {
		t.Helper()
		var got int
		if err := s.DB.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("row count = %d, want %d for %q", got, want, query)
		}
	}

	insertSnapshot(firstAccountID, 100, "first-old")
	insertSnapshot(firstAccountID, 200, "first-latest-old")
	insertSnapshot(secondAccountID, 100, "second-old")
	insertSnapshot(secondAccountID, 1_000, "second-new")
	oldOccurredAt, newOccurredAt := int64(100), int64(1_000)
	insertUsage(firstAccountID, "old-occurred", &oldOccurredAt, 1_000)
	insertUsage(firstAccountID, "old-synced", nil, 100)
	insertUsage(firstAccountID, "new-occurred", &newOccurredAt, 100)
	insertUsage(firstAccountID, "new-synced", nil, 1_000)

	if err := s.DeleteOldAccountData(ctx, 500); err != nil {
		t.Fatalf("DeleteOldAccountData() error = %v", err)
	}
	assertCount("SELECT COUNT(*) FROM upstream_account_snapshots", 2)
	assertCount("SELECT COUNT(*) FROM upstream_account_snapshots WHERE upstream_account_id=? AND captured_at=200", 1, firstAccountID)
	assertCount("SELECT COUNT(*) FROM upstream_account_snapshots WHERE upstream_account_id=? AND captured_at=1000", 1, secondAccountID)
	assertCount("SELECT COUNT(*) FROM upstream_account_usage_records", 2)
	assertCount("SELECT COUNT(*) FROM upstream_account_usage_records WHERE dedupe_key IN ('new-occurred','new-synced')", 2)
}

func TestUpdateSyncSuccessRollsBackAllWritesOnUsageFailure(t *testing.T) {
	s, upstreamID := newUpstreamAccountTestStore(t)
	ctx := context.Background()
	createTestUpstreamAccount(t, s, upstreamID, []byte("old-auth"))
	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER fail_account_usage BEFORE INSERT ON upstream_account_usage_records
WHEN NEW.dedupe_key='force-failure'
BEGIN
  SELECT RAISE(ABORT, 'forced usage failure');
END`); err != nil {
		t.Fatal(err)
	}

	err := s.UpdateSyncSuccess(ctx, upstreamID, UpstreamAccountSyncSuccess{
		AttemptedAt: 10, SucceededAt: 20,
		Snapshot:          json.RawMessage(`{"balance":"9"}`),
		RotatedAuthCipher: []byte("new-auth"),
		Usage:             []UpstreamAccountUsageWrite{{DedupeKey: "force-failure", Raw: json.RawMessage(`{}`)}},
	})
	if err == nil {
		t.Fatal("UpdateSyncSuccess() unexpectedly succeeded")
	}
	account, err := s.GetUpstreamAccount(ctx, upstreamID)
	if err != nil {
		t.Fatal(err)
	}
	if account.SyncState != "pending" || account.LastAttemptAt != nil || account.LastSuccessAt != nil {
		t.Fatalf("account changed after rollback: %+v", account)
	}
	secret, err := s.GetUpstreamAccountSecret(ctx, upstreamID)
	if err != nil || !bytes.Equal(secret.AuthCipher, []byte("old-auth")) {
		t.Fatalf("auth changed after rollback: %q, %v", secret.AuthCipher, err)
	}
	assertRowCount(t, s, "SELECT COUNT(*) FROM upstream_account_snapshots", 0)
	assertRowCount(t, s, "SELECT COUNT(*) FROM upstream_account_usage_records", 0)
}

func newUpstreamAccountTestStore(t *testing.T) (*Store, int64) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	upstreamID, err := s.CreateUpstream(ctx, UpstreamWrite{
		Name: "account-test", Kind: "compatible", BaseURL: "https://api.example.com", Enabled: true,
		CustomHeaders: json.RawMessage(`{}`), SecretCipher: []byte("inference-cipher"),
	})
	if err != nil {
		t.Fatalf("CreateUpstream() error = %v", err)
	}
	return s, upstreamID
}

func createTestUpstreamAccount(t *testing.T, s *Store, upstreamID int64, auth []byte) int64 {
	t.Helper()
	id, err := s.CreateUpstreamAccount(context.Background(), UpstreamAccountWrite{
		UpstreamID: upstreamID, AdapterKind: "new_api", APIOrigin: "https://panel.example.com", AuthCipher: auth, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateUpstreamAccount() error = %v", err)
	}
	return id
}
