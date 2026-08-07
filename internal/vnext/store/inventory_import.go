package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const MaxSealedEndpointCredentialImports = 100

type SealedEndpointCredentialImport struct {
	Endpoint   SiteEndpointWrite
	Credential SealedSiteCredentialInput
}

type SealedEndpointCredentialImportResult struct {
	EndpointID   int64
	CredentialID int64
}

// ImportSealedEndpointCredentials commits credentials, endpoints, and their
// bindings as one unit. Existing endpoints are reused only when their exact
// protocol surface and authentication placement match the import.
func (s *Store) ImportSealedEndpointCredentials(
	ctx context.Context,
	siteID int64,
	inputs []SealedEndpointCredentialImport,
	seal func(index int, credentialID, siteID int64) ([]byte, error),
) ([]SealedEndpointCredentialImportResult, error) {
	if siteID <= 0 || len(inputs) == 0 || len(inputs) > MaxSealedEndpointCredentialImports || seal == nil {
		return nil, errors.New("site, imports, and credential sealer are required")
	}

	normalized := make([]SealedEndpointCredentialImport, len(inputs))
	credentialNames := make(map[string]struct{}, len(inputs))
	for index, input := range inputs {
		input.Credential.Name = strings.TrimSpace(input.Credential.Name)
		if input.Credential.Name == "" || input.Credential.CipherVersion <= 0 {
			return nil, fmt.Errorf("import %d has invalid credential metadata", index)
		}
		nameKey := strings.ToLower(input.Credential.Name)
		if _, duplicate := credentialNames[nameKey]; duplicate {
			return nil, fmt.Errorf("import %d repeats credential name %q", index, input.Credential.Name)
		}
		credentialNames[nameKey] = struct{}{}
		endpoint, err := normalizeSiteEndpointWrite(input.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("import %d endpoint: %w", index, err)
		}
		input.Endpoint = endpoint
		normalized[index] = input
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireSite(ctx, tx, siteID); err != nil {
		return nil, err
	}

	var nextEndpointPosition int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position)+1,0) FROM site_endpoints WHERE site_id=?`, siteID).
		Scan(&nextEndpointPosition); err != nil {
		return nil, err
	}
	now := NowMS()
	results := make([]SealedEndpointCredentialImportResult, 0, len(normalized))
	for index, input := range normalized {
		credentialID, err := insertSealedImportedCredential(ctx, tx, siteID, index, input.Credential, now, seal)
		if err != nil {
			return nil, normalizeInventoryConflict(err)
		}
		endpointID, created, err := findOrInsertImportedEndpoint(ctx, tx, siteID, input.Endpoint, nextEndpointPosition, now)
		if err != nil {
			return nil, normalizeInventoryConflict(err)
		}
		if created {
			nextEndpointPosition++
		} else if _, err := tx.ExecContext(ctx, `UPDATE site_endpoints
SET revision=revision+1,updated_at=? WHERE id=? AND site_id=?`, now, endpointID, siteID); err != nil {
			return nil, err
		}
		var bindingPosition int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position)+1,0)
FROM credential_endpoint_bindings WHERE endpoint_id=?`, endpointID).Scan(&bindingPosition); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO credential_endpoint_bindings(
site_id,endpoint_id,credential_id,position,enabled,created_at,updated_at) VALUES (?,?,?,?,1,?,?)`,
			siteID, endpointID, credentialID, bindingPosition, now, now); err != nil {
			return nil, err
		}
		results = append(results, SealedEndpointCredentialImportResult{
			EndpointID: endpointID, CredentialID: credentialID,
		})
	}
	if _, err := s.EnqueueConfigRevisionTx(ctx, tx, "token_json_imported", time.UnixMilli(now)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}

func insertSealedImportedCredential(
	ctx context.Context,
	tx *sql.Tx,
	siteID int64,
	index int,
	input SealedSiteCredentialInput,
	now int64,
	seal func(index int, credentialID, siteID int64) ([]byte, error),
) (int64, error) {
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
	ciphertext, err := seal(index, credentialID, siteID)
	if err != nil {
		return 0, err
	}
	if len(ciphertext) == 0 {
		return 0, errors.New("credential sealer returned an empty ciphertext")
	}
	changed, updateErr := tx.ExecContext(ctx, `UPDATE site_credentials SET secret_cipher=? WHERE id=? AND site_id=?`,
		ciphertext, credentialID, siteID)
	clear(ciphertext)
	if updateErr != nil {
		return 0, updateErr
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
	return credentialID, nil
}

func findOrInsertImportedEndpoint(
	ctx context.Context,
	tx *sql.Tx,
	siteID int64,
	input SiteEndpointWrite,
	position int,
	now int64,
) (int64, bool, error) {
	var endpointID int64
	var authScheme string
	err := tx.QueryRowContext(ctx, `SELECT id,auth_scheme FROM site_endpoints
WHERE site_id=? AND base_url=? AND wire_protocol=? AND surface=?`,
		siteID, input.BaseURL, input.WireProtocol, input.Surface).Scan(&endpointID, &authScheme)
	if err == nil {
		if authScheme != input.AuthScheme {
			return 0, false, fmt.Errorf("%w: existing endpoint uses a different authentication scheme", ErrConflict)
		}
		return endpointID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO site_endpoints(
site_id,name,base_url,wire_protocol,surface,adapter_kind,auth_scheme,header_template_json,secret_headers_cipher,cipher_version,
position,enabled,revision,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,NULL,0,?,?,1,?,?)`, siteID, input.Name, input.BaseURL, input.WireProtocol,
		input.Surface, input.AdapterKind, input.AuthScheme, string(input.HeaderTemplate), position, boolInt(input.Enabled), now, now)
	if err != nil {
		return 0, false, err
	}
	endpointID, err = result.LastInsertId()
	return endpointID, true, err
}
