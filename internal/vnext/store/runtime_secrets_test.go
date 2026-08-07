package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestCreateSealedSiteCredentialBindsAfterIDAllocationAndRollsBackOnFailure(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, storage, "Sealed credentials")

	credentialID, err := storage.CreateSealedSiteCredential(ctx, siteID, SealedSiteCredentialInput{
		Name: "Primary", CipherVersion: 1, Enabled: true,
	}, func(recordID, ownerID int64) ([]byte, error) {
		if recordID <= 0 || ownerID != siteID {
			t.Fatalf("seal identity = %d/%d", recordID, ownerID)
		}
		return []byte("record-bound-ciphertext"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := storage.DB.QueryRowContext(ctx, `SELECT secret_cipher FROM site_credentials WHERE id=?`, credentialID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) != "record-bound-ciphertext" {
		t.Fatalf("ciphertext = %q", ciphertext)
	}

	if _, err := storage.CreateSealedSiteCredential(ctx, siteID, SealedSiteCredentialInput{
		Name: "Rollback", CipherVersion: 1, Enabled: true,
	}, func(int64, int64) ([]byte, error) {
		return nil, errors.New("seal failed")
	}); err == nil {
		t.Fatal("expected sealing failure")
	}
	var count int
	if err := storage.DB.QueryRowContext(ctx, `SELECT count(*) FROM site_credentials WHERE name='Rollback'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed credential rows = %d", count)
	}
}

func TestLoadRuntimeSecretBundleRequiresTheExactEnabledBinding(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, storage, "Runtime bundle")
	endpointID := mustCreateEndpoint(t, storage, siteID, "Chat", "https://runtime.example/v1")
	credentialID, err := storage.CreateSealedSiteCredential(ctx, siteID, SealedSiteCredentialInput{
		Name: "Bound", CipherVersion: 1, Enabled: true,
	}, func(int64, int64) ([]byte, error) { return []byte("ciphertext"), nil })
	if err != nil {
		t.Fatal(err)
	}
	mustReplaceBindings(t, storage, siteID, endpointID, []int64{credentialID})

	bundle, err := storage.LoadRuntimeSecretBundle(ctx, siteID, endpointID, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.SiteID != siteID || bundle.EndpointID != endpointID || bundle.CredentialID != credentialID || string(bundle.CredentialCipher) != "ciphertext" {
		t.Fatalf("bundle = %+v", bundle)
	}

	if _, err := storage.DB.ExecContext(ctx, `UPDATE credential_endpoint_bindings SET enabled=0 WHERE endpoint_id=? AND credential_id=?`, endpointID, credentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.LoadRuntimeSecretBundle(ctx, siteID, endpointID, credentialID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled binding error = %v", err)
	}
}
