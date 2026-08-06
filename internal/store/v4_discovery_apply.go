package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/redact"
)

// ModelDiscoveryCredentialModels is the successful model snapshot returned by
// one credential during a discovery run.
type ModelDiscoveryCredentialModels struct {
	CredentialID int64
	Models       []string
}

// ApplyModelDiscoveryRun atomically verifies the configuration snapshot and
// applies only successful discovery results. Failed runs therefore leave the
// existing catalog untouched.
func (s *Store) ApplyModelDiscoveryRun(ctx context.Context, runID string, credentials []ModelDiscoveryCredentialModels, models []string, now int64) error {
	runID = strings.TrimSpace(runID)
	models = normalizedDiscoveryModels(models)
	if runID == "" || len(credentials) == 0 || len(models) == 0 {
		return errors.New("discovery run, successful credentials, and models are required")
	}
	if now <= 0 {
		now = NowMS()
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var siteID, endpointID, siteRevision, endpointRevision int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT site_id,endpoint_id,status,base_site_revision,base_endpoint_revision
FROM model_discovery_runs WHERE id=?`, runID).Scan(
		&siteID, &endpointID, &status, &siteRevision, &endpointRevision,
	); err != nil {
		return err
	}
	if status != "running" {
		return ErrRequestAlreadyFinished
	}

	// These no-op writes acquire the SQLite write lock before catalog changes,
	// preventing a configuration update from racing between validation and apply.
	if err := requireDiscoveryRevision(ctx, tx,
		"UPDATE sites SET updated_at=updated_at WHERE id=? AND revision=? AND enabled=1",
		siteID, siteRevision); err != nil {
		return err
	}
	if err := requireDiscoveryRevision(ctx, tx,
		"UPDATE inference_endpoints SET updated_at=updated_at WHERE id=? AND site_id=? AND revision=? AND enabled=1",
		endpointID, siteID, endpointRevision); err != nil {
		return err
	}

	modelIDs := make(map[string]int64, len(models))
	for _, name := range models {
		var existingID int64
		existingErr := tx.QueryRowContext(ctx,
			"SELECT id FROM site_models WHERE endpoint_id=? AND model_name=?", endpointID, name,
		).Scan(&existingID)
		if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
			return existingErr
		}
		if existingErr == nil {
			if err := bumpPublishedModelsForSiteModelTx(ctx, tx, existingID, now); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO site_models(
site_id,endpoint_id,model_name,display_name,enabled,stale,missing_count,last_seen_at,revision,created_at,updated_at)
VALUES (?,?,?,NULL,1,0,0,?,1,?,?)
ON CONFLICT(endpoint_id,model_name) DO UPDATE SET
stale=0,missing_count=0,last_seen_at=excluded.last_seen_at,revision=site_models.revision+1,updated_at=excluded.updated_at
WHERE site_models.site_id=excluded.site_id`, siteID, endpointID, name, now, now, now)
		if err != nil {
			return err
		}
	}

	rows, err := tx.QueryContext(ctx, "SELECT id,model_name FROM site_models WHERE endpoint_id=?", endpointID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		modelIDs[name] = id
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	seenCredentials := make(map[int64]struct{}, len(credentials))
	for _, credential := range credentials {
		if credential.CredentialID <= 0 {
			return errors.New("discovery credential ID must be positive")
		}
		if _, duplicate := seenCredentials[credential.CredentialID]; duplicate {
			return errors.New("discovery credential results must be unique")
		}
		seenCredentials[credential.CredentialID] = struct{}{}
		var exists int
		if err := tx.QueryRowContext(ctx,
			"SELECT 1 FROM inference_credentials WHERE id=? AND site_id=? AND enabled=1",
			credential.CredentialID, siteID,
		).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrRevisionConflict
			}
			return err
		}
		found := make(map[string]struct{}, len(credential.Models))
		for _, name := range normalizedDiscoveryModels(credential.Models) {
			found[name] = struct{}{}
		}
		for name, modelID := range modelIDs {
			availability := "supported"
			missingCount := 0
			var lastSeen any = now
			if _, present := found[name]; !present {
				availability = "unknown"
				lastSeen = nil
				var currentAvailability string
				err := tx.QueryRowContext(ctx, `SELECT availability,missing_count
FROM credential_model_access WHERE credential_id=? AND site_model_id=?`, credential.CredentialID, modelID).Scan(
					&currentAvailability, &missingCount,
				)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if errors.Is(err, sql.ErrNoRows) {
					missingCount = 0
				} else {
					availability = currentAvailability
				}
				missingCount++
				if missingCount >= 2 {
					availability = "unsupported"
				}
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO credential_model_access(
site_id,credential_id,site_model_id,availability,missing_count,last_seen_at,last_checked_at,revision,updated_at)
VALUES (?,?,?,?,?,?,?,1,?)
ON CONFLICT(credential_id,site_model_id) DO UPDATE SET
availability=excluded.availability,missing_count=excluded.missing_count,
last_seen_at=COALESCE(excluded.last_seen_at,credential_model_access.last_seen_at),
last_checked_at=excluded.last_checked_at,revision=credential_model_access.revision+1,updated_at=excluded.updated_at
WHERE credential_model_access.site_id=excluded.site_id`, siteID, credential.CredentialID, modelID,
				availability, missingCount, lastSeen, now, now)
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// FailModelDiscoveryRun records an orchestration failure such as a stale
// configuration snapshot. It intentionally overrides attempt-derived success.
func (s *Store) FailModelDiscoveryRun(ctx context.Context, id string, modelCount int, summary json.RawMessage, errorMessage string, finishedAt int64) (ModelDiscoveryRun, error) {
	id = strings.TrimSpace(id)
	if id == "" || modelCount < 0 {
		return ModelDiscoveryRun{}, errors.New("discovery run ID is required and model count cannot be negative")
	}
	if finishedAt <= 0 {
		finishedAt = NowMS()
	}
	normalizedSummary, err := normalizeRunSummary(summary)
	if err != nil {
		return ModelDiscoveryRun{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ModelDiscoveryRun{}, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM model_discovery_runs WHERE id=?", id).Scan(&status); err != nil {
		return ModelDiscoveryRun{}, err
	}
	if status != "running" {
		if err := tx.Commit(); err != nil {
			return ModelDiscoveryRun{}, err
		}
		return s.GetModelDiscoveryRun(ctx, id)
	}
	var successes int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0)
FROM model_discovery_attempts WHERE discovery_run_id=?`, id).Scan(&successes); err != nil {
		return ModelDiscoveryRun{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE model_discovery_runs SET status='failed',success_count=?,model_count=?,
summary_json=?,error_message=?,finished_at=? WHERE id=? AND status='running'`, successes, modelCount,
		normalizedSummary, nullableString(redact.String(errorMessage)), finishedAt, id); err != nil {
		return ModelDiscoveryRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelDiscoveryRun{}, err
	}
	return s.GetModelDiscoveryRun(ctx, id)
}

func requireDiscoveryRevision(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrRevisionConflict
	}
	return nil
}

func normalizedDiscoveryModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	sort.Strings(result)
	return result
}
