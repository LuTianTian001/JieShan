package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/redact"
)

func (s *Store) InsertProbeRun(ctx context.Context, item ProbeRun) error {
	item.ID = strings.TrimSpace(item.ID)
	item.TriggerKind = strings.ToLower(strings.TrimSpace(item.TriggerKind))
	if item.ID == "" || item.PublishedModelID <= 0 {
		return errors.New("probe run ID and published model are required")
	}
	if item.TriggerKind == "" {
		item.TriggerKind = "scheduled"
	}
	if !validProbeTrigger(item.TriggerKind) {
		return errors.New("unknown probe trigger")
	}
	if item.StartedAt <= 0 {
		item.StartedAt = NowMS()
	}
	var revision int64
	var targetCount int
	if err := s.DB.QueryRowContext(ctx, `SELECT p.revision,
(SELECT COUNT(*) FROM route_site_targets t WHERE t.published_model_id=p.id AND t.enabled=1)
FROM published_models p WHERE p.id=?`, item.PublishedModelID).Scan(&revision, &targetCount); err != nil {
		return err
	}
	if item.PublishedModelRevision <= 0 {
		item.PublishedModelRevision = revision
	}
	if item.TargetCount <= 0 {
		item.TargetCount = targetCount
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO probe_runs(
id,published_model_id,published_model_revision,trigger_kind,status,target_count,started_at)
VALUES (?,?,?,?,'running',?,?)`, item.ID, item.PublishedModelID, item.PublishedModelRevision,
		item.TriggerKind, item.TargetCount, item.StartedAt)
	return err
}

func (s *Store) InsertProbeAttempt(ctx context.Context, item ProbeAttempt) (int64, error) {
	item.ProbeRunID = strings.TrimSpace(item.ProbeRunID)
	item.Status = strings.ToLower(strings.TrimSpace(item.Status))
	if item.ProbeRunID == "" || item.AttemptIndex < 0 || item.RouteSiteTargetID == nil || *item.RouteSiteTargetID <= 0 {
		return 0, errors.New("probe run, attempt index, and route site target are required")
	}
	if !validAttemptStatus(item.Status) {
		return 0, errors.New("unknown probe attempt status")
	}
	if item.FinishedAt <= 0 {
		item.FinishedAt = NowMS()
	}
	if item.StartedAt <= 0 || item.StartedAt > item.FinishedAt {
		item.StartedAt = item.FinishedAt
	}
	var runStatus string
	var siteID, endpointID, siteModelID int64
	if err := s.DB.QueryRowContext(ctx, `SELECT r.status,t.site_id,s.name,t.endpoint_id,e.name,t.site_model_id,m.model_name
FROM probe_runs r JOIN route_site_targets t ON t.published_model_id=r.published_model_id
JOIN sites s ON s.id=t.site_id JOIN inference_endpoints e ON e.id=t.endpoint_id JOIN site_models m ON m.id=t.site_model_id
WHERE r.id=? AND t.id=?`, item.ProbeRunID, *item.RouteSiteTargetID).Scan(
		&runStatus, &siteID, &item.SiteName, &endpointID, &item.EndpointName, &siteModelID, &item.SourceModel,
	); err != nil {
		return 0, err
	}
	if runStatus != "running" {
		return 0, errors.New("probe run is already finished")
	}
	item.SiteID = copyInt64(siteID)
	item.EndpointID = copyInt64(endpointID)
	item.SiteModelID = copyInt64(siteModelID)
	if item.InferenceCredentialID != nil {
		if *item.InferenceCredentialID <= 0 {
			return 0, errors.New("inference credential ID must be positive")
		}
		if err := s.DB.QueryRowContext(ctx, `SELECT name FROM inference_credentials WHERE id=? AND site_id=?`,
			*item.InferenceCredentialID, siteID).Scan(&item.CredentialName); err != nil {
			return 0, err
		}
	}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO probe_attempts(
probe_run_id,attempt_index,route_site_target_id,site_id,endpoint_id,inference_credential_id,site_model_id,
site_name,endpoint_name,credential_name,source_model,status,http_status,latency_ms,first_output_ms,
error_class,error_message,started_at,finished_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ProbeRunID, item.AttemptIndex, item.RouteSiteTargetID,
		item.SiteID, item.EndpointID, item.InferenceCredentialID, item.SiteModelID, item.SiteName, item.EndpointName,
		nullableString(item.CredentialName), item.SourceModel, item.Status, item.HTTPStatus, item.LatencyMS,
		item.FirstOutputMS, nullableString(item.ErrorClass), nullableString(redact.String(item.ErrorMessage)),
		item.StartedAt, item.FinishedAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) CompleteProbeRun(ctx context.Context, id string, finishedAt int64, errorMessage string) (ProbeRun, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ProbeRun{}, errors.New("probe run ID is required")
	}
	if finishedAt <= 0 {
		finishedAt = NowMS()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProbeRun{}, err
	}
	defer tx.Rollback()
	var targetCount int
	var currentStatus string
	if err := tx.QueryRowContext(ctx, "SELECT target_count,status FROM probe_runs WHERE id=?", id).Scan(&targetCount, &currentStatus); err != nil {
		return ProbeRun{}, err
	}
	if currentStatus != "running" {
		if err := tx.Commit(); err != nil {
			return ProbeRun{}, err
		}
		return s.GetProbeRun(ctx, id)
	}
	var observedTargets, successes, failures, skipped int
	if err := tx.QueryRowContext(ctx, `WITH target_results AS (
  SELECT route_site_target_id,
    MAX(CASE WHEN status='success' THEN 1 ELSE 0 END) AS succeeded,
    MAX(CASE WHEN status='failed' THEN 1 ELSE 0 END) AS failed,
    MAX(CASE WHEN status='skipped' THEN 1 ELSE 0 END) AS skipped
  FROM probe_attempts WHERE probe_run_id=? GROUP BY route_site_target_id
)
SELECT COUNT(*),
COALESCE(SUM(succeeded),0),
COALESCE(SUM(CASE WHEN succeeded=0 AND failed=1 THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN succeeded=0 AND failed=0 AND skipped=1 THEN 1 ELSE 0 END),0)
FROM target_results`, id).Scan(&observedTargets, &successes, &failures, &skipped); err != nil {
		return ProbeRun{}, err
	}
	if observedTargets > targetCount {
		targetCount = observedTargets
	}
	if observedTargets < targetCount {
		skipped += targetCount - observedTargets
	}
	status := "failed"
	if targetCount > 0 && successes == targetCount {
		status = "success"
	} else if successes > 0 {
		status = "partial"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE probe_runs SET status=?,target_count=?,success_count=?,failure_count=?,skipped_count=?,
error_message=?,finished_at=? WHERE id=? AND status='running'`, status, targetCount, successes, failures, skipped,
		nullableString(redact.String(errorMessage)), finishedAt, id); err != nil {
		return ProbeRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProbeRun{}, err
	}
	return s.GetProbeRun(ctx, id)
}

func (s *Store) CancelProbeRun(ctx context.Context, id string, finishedAt int64, message string) error {
	if finishedAt <= 0 {
		finishedAt = NowMS()
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE probe_runs SET status='cancelled',error_message=?,finished_at=?
WHERE id=? AND status='running'`, nullableString(redact.String(message)), finishedAt, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrRequestAlreadyFinished
	}
	return nil
}

func (s *Store) GetProbeRun(ctx context.Context, id string) (ProbeRun, error) {
	return scanProbeRun(s.DB.QueryRowContext(ctx, probeRunSelect+` WHERE r.id=?`, strings.TrimSpace(id)))
}

func (s *Store) ListProbeRuns(ctx context.Context, publishedModelID int64, limit int) ([]ProbeRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, probeRunSelect+` WHERE r.published_model_id=? ORDER BY r.started_at DESC,r.id DESC LIMIT ?`, publishedModelID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ProbeRun, 0)
	for rows.Next() {
		item, err := scanProbeRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListProbeAttempts(ctx context.Context, runID string) ([]ProbeAttempt, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,probe_run_id,attempt_index,route_site_target_id,site_id,endpoint_id,
inference_credential_id,site_model_id,site_name,endpoint_name,credential_name,source_model,status,http_status,
latency_ms,first_output_ms,error_class,error_message,started_at,finished_at
FROM probe_attempts WHERE probe_run_id=? ORDER BY attempt_index,id`, strings.TrimSpace(runID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ProbeAttempt, 0)
	for rows.Next() {
		item, err := scanProbeAttempt(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const probeRunSelect = `SELECT r.id,r.published_model_id,p.public_name,r.published_model_revision,r.trigger_kind,r.status,
r.target_count,r.success_count,r.failure_count,r.skipped_count,r.error_message,r.started_at,r.finished_at
FROM probe_runs r JOIN published_models p ON p.id=r.published_model_id`

func scanProbeRun(row scanner) (ProbeRun, error) {
	var item ProbeRun
	var message sql.NullString
	var finished sql.NullInt64
	err := row.Scan(&item.ID, &item.PublishedModelID, &item.PublicModel, &item.PublishedModelRevision,
		&item.TriggerKind, &item.Status, &item.TargetCount, &item.SuccessCount, &item.FailureCount,
		&item.SkippedCount, &message, &item.StartedAt, &finished)
	item.ErrorMessage = message.String
	item.FinishedAt = int64Ptr(finished)
	return item, err
}

func scanProbeAttempt(row scanner) (ProbeAttempt, error) {
	var item ProbeAttempt
	var targetID, siteID, endpointID, credentialID, siteModelID, httpStatus, latency, firstOutput sql.NullInt64
	var credentialName, errorClass, errorMessage sql.NullString
	err := row.Scan(&item.ID, &item.ProbeRunID, &item.AttemptIndex, &targetID, &siteID, &endpointID,
		&credentialID, &siteModelID, &item.SiteName, &item.EndpointName, &credentialName, &item.SourceModel,
		&item.Status, &httpStatus, &latency, &firstOutput, &errorClass, &errorMessage, &item.StartedAt, &item.FinishedAt)
	item.RouteSiteTargetID = int64Ptr(targetID)
	item.SiteID = int64Ptr(siteID)
	item.EndpointID = int64Ptr(endpointID)
	item.InferenceCredentialID = int64Ptr(credentialID)
	item.SiteModelID = int64Ptr(siteModelID)
	item.CredentialName = credentialName.String
	item.HTTPStatus = intPtr(httpStatus)
	item.LatencyMS = int64Ptr(latency)
	item.FirstOutputMS = int64Ptr(firstOutput)
	item.ErrorClass = errorClass.String
	item.ErrorMessage = errorMessage.String
	return item, err
}

func (s *Store) InsertModelDiscoveryRun(ctx context.Context, item ModelDiscoveryRun) error {
	item.ID = strings.TrimSpace(item.ID)
	item.Mode = strings.ToLower(strings.TrimSpace(item.Mode))
	if item.ID == "" || item.SiteID <= 0 || item.EndpointID <= 0 {
		return errors.New("discovery run, site, and endpoint are required")
	}
	if item.Mode == "" {
		item.Mode = "first_success"
	}
	if !validDiscoveryMode(item.Mode) {
		return errors.New("unknown model discovery mode")
	}
	if item.StartedAt <= 0 {
		item.StartedAt = NowMS()
	}
	var siteRevision, endpointRevision int64
	var credentials int
	if err := s.DB.QueryRowContext(ctx, `SELECT s.revision,e.revision,
(SELECT COUNT(*) FROM inference_credentials c WHERE c.site_id=s.id AND c.enabled=1)
FROM sites s JOIN inference_endpoints e ON e.site_id=s.id WHERE s.id=? AND e.id=?`, item.SiteID, item.EndpointID).Scan(
		&siteRevision, &endpointRevision, &credentials,
	); err != nil {
		return err
	}
	if item.BaseSiteRevision <= 0 {
		item.BaseSiteRevision = siteRevision
	}
	if item.BaseEndpointRevision <= 0 {
		item.BaseEndpointRevision = endpointRevision
	}
	if item.CredentialCount <= 0 {
		item.CredentialCount = credentials
	}
	summary, err := normalizeRunSummary(item.Summary)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO model_discovery_runs(
id,site_id,endpoint_id,mode,status,base_site_revision,base_endpoint_revision,credential_count,summary_json,started_at)
VALUES (?,?,?,?,'running',?,?,?,?,?)`, item.ID, item.SiteID, item.EndpointID, item.Mode,
		item.BaseSiteRevision, item.BaseEndpointRevision, item.CredentialCount, summary, item.StartedAt)
	return err
}

func (s *Store) InsertModelDiscoveryAttempt(ctx context.Context, item ModelDiscoveryAttempt) (int64, error) {
	item.DiscoveryRunID = strings.TrimSpace(item.DiscoveryRunID)
	item.Status = strings.ToLower(strings.TrimSpace(item.Status))
	if item.DiscoveryRunID == "" || item.AttemptIndex < 0 {
		return 0, errors.New("discovery run and attempt index are required")
	}
	if !validAttemptStatus(item.Status) {
		return 0, errors.New("unknown discovery attempt status")
	}
	if item.ModelCount < 0 || item.PagesFetched < 0 {
		return 0, errors.New("discovery counts cannot be negative")
	}
	if item.FinishedAt <= 0 {
		item.FinishedAt = NowMS()
	}
	if item.StartedAt <= 0 || item.StartedAt > item.FinishedAt {
		item.StartedAt = item.FinishedAt
	}
	var runStatus string
	var siteID int64
	if err := s.DB.QueryRowContext(ctx, "SELECT status,site_id FROM model_discovery_runs WHERE id=?", item.DiscoveryRunID).Scan(&runStatus, &siteID); err != nil {
		return 0, err
	}
	if runStatus != "running" {
		return 0, errors.New("model discovery run is already finished")
	}
	if item.InferenceCredentialID != nil {
		if *item.InferenceCredentialID <= 0 {
			return 0, errors.New("inference credential ID must be positive")
		}
		if err := s.DB.QueryRowContext(ctx, "SELECT name FROM inference_credentials WHERE id=? AND site_id=?",
			*item.InferenceCredentialID, siteID).Scan(&item.CredentialName); err != nil {
			return 0, err
		}
	}
	if strings.TrimSpace(item.CredentialName) == "" {
		item.CredentialName = "Unavailable credential"
	}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO model_discovery_attempts(
discovery_run_id,attempt_index,inference_credential_id,credential_name,status,model_count,complete,pages_fetched,
error_class,error_message,started_at,finished_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, item.DiscoveryRunID, item.AttemptIndex, item.InferenceCredentialID,
		item.CredentialName, item.Status, item.ModelCount, boolInt(item.Complete), item.PagesFetched,
		nullableString(item.ErrorClass), nullableString(redact.String(item.ErrorMessage)), item.StartedAt, item.FinishedAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) CompleteModelDiscoveryRun(ctx context.Context, id string, modelCount int, summary json.RawMessage, errorMessage string, finishedAt int64) (ModelDiscoveryRun, error) {
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
	var currentStatus string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM model_discovery_runs WHERE id=?", id).Scan(&currentStatus); err != nil {
		return ModelDiscoveryRun{}, err
	}
	if currentStatus != "running" {
		if err := tx.Commit(); err != nil {
			return ModelDiscoveryRun{}, err
		}
		return s.GetModelDiscoveryRun(ctx, id)
	}
	var successes, failures, skipped, incomplete int
	if err := tx.QueryRowContext(ctx, `SELECT
COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN status='skipped' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN status='success' AND complete=0 THEN 1 ELSE 0 END),0)
FROM model_discovery_attempts WHERE discovery_run_id=?`, id).Scan(&successes, &failures, &skipped, &incomplete); err != nil {
		return ModelDiscoveryRun{}, err
	}
	status := "failed"
	if successes > 0 && failures == 0 && skipped == 0 && incomplete == 0 {
		status = "success"
	} else if successes > 0 {
		status = "partial"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE model_discovery_runs SET status=?,success_count=?,model_count=?,summary_json=?,
error_message=?,finished_at=? WHERE id=? AND status='running'`, status, successes, modelCount, normalizedSummary,
		nullableString(redact.String(errorMessage)), finishedAt, id); err != nil {
		return ModelDiscoveryRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelDiscoveryRun{}, err
	}
	return s.GetModelDiscoveryRun(ctx, id)
}

func (s *Store) GetModelDiscoveryRun(ctx context.Context, id string) (ModelDiscoveryRun, error) {
	return scanModelDiscoveryRun(s.DB.QueryRowContext(ctx, modelDiscoveryRunSelect+` WHERE id=?`, strings.TrimSpace(id)))
}

func (s *Store) ListModelDiscoveryRuns(ctx context.Context, siteID int64, limit int) ([]ModelDiscoveryRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, modelDiscoveryRunSelect+` WHERE site_id=? ORDER BY started_at DESC,id DESC LIMIT ?`, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ModelDiscoveryRun, 0)
	for rows.Next() {
		item, err := scanModelDiscoveryRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListModelDiscoveryAttempts(ctx context.Context, runID string) ([]ModelDiscoveryAttempt, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,discovery_run_id,attempt_index,inference_credential_id,credential_name,
status,model_count,complete,pages_fetched,error_class,error_message,started_at,finished_at
FROM model_discovery_attempts WHERE discovery_run_id=? ORDER BY attempt_index,id`, strings.TrimSpace(runID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ModelDiscoveryAttempt, 0)
	for rows.Next() {
		var item ModelDiscoveryAttempt
		var credentialID sql.NullInt64
		var complete int
		var errorClass, errorMessage sql.NullString
		if err := rows.Scan(&item.ID, &item.DiscoveryRunID, &item.AttemptIndex, &credentialID, &item.CredentialName,
			&item.Status, &item.ModelCount, &complete, &item.PagesFetched, &errorClass, &errorMessage,
			&item.StartedAt, &item.FinishedAt); err != nil {
			return nil, err
		}
		item.InferenceCredentialID = int64Ptr(credentialID)
		item.Complete = complete == 1
		item.ErrorClass = errorClass.String
		item.ErrorMessage = errorMessage.String
		items = append(items, item)
	}
	return items, rows.Err()
}

const modelDiscoveryRunSelect = `SELECT id,site_id,endpoint_id,mode,status,base_site_revision,base_endpoint_revision,
credential_count,success_count,model_count,summary_json,error_message,started_at,finished_at FROM model_discovery_runs`

func scanModelDiscoveryRun(row scanner) (ModelDiscoveryRun, error) {
	var item ModelDiscoveryRun
	var summary string
	var message sql.NullString
	var finished sql.NullInt64
	err := row.Scan(&item.ID, &item.SiteID, &item.EndpointID, &item.Mode, &item.Status,
		&item.BaseSiteRevision, &item.BaseEndpointRevision, &item.CredentialCount, &item.SuccessCount,
		&item.ModelCount, &summary, &message, &item.StartedAt, &finished)
	item.Summary = json.RawMessage(summary)
	item.ErrorMessage = message.String
	item.FinishedAt = int64Ptr(finished)
	return item, err
}

func normalizeRunSummary(value json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(value)) == 0 {
		return "{}", nil
	}
	if !json.Valid(value) {
		return "", errors.New("run summary must be valid JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return "", err
	}
	return compact.String(), nil
}

func validProbeTrigger(value string) bool {
	return value == "scheduled" || value == "manual" || value == "recovery"
}

func validDiscoveryMode(value string) bool {
	return value == "selected" || value == "first_success" || value == "all"
}

func validAttemptStatus(value string) bool {
	return value == "success" || value == "failed" || value == "skipped"
}
