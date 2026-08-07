package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestImportSealedEndpointCredentialsIsAtomicAndReusesExactEndpoint(t *testing.T) {
	storage := newTestStore(t)
	siteID := mustCreateSite(t, storage, "Token import")
	endpoint := SiteEndpointWrite{
		Name: "Imported OpenAI", BaseURL: "https://relay.example/v1", WireProtocol: "openai",
		Surface: "openai.chat_completions", AdapterKind: "openai", AuthScheme: "bearer",
		HeaderTemplate: []byte(`{}`), Enabled: true,
	}
	inputs := []SealedEndpointCredentialImport{
		{Endpoint: endpoint, Credential: SealedSiteCredentialInput{Name: "alpha", CipherVersion: 1, Enabled: true}},
		{Endpoint: endpoint, Credential: SealedSiteCredentialInput{Name: "beta", CipherVersion: 1, Enabled: true}},
	}
	results, err := storage.ImportSealedEndpointCredentials(
		context.Background(), siteID, inputs,
		func(index int, credentialID, ownerSiteID int64) ([]byte, error) {
			if ownerSiteID != siteID || credentialID <= 0 {
				t.Fatalf("sealer identity index=%d credential=%d site=%d", index, credentialID, ownerSiteID)
			}
			return []byte(fmt.Sprintf("cipher-%d-%d", index, credentialID)), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].EndpointID != results[1].EndpointID ||
		results[0].CredentialID == results[1].CredentialID {
		t.Fatalf("import results = %#v", results)
	}

	var endpointCount, credentialCount, bindingCount int
	if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM site_endpoints WHERE site_id=?`, siteID).Scan(&endpointCount); err != nil {
		t.Fatal(err)
	}
	if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM site_credentials WHERE site_id=?`, siteID).Scan(&credentialCount); err != nil {
		t.Fatal(err)
	}
	if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM credential_endpoint_bindings WHERE site_id=?`, siteID).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if endpointCount != 1 || credentialCount != 2 || bindingCount != 2 {
		t.Fatalf("counts endpoint=%d credential=%d binding=%d", endpointCount, credentialCount, bindingCount)
	}
	bindings, err := storage.ListEndpointCredentialBindings(context.Background(), results[0].EndpointID)
	if err != nil {
		t.Fatal(err)
	}
	if got := bindingCredentialIDs(bindings); !reflect.DeepEqual(got, []int64{results[0].CredentialID, results[1].CredentialID}) {
		t.Fatalf("binding credential IDs = %v", got)
	}
}

func TestImportSealedEndpointCredentialsRollsBackEveryRowWhenSealingFails(t *testing.T) {
	storage := newTestStore(t)
	siteID := mustCreateSite(t, storage, "Rollback import")
	inputs := []SealedEndpointCredentialImport{
		{
			Endpoint: SiteEndpointWrite{
				Name: "OpenAI", BaseURL: "https://one.example/v1", WireProtocol: "openai",
				Surface: "openai.chat_completions", AdapterKind: "openai", AuthScheme: "bearer", Enabled: true,
			},
			Credential: SealedSiteCredentialInput{Name: "one", CipherVersion: 1, Enabled: true},
		},
		{
			Endpoint: SiteEndpointWrite{
				Name: "Anthropic", BaseURL: "https://two.example/v1", WireProtocol: "anthropic",
				Surface: "anthropic.messages", AdapterKind: "anthropic", AuthScheme: "x-api-key", Enabled: true,
			},
			Credential: SealedSiteCredentialInput{Name: "two", CipherVersion: 1, Enabled: true},
		},
	}
	sealFailure := errors.New("forced seal failure")
	_, err := storage.ImportSealedEndpointCredentials(
		context.Background(), siteID, inputs,
		func(index int, _, _ int64) ([]byte, error) {
			if index == 1 {
				return nil, sealFailure
			}
			return []byte("ciphertext"), nil
		},
	)
	if !errors.Is(err, sealFailure) {
		t.Fatalf("import error = %v", err)
	}
	for _, table := range []string{"site_endpoints", "site_credentials", "credential_endpoint_bindings"} {
		var count int
		if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE site_id=?`, siteID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows after rollback = %d", table, count)
		}
	}
}

func TestImportSealedEndpointCredentialsRollsBackCredentialWhenEndpointConflicts(t *testing.T) {
	storage := newTestStore(t)
	siteID := mustCreateSite(t, storage, "Endpoint conflict import")
	endpointID, err := storage.CreateSiteEndpoint(context.Background(), siteID, SiteEndpointWrite{
		Name: "Existing OpenAI", BaseURL: "https://relay.example/v1", WireProtocol: "openai",
		Surface: "openai.chat_completions", AdapterKind: "openai", AuthScheme: "bearer", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = storage.ImportSealedEndpointCredentials(
		context.Background(),
		siteID,
		[]SealedEndpointCredentialImport{{
			Endpoint: SiteEndpointWrite{
				Name: "Conflicting OpenAI", BaseURL: "https://relay.example/v1", WireProtocol: "openai",
				Surface: "openai.chat_completions", AdapterKind: "openai", AuthScheme: "x-api-key", Enabled: true,
			},
			Credential: SealedSiteCredentialInput{Name: "must-rollback", CipherVersion: 1, Enabled: true},
		}},
		func(_ int, _, _ int64) ([]byte, error) { return []byte("ciphertext"), nil },
	)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting endpoint import error = %v", err)
	}
	var credentialCount, bindingCount, endpointCount int
	if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM site_credentials WHERE site_id=?`, siteID).Scan(&credentialCount); err != nil {
		t.Fatal(err)
	}
	if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM credential_endpoint_bindings WHERE site_id=?`, siteID).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM site_endpoints WHERE site_id=?`, siteID).Scan(&endpointCount); err != nil {
		t.Fatal(err)
	}
	if credentialCount != 0 || bindingCount != 0 || endpointCount != 1 {
		t.Fatalf("rows after conflict: credentials=%d bindings=%d endpoints=%d", credentialCount, bindingCount, endpointCount)
	}
	endpoint, err := storage.GetSiteEndpoint(context.Background(), endpointID)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Revision != 1 {
		t.Fatalf("existing endpoint revision after rollback = %d", endpoint.Revision)
	}
}
