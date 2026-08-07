package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestCredentialRuntimeTransitionsAreCASAndModelIndependent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, s, "Runtime upstream")
	endpointID := mustCreateEndpoint(t, s, siteID, "Runtime endpoint", "https://runtime.example/v1")
	credentialID := mustCreateCredential(t, s, siteID, "Runtime credential")
	mustReplaceBindings(t, s, siteID, endpointID, []int64{credentialID})
	targetID := mustCreateProviderTarget(t, s, siteID, endpointID, "runtime-model")
	forbiddenStatus := 403
	checkedAt := NowMS()
	if err := s.UpsertCredentialTargetAccess(ctx, CredentialTargetAccessWrite{
		SiteID: siteID, EndpointID: endpointID, CredentialID: credentialID,
		ProviderModelTargetID: targetID, Availability: "forbidden", LastHTTPStatus: &forbiddenStatus,
		LastErrorCode: "model.forbidden", LastCheckedAt: &checkedAt,
	}); err != nil {
		t.Fatal(err)
	}

	initial, err := s.GetCredentialRuntimeState(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if initial.State != "active" || initial.Revision != 1 || initial.CoolingUntil != nil ||
		initial.LastHTTPStatus != nil || initial.LastErrorCode != "" {
		t.Fatalf("initial runtime state = %+v", initial)
	}

	unauthorized := 401
	invalid, err := s.UpdateCredentialRuntimeState(ctx, CredentialRuntimeUpdate{
		CredentialID: credentialID, ExpectedRevision: initial.Revision, State: "invalid",
		LastHTTPStatus: &unauthorized, LastErrorCode: "credential.unauthorized", UpdatedAt: initial.UpdatedAt + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invalid.State != "invalid" || invalid.Revision != 2 || invalid.LastHTTPStatus == nil ||
		*invalid.LastHTTPStatus != 401 || invalid.CoolingUntil != nil {
		t.Fatalf("401 transition = %+v", invalid)
	}

	success := 200
	if _, err := s.UpdateCredentialRuntimeState(ctx, CredentialRuntimeUpdate{
		CredentialID: credentialID, ExpectedRevision: initial.Revision, State: "active",
		LastHTTPStatus: &success, UpdatedAt: invalid.UpdatedAt + 1,
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale credential runtime revision = %v", err)
	}
	afterStale, err := s.GetCredentialRuntimeState(ctx, credentialID)
	if err != nil || afterStale.Revision != invalid.Revision || afterStale.State != invalid.State {
		t.Fatalf("stale update changed state = %+v, %v", afterStale, err)
	}

	paymentRequired := 402
	exhausted, err := s.UpdateCredentialRuntimeState(ctx, CredentialRuntimeUpdate{
		CredentialID: credentialID, ExpectedRevision: invalid.Revision, State: "exhausted",
		LastHTTPStatus: &paymentRequired, LastErrorCode: "credential.exhausted", UpdatedAt: invalid.UpdatedAt + 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exhausted.State != "exhausted" || exhausted.Revision != 3 || exhausted.LastHTTPStatus == nil ||
		*exhausted.LastHTTPStatus != 402 || exhausted.CoolingUntil != nil {
		t.Fatalf("402 transition = %+v", exhausted)
	}

	rateLimited := 429
	coolingUntil := exhausted.UpdatedAt + 300_000
	cooling, err := s.UpdateCredentialRuntimeState(ctx, CredentialRuntimeUpdate{
		CredentialID: credentialID, ExpectedRevision: exhausted.Revision, State: "cooling",
		CoolingUntil: &coolingUntil, LastHTTPStatus: &rateLimited, LastErrorCode: "credential.rate_limited",
		UpdatedAt: exhausted.UpdatedAt + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cooling.State != "cooling" || cooling.Revision != 4 || cooling.CoolingUntil == nil ||
		*cooling.CoolingUntil != coolingUntil || cooling.LastHTTPStatus == nil || *cooling.LastHTTPStatus != 429 {
		t.Fatalf("429 transition = %+v", cooling)
	}

	credential, err := s.GetSiteCredential(ctx, credentialID)
	if err != nil || !credential.Enabled || credential.Revision != 1 {
		t.Fatalf("runtime update changed configured credential = %+v, %v", credential, err)
	}
	var availability string
	var modelStatus int
	var modelError string
	if err := s.DB.QueryRowContext(ctx, `SELECT availability,last_http_status,last_error_code
FROM credential_target_access WHERE credential_id=? AND provider_model_target_id=?`, credentialID, targetID).
		Scan(&availability, &modelStatus, &modelError); err != nil {
		t.Fatal(err)
	}
	if availability != "forbidden" || modelStatus != 403 || modelError != "model.forbidden" {
		t.Fatalf("global runtime update changed model access: %q/%d/%q", availability, modelStatus, modelError)
	}
	states, err := s.ListCredentialRuntimeStates(ctx, []int64{credentialID, credentialID})
	if err != nil || len(states) != 1 || states[credentialID].Revision != cooling.Revision {
		t.Fatalf("runtime state list = %+v, %v", states, err)
	}
}

func TestCredentialRuntimeCoolingValidation(t *testing.T) {
	s := newTestStore(t)
	siteID := mustCreateSite(t, s, "Runtime validation")
	credentialID := mustCreateCredential(t, s, siteID, "Validation credential")
	initial, err := s.GetCredentialRuntimeState(context.Background(), credentialID)
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := initial.UpdatedAt + 10
	rateLimited := 429
	serverError := 500
	unauthorized := 401
	future := updatedAt + 60_000
	now := updatedAt

	tests := map[string]CredentialRuntimeUpdate{
		"cooling deadline is not future": {
			CredentialID: credentialID, ExpectedRevision: initial.Revision, State: "cooling",
			CoolingUntil: &now, LastHTTPStatus: &rateLimited, UpdatedAt: updatedAt,
		},
		"cooling requires 429": {
			CredentialID: credentialID, ExpectedRevision: initial.Revision, State: "cooling",
			CoolingUntil: &future, LastHTTPStatus: &serverError, UpdatedAt: updatedAt,
		},
		"invalid cannot cool": {
			CredentialID: credentialID, ExpectedRevision: initial.Revision, State: "invalid",
			CoolingUntil: &future, LastHTTPStatus: &unauthorized, UpdatedAt: updatedAt,
		},
		"active cannot retain failure status": {
			CredentialID: credentialID, ExpectedRevision: initial.Revision, State: "active",
			LastHTTPStatus: &unauthorized, UpdatedAt: updatedAt,
		},
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := s.UpdateCredentialRuntimeState(context.Background(), input); err == nil {
				t.Fatal("invalid runtime transition unexpectedly succeeded")
			}
		})
	}
	after, err := s.GetCredentialRuntimeState(context.Background(), credentialID)
	if err != nil || after != initial {
		t.Fatalf("validation failures changed state: before=%+v after=%+v err=%v", initial, after, err)
	}
}

func TestCredentialCreationRollsBackWhenRuntimeRowFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, s, "Runtime rollback")
	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER fail_runtime_insert
BEFORE INSERT ON credential_runtime_state
BEGIN SELECT RAISE(ABORT,'forced runtime insert failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSiteCredential(ctx, siteID, SiteCredentialWrite{
		Name: "Rolled back credential", SecretCipher: []byte("encrypted"), CipherVersion: 1, Enabled: true,
	}); err == nil {
		t.Fatal("credential creation unexpectedly survived runtime-row failure")
	}
	var credentialCount int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_credentials WHERE site_id=?`, siteID).Scan(&credentialCount); err != nil {
		t.Fatal(err)
	}
	if credentialCount != 0 {
		t.Fatalf("rolled-back site credential count = %d", credentialCount)
	}
	assertTableCount(t, s, "credential_runtime_state", 0)
}

func TestCredentialRuntimeMigrationBackfillsExistingCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vnext-v4.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at INTEGER NOT NULL
)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	for _, item := range migrations[:4] {
		tx, err := db.Begin()
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if _, err := tx.Exec(item.sql); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			t.Fatalf("apply migration %d: %v", item.version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,name,applied_at) VALUES (?,?,?)`,
			item.version, item.name, int64(item.version)); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	result, err := db.Exec(`INSERT INTO sites(name,dashboard_url,enabled,revision,created_at,updated_at)
VALUES ('Existing site',NULL,1,1,10,10)`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	siteID, err := result.LastInsertId()
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	result, err = db.Exec(`INSERT INTO site_credentials(
site_id,name,secret_cipher,cipher_version,enabled,revision,created_at,updated_at)
VALUES (?,?,?,?,1,1,20,25)`, siteID, "Existing credential", []byte("encrypted"), 1)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	credentialID, err := result.LastInsertId()
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, err := s.GetCredentialRuntimeState(context.Background(), credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "active" || state.Revision != 1 || state.UpdatedAt != 25 || state.CoolingUntil != nil ||
		state.LastHTTPStatus != nil || state.LastErrorCode != "" {
		t.Fatalf("backfilled runtime state = %+v", state)
	}
}
