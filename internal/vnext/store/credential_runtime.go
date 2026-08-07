package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type CredentialRuntimeState struct {
	CredentialID   int64
	State          string
	CoolingUntil   *int64
	LastHTTPStatus *int
	LastErrorCode  string
	Revision       int64
	UpdatedAt      int64
}

type CredentialRuntimeUpdate struct {
	CredentialID     int64
	ExpectedRevision int64
	State            string
	CoolingUntil     *int64
	LastHTTPStatus   *int
	LastErrorCode    string
	UpdatedAt        int64
}

func (s *Store) GetCredentialRuntimeState(ctx context.Context, credentialID int64) (CredentialRuntimeState, error) {
	return scanCredentialRuntimeState(s.DB.QueryRowContext(ctx, credentialRuntimeSelect+` WHERE credential_id=?`, credentialID))
}

func (s *Store) ListCredentialRuntimeStates(ctx context.Context, credentialIDs []int64) (map[int64]CredentialRuntimeState, error) {
	result := make(map[int64]CredentialRuntimeState, len(credentialIDs))
	if len(credentialIDs) == 0 {
		return result, nil
	}
	seen := make(map[int64]struct{}, len(credentialIDs))
	placeholders := make([]string, 0, len(credentialIDs))
	args := make([]any, 0, len(credentialIDs))
	for _, credentialID := range credentialIDs {
		if credentialID <= 0 {
			return nil, errors.New("credential IDs must be positive")
		}
		if _, duplicate := seen[credentialID]; duplicate {
			continue
		}
		seen[credentialID] = struct{}{}
		placeholders = append(placeholders, "?")
		args = append(args, credentialID)
	}
	rows, err := s.DB.QueryContext(ctx, credentialRuntimeSelect+` WHERE credential_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanCredentialRuntimeState(rows)
		if err != nil {
			return nil, err
		}
		result[item.CredentialID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(seen) {
		return nil, fmt.Errorf("credential runtime state is missing for %d credential(s)", len(seen)-len(result))
	}
	return result, nil
}

// UpdateCredentialRuntimeState changes runtime availability only. The
// site_credentials.enabled configuration and model-scoped 403 access rows are
// deliberately outside this update.
func (s *Store) UpdateCredentialRuntimeState(ctx context.Context, input CredentialRuntimeUpdate) (CredentialRuntimeState, error) {
	input.State = strings.ToLower(strings.TrimSpace(input.State))
	input.LastErrorCode = strings.ToLower(strings.TrimSpace(input.LastErrorCode))
	if err := validateCredentialRuntimeUpdate(input); err != nil {
		return CredentialRuntimeState{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return CredentialRuntimeState{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE credential_runtime_state SET
state=?,cooling_until=?,last_http_status=?,last_error_code=?,revision=revision+1,updated_at=?
WHERE credential_id=? AND revision=?`, input.State, input.CoolingUntil, input.LastHTTPStatus,
		nullableString(input.LastErrorCode), input.UpdatedAt, input.CredentialID, input.ExpectedRevision)
	if err != nil {
		return CredentialRuntimeState{}, err
	}
	if err := requireRevisionChange(ctx, tx, result,
		`SELECT 1 FROM credential_runtime_state WHERE credential_id=?`, input.CredentialID); err != nil {
		return CredentialRuntimeState{}, err
	}
	updated, err := scanCredentialRuntimeState(tx.QueryRowContext(ctx, credentialRuntimeSelect+` WHERE credential_id=?`, input.CredentialID))
	if err != nil {
		return CredentialRuntimeState{}, err
	}
	if err := tx.Commit(); err != nil {
		return CredentialRuntimeState{}, err
	}
	return updated, nil
}

const credentialRuntimeSelect = `SELECT credential_id,state,cooling_until,last_http_status,last_error_code,
revision,updated_at FROM credential_runtime_state`

func scanCredentialRuntimeState(row scanner) (CredentialRuntimeState, error) {
	var item CredentialRuntimeState
	var coolingUntil, httpStatus sql.NullInt64
	var errorCode sql.NullString
	if err := row.Scan(&item.CredentialID, &item.State, &coolingUntil, &httpStatus, &errorCode,
		&item.Revision, &item.UpdatedAt); err != nil {
		return CredentialRuntimeState{}, err
	}
	item.CoolingUntil = nullInt64Pointer(coolingUntil)
	item.LastHTTPStatus = nullIntPointer(httpStatus)
	item.LastErrorCode = errorCode.String
	return item, nil
}

func validateCredentialRuntimeUpdate(input CredentialRuntimeUpdate) error {
	if input.CredentialID <= 0 || input.ExpectedRevision <= 0 || input.UpdatedAt < 0 {
		return errors.New("credential ID, expected revision, and update time are required")
	}
	if err := validateAccountingCode(input.LastErrorCode); err != nil {
		return err
	}
	switch input.State {
	case "active":
		if input.CoolingUntil != nil {
			return errors.New("active credential cannot have a cooling deadline")
		}
		if input.LastHTTPStatus != nil && (*input.LastHTTPStatus < 200 || *input.LastHTTPStatus > 299) {
			return errors.New("active credential status must be empty or successful")
		}
	case "invalid":
		if input.CoolingUntil != nil || input.LastHTTPStatus == nil || *input.LastHTTPStatus != 401 {
			return errors.New("invalid credential state requires HTTP 401 and no cooling deadline")
		}
	case "exhausted":
		if input.CoolingUntil != nil || input.LastHTTPStatus == nil || *input.LastHTTPStatus != 402 {
			return errors.New("exhausted credential state requires HTTP 402 and no cooling deadline")
		}
	case "cooling":
		if input.CoolingUntil == nil || *input.CoolingUntil <= input.UpdatedAt || input.LastHTTPStatus == nil || *input.LastHTTPStatus != 429 {
			return errors.New("cooling credential state requires HTTP 429 and a future cooling deadline")
		}
	default:
		return errors.New("credential runtime state is invalid")
	}
	return nil
}
