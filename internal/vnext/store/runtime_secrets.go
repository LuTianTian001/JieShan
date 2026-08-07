package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type SealedSiteCredentialInput struct {
	Name          string
	CipherVersion int64
	Enabled       bool
}

type RuntimeSecretBundle struct {
	SiteID                     int64
	EndpointID                 int64
	CredentialID               int64
	CredentialCipher           []byte
	CredentialCipherVersion    int64
	SecretHeadersCipher        []byte
	SecretHeadersCipherVersion int64
}

// CreateSealedSiteCredential allocates the credential ID before encryption so
// authenticated encryption can bind the ciphertext to both record and site.
// The placeholder never commits if sealing fails.
func (s *Store) CreateSealedSiteCredential(
	ctx context.Context,
	siteID int64,
	input SealedSiteCredentialInput,
	seal func(credentialID, siteID int64) ([]byte, error),
) (int64, error) {
	input.Name = strings.TrimSpace(input.Name)
	if siteID <= 0 || input.Name == "" || input.CipherVersion <= 0 || seal == nil {
		return 0, errors.New("site, credential name, cipher version, and sealer are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := NowMS()
	result, err := tx.ExecContext(ctx, `INSERT INTO site_credentials(
site_id,name,secret_cipher,cipher_version,enabled,revision,created_at,updated_at)
VALUES (?,?,X'00',?,?,1,?,?)`, siteID, input.Name, input.CipherVersion, boolInt(input.Enabled), now, now)
	if err != nil {
		return 0, err
	}
	credentialID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	ciphertext, err := seal(credentialID, siteID)
	if err != nil {
		return 0, err
	}
	if len(ciphertext) == 0 {
		return 0, errors.New("credential sealer returned an empty ciphertext")
	}
	changed, err := tx.ExecContext(ctx, `UPDATE site_credentials SET secret_cipher=? WHERE id=? AND site_id=?`, ciphertext, credentialID, siteID)
	if err != nil {
		return 0, err
	}
	rows, err := changed.RowsAffected()
	if err != nil || rows != 1 {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("credential ciphertext was not stored")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credential_runtime_state(
credential_id,state,cooling_until,last_http_status,last_error_code,revision,updated_at)
VALUES (?,'active',NULL,NULL,NULL,1,?)`, credentialID, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return credentialID, nil
}

// LoadRuntimeSecretBundle verifies the exact enabled endpoint binding before
// returning ciphertext. Site-wide credential fallback is intentionally absent.
func (s *Store) LoadRuntimeSecretBundle(ctx context.Context, siteID, endpointID, credentialID int64) (RuntimeSecretBundle, error) {
	if siteID <= 0 || endpointID <= 0 || credentialID <= 0 {
		return RuntimeSecretBundle{}, errors.New("site, endpoint, and credential IDs must be positive")
	}
	var item RuntimeSecretBundle
	var secretHeaders []byte
	var secretHeadersVersion int64
	err := s.DB.QueryRowContext(ctx, `SELECT e.site_id,e.id,c.id,c.secret_cipher,c.cipher_version,
e.secret_headers_cipher,e.cipher_version
FROM site_endpoints e
JOIN sites s ON s.id=e.site_id
JOIN credential_endpoint_bindings b ON b.site_id=e.site_id AND b.endpoint_id=e.id
JOIN site_credentials c ON c.site_id=b.site_id AND c.id=b.credential_id
WHERE e.site_id=? AND e.id=? AND c.id=?
  AND s.enabled=1 AND e.enabled=1 AND b.enabled=1 AND c.enabled=1`, siteID, endpointID, credentialID).Scan(
		&item.SiteID,
		&item.EndpointID,
		&item.CredentialID,
		&item.CredentialCipher,
		&item.CredentialCipherVersion,
		&secretHeaders,
		&secretHeadersVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RuntimeSecretBundle{}, sql.ErrNoRows
	}
	if err != nil {
		return RuntimeSecretBundle{}, err
	}
	item.CredentialCipher = append([]byte(nil), item.CredentialCipher...)
	item.SecretHeadersCipher = append([]byte(nil), secretHeaders...)
	item.SecretHeadersCipherVersion = secretHeadersVersion
	return item, nil
}
