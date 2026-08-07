package gateway

import (
	"bytes"
	"context"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestStoreSecretProviderDecryptsOnlyRecordBoundEndpointMaterial(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, t.TempDir()+"/vnext.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	siteID, err := storage.CreateSite(ctx, vnextstore.SiteWrite{Name: "Secrets", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := storage.CreateSiteEndpoint(ctx, siteID, vnextstore.SiteEndpointWrite{
		Name: "Chat", BaseURL: "https://secrets.example/v1",
		WireProtocol: string(protocol.OpenAI), Surface: string(protocol.OpenAIChatCompletions),
		AuthScheme: string(protocol.AuthBearer), HeaderTemplate: []byte(`{"X-Client":"JieShan"}`), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	credentialID, err := storage.CreateSealedSiteCredential(ctx, siteID, vnextstore.SealedSiteCredentialInput{
		Name: "Primary", CipherVersion: 1, Enabled: true,
	}, func(recordID, ownerID int64) ([]byte, error) {
		return box.Seal(secretbox.PurposeSiteCredential, secretbox.Identity{RecordID: recordID, OwnerID: ownerID}, []byte("sk-secret"))
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := storage.GetSiteEndpoint(ctx, endpointID)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.ReplaceEndpointCredentialBindings(ctx, siteID, endpointID, endpoint.Revision, []int64{credentialID}); err != nil {
		t.Fatal(err)
	}
	secretHeaders, err := box.Seal(secretbox.PurposeSiteSecretHeaders, secretbox.Identity{
		RecordID: endpointID, OwnerID: siteID,
	}, []byte(`{"CF-Access-Client-Secret":"cf-secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.DB.ExecContext(ctx, `UPDATE site_endpoints SET secret_headers_cipher=?,cipher_version=1 WHERE id=?`, secretHeaders, endpointID); err != nil {
		t.Fatal(err)
	}

	provider, err := NewStoreSecretProvider(storage, box)
	if err != nil {
		t.Fatal(err)
	}
	metadata := resolver.EndpointMetadata{
		SiteID: siteID, EndpointID: endpointID, Protocol: protocol.OpenAI,
		Surface: protocol.OpenAIChatCompletions, AuthScheme: protocol.AuthBearer,
		HeaderTemplate: []byte(`{"X-Client":"JieShan"}`),
	}
	material, err := provider.Materialize(ctx, metadata, routing.CredentialID(credentialID))
	if err != nil {
		t.Fatal(err)
	}
	if material.Credential != "sk-secret" || material.Headers.Get("X-Client") != "JieShan" ||
		material.Headers.Get("CF-Access-Client-Secret") != "cf-secret" {
		t.Fatalf("material = %+v", material)
	}

	otherID, err := storage.CreateSealedSiteCredential(ctx, siteID, vnextstore.SealedSiteCredentialInput{
		Name: "Other", CipherVersion: 1, Enabled: true,
	}, func(recordID, ownerID int64) ([]byte, error) {
		return box.Seal(secretbox.PurposeSiteCredential, secretbox.Identity{RecordID: recordID, OwnerID: ownerID}, []byte("other-secret"))
	})
	if err != nil {
		t.Fatal(err)
	}
	var transplanted []byte
	if err := storage.DB.QueryRowContext(ctx, `SELECT secret_cipher FROM site_credentials WHERE id=?`, otherID).Scan(&transplanted); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.DB.ExecContext(ctx, `UPDATE site_credentials SET secret_cipher=? WHERE id=?`, transplanted, credentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Materialize(ctx, metadata, routing.CredentialID(credentialID)); err == nil {
		t.Fatal("transplanted credential ciphertext decrypted")
	}
}
