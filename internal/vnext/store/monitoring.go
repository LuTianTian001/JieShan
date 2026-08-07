package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	DefaultModelMonitorHistoryLimit = 288
)

var (
	ErrModelMonitorNotFound  = errors.New("model monitor not found or disabled")
	ErrModelMonitorBusy      = errors.New("model monitor already has an active lease")
	ErrModelMonitorLeaseLost = errors.New("model monitor lease was lost")
	ErrModelProbeRunNotFound = errors.New("model probe run not found or already finished")
)

type ModelMonitorSetting struct {
	PublishedModelID    int64
	Enabled             bool
	Interval            time.Duration
	HistoryLimit        int
	NextProbeAt         time.Time
	LastProbeStartedAt  *time.Time
	LastProbeFinishedAt *time.Time
	LeaseOwner          string
	LeaseUntil          *time.Time
	Revision            int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type ModelMonitorSettingWrite struct {
	Enabled      bool
	HistoryLimit int
}

type ModelMonitorTarget struct {
	PublishedModelTargetID       int64
	PublishedModelTargetRevision int64
	ProviderModelTargetID        int64
	ProviderModelTargetRevision  int64
	Position                     int
	SiteID                       int64
	SiteName                     string
	EndpointID                   int64
	EndpointName                 string
	BaseURL                      string
	WireProtocol                 string
	Surface                      string
	AdapterKind                  string
	AuthScheme                   string
	HeaderTemplate               json.RawMessage
	SecretHeadersConfigured      bool
	SecretHeadersCipherVersion   int64
	SourceModel                  string
	CredentialIDs                []int64
}

type ModelMonitorJob struct {
	Setting                ModelMonitorSetting
	PublicModel            string
	PublishedModelRevision int64
	Targets                []ModelMonitorTarget
}

type ModelProbeRunWrite struct {
	ID               string
	PublishedModelID int64
	LeaseOwner       string
	TriggerKind      string
	TargetCount      int
	StartedAt        time.Time
}

type ModelProbeTargetWrite struct {
	RunID                        string
	PublishedModelID             int64
	PublishedModelTargetID       int64
	PublishedModelTargetRevision int64
	ProviderModelTargetID        int64
	ProviderModelTargetRevision  int64
	TargetPosition               int
	SiteID                       int64
	EndpointID                   int64
	SiteName                     string
	EndpointName                 string
	SourceModel                  string
	WireProtocol                 string
	Surface                      string
	Outcome                      string
	PermitMode                   string
	PermitReason                 string
	HTTPStatus                   *int
	FailureKind                  string
	ErrorCode                    string
	LatencyMS                    int64
	FirstOutputMS                *int64
	StartedAt                    time.Time
	FinishedAt                   time.Time
	HealthApplied                bool
	HealthApplyReason            string
	HealthErrorCode              string
}

type ModelProbeRun struct {
	ID                     string
	PublishedModelID       int64
	PublishedModelRevision int64
	PublicModelSnapshot    string
	TriggerKind            string
	Status                 string
	TargetCount            int
	SuccessCount           int
	FailureCount           int
	SkippedCount           int
	StartedAt              time.Time
	FinishedAt             *time.Time
}

type ModelProbeTargetResult struct {
	ID                           int64
	RunID                        string
	PublishedModelID             int64
	PublishedModelTargetID       int64
	PublishedModelTargetRevision int64
	ProviderModelTargetID        int64
	ProviderModelTargetRevision  int64
	TargetPosition               int
	SiteID                       int64
	EndpointID                   int64
	SiteNameSnapshot             string
	EndpointNameSnapshot         string
	SourceModelSnapshot          string
	WireProtocol                 string
	Surface                      string
	Outcome                      string
	PermitMode                   string
	PermitReason                 string
	HTTPStatus                   *int
	FailureKind                  string
	ErrorCode                    string
	LatencyMS                    int64
	FirstOutputMS                *int64
	StartedAt                    time.Time
	FinishedAt                   time.Time
	HealthApplied                bool
	HealthApplyReason            string
	HealthErrorCode              string
}

func (s *Store) PutModelMonitorSetting(
	ctx context.Context,
	publishedModelID int64,
	input ModelMonitorSettingWrite,
	now time.Time,
) (ModelMonitorSetting, error) {
	if publishedModelID <= 0 {
		return ModelMonitorSetting{}, errors.New("published model ID must be positive")
	}
	if now.IsZero() {
		return ModelMonitorSetting{}, errors.New("monitor setting time must be set")
	}
	historyLimit, err := normalizeMonitorHistoryLimit(input)
	if err != nil {
		return ModelMonitorSetting{}, err
	}
	nowMS := now.UTC().UnixMilli()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO model_monitor_settings(
published_model_id,enabled,interval_ms,history_limit,next_probe_at,
last_probe_started_at,last_probe_finished_at,lease_owner,lease_until,revision,created_at,updated_at)
SELECT id,?,(SELECT probe_interval_ms FROM runtime_settings WHERE singleton_id=1),?,?,NULL,NULL,NULL,NULL,1,?,?
FROM published_models WHERE id=?
ON CONFLICT(published_model_id) DO UPDATE SET
  enabled=excluded.enabled,
  interval_ms=excluded.interval_ms,
  history_limit=excluded.history_limit,
  next_probe_at=CASE
    WHEN excluded.enabled=1 AND (model_monitor_settings.enabled=0 OR
      model_monitor_settings.interval_ms<>excluded.interval_ms) THEN excluded.next_probe_at
    ELSE model_monitor_settings.next_probe_at
  END,
  lease_owner=CASE WHEN excluded.enabled=0 THEN NULL ELSE model_monitor_settings.lease_owner END,
  lease_until=CASE WHEN excluded.enabled=0 THEN NULL ELSE model_monitor_settings.lease_until END,
  revision=model_monitor_settings.revision+1,
  updated_at=excluded.updated_at`, boolInt(input.Enabled), historyLimit,
		nowMS, nowMS, nowMS, publishedModelID)
	if err != nil {
		return ModelMonitorSetting{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ModelMonitorSetting{}, err
	}
	if changed != 1 {
		return ModelMonitorSetting{}, ErrModelMonitorNotFound
	}
	return s.GetModelMonitorSetting(ctx, publishedModelID)
}

func (s *Store) GetModelMonitorSetting(ctx context.Context, publishedModelID int64) (ModelMonitorSetting, error) {
	if publishedModelID <= 0 {
		return ModelMonitorSetting{}, errors.New("published model ID must be positive")
	}
	item, err := scanModelMonitorSetting(s.DB.QueryRowContext(ctx, modelMonitorSettingSelect+`
WHERE published_model_id=?`, publishedModelID))
	if errors.Is(err, sql.ErrNoRows) {
		return ModelMonitorSetting{}, ErrModelMonitorNotFound
	}
	return item, err
}

func (s *Store) ListModelMonitorSettings(ctx context.Context) ([]ModelMonitorSetting, error) {
	rows, err := s.DB.QueryContext(ctx, modelMonitorSettingSelect+`
ORDER BY published_model_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ModelMonitorSetting, 0)
	for rows.Next() {
		item, scanErr := scanModelMonitorSetting(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ClaimDueModelMonitors(
	ctx context.Context,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
	limit int,
) ([]ModelMonitorJob, error) {
	owner, leaseUntil, err := validateMonitorLease(owner, now, leaseDuration)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 128 {
		return nil, errors.New("due monitor claim limit must be between 1 and 128")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nowMS := now.UTC().UnixMilli()
	rows, err := tx.QueryContext(ctx, `UPDATE model_monitor_settings
SET lease_owner=?,lease_until=?,last_probe_started_at=?,revision=revision+1,updated_at=?
WHERE published_model_id IN (
  SELECT ms.published_model_id
  FROM model_monitor_settings ms
  JOIN published_models m ON m.id=ms.published_model_id
  WHERE ms.enabled=1 AND m.enabled=1
    AND ms.next_probe_at<=?
    AND (ms.lease_until IS NULL OR ms.lease_until<=?)
  ORDER BY ms.next_probe_at,ms.published_model_id
  LIMIT ?
)
AND enabled=1 AND (lease_until IS NULL OR lease_until<=?)
RETURNING published_model_id`, owner, leaseUntil.UTC().UnixMilli(), nowMS, nowMS, nowMS, nowMS, limit, nowMS)
	if err != nil {
		return nil, err
	}
	claimedIDs, err := scanClaimedMonitorIDs(rows)
	if err != nil {
		return nil, err
	}
	jobs := make([]ModelMonitorJob, 0, len(claimedIDs))
	for _, routeID := range claimedIDs {
		job, loadErr := loadModelMonitorJob(ctx, tx, routeID, owner, nowMS)
		if loadErr != nil {
			return nil, loadErr
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(left, right int) bool {
		if jobs[left].Setting.NextProbeAt.Equal(jobs[right].Setting.NextProbeAt) {
			return jobs[left].Setting.PublishedModelID < jobs[right].Setting.PublishedModelID
		}
		return jobs[left].Setting.NextProbeAt.Before(jobs[right].Setting.NextProbeAt)
	})
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) ClaimModelMonitor(
	ctx context.Context,
	publishedModelID int64,
	owner string,
	now time.Time,
	leaseDuration time.Duration,
) (ModelMonitorJob, error) {
	if publishedModelID <= 0 {
		return ModelMonitorJob{}, errors.New("published model ID must be positive")
	}
	owner, leaseUntil, err := validateMonitorLease(owner, now, leaseDuration)
	if err != nil {
		return ModelMonitorJob{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ModelMonitorJob{}, err
	}
	defer tx.Rollback()
	nowMS := now.UTC().UnixMilli()
	rows, err := tx.QueryContext(ctx, `UPDATE model_monitor_settings
SET lease_owner=?,lease_until=?,last_probe_started_at=?,revision=revision+1,updated_at=?
WHERE published_model_id=? AND enabled=1
  AND (lease_until IS NULL OR lease_until<=?)
  AND EXISTS (
    SELECT 1 FROM published_models m
    WHERE m.id=model_monitor_settings.published_model_id AND m.enabled=1
  )
RETURNING published_model_id`, owner, leaseUntil.UTC().UnixMilli(), nowMS, nowMS, publishedModelID, nowMS)
	if err != nil {
		return ModelMonitorJob{}, err
	}
	claimedIDs, err := scanClaimedMonitorIDs(rows)
	if err != nil {
		return ModelMonitorJob{}, err
	}
	if len(claimedIDs) == 0 {
		var enabled int
		var activeLease sql.NullInt64
		lookupErr := tx.QueryRowContext(ctx, `SELECT ms.enabled,ms.lease_until
FROM model_monitor_settings ms
JOIN published_models m ON m.id=ms.published_model_id
WHERE ms.published_model_id=? AND m.enabled=1`, publishedModelID).Scan(&enabled, &activeLease)
		if lookupErr == nil && enabled == 1 && activeLease.Valid && activeLease.Int64 > nowMS {
			return ModelMonitorJob{}, ErrModelMonitorBusy
		}
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return ModelMonitorJob{}, lookupErr
		}
		return ModelMonitorJob{}, ErrModelMonitorNotFound
	}
	job, err := loadModelMonitorJob(ctx, tx, publishedModelID, owner, nowMS)
	if err != nil {
		return ModelMonitorJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelMonitorJob{}, err
	}
	return job, nil
}

func (s *Store) FinishModelMonitorClaim(
	ctx context.Context,
	publishedModelID int64,
	owner string,
	finishedAt time.Time,
) error {
	owner = strings.TrimSpace(owner)
	if publishedModelID <= 0 || owner == "" || finishedAt.IsZero() {
		return errors.New("published model ID, lease owner, and finish time are required")
	}
	finishedMS := finishedAt.UTC().UnixMilli()
	result, err := s.DB.ExecContext(ctx, `UPDATE model_monitor_settings
SET lease_owner=NULL,lease_until=NULL,last_probe_finished_at=?,
    interval_ms=(SELECT probe_interval_ms FROM runtime_settings WHERE singleton_id=1),
    next_probe_at=?+(SELECT probe_interval_ms FROM runtime_settings WHERE singleton_id=1),
    revision=revision+1,updated_at=?
WHERE published_model_id=? AND lease_owner=?`, finishedMS, finishedMS, finishedMS, publishedModelID, owner)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrModelMonitorLeaseLost
	}
	return nil
}

func (s *Store) ReleaseModelMonitorClaim(ctx context.Context, publishedModelID int64, owner string) error {
	owner = strings.TrimSpace(owner)
	if publishedModelID <= 0 || owner == "" {
		return errors.New("published model ID and lease owner are required")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE model_monitor_settings
SET lease_owner=NULL,lease_until=NULL,revision=revision+1,updated_at=?
WHERE published_model_id=? AND lease_owner=?`, NowMS(), publishedModelID, owner)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrModelMonitorLeaseLost
	}
	return nil
}

func (s *Store) StartModelProbeRun(ctx context.Context, input ModelProbeRunWrite) error {
	input.ID = strings.TrimSpace(input.ID)
	input.LeaseOwner = strings.TrimSpace(input.LeaseOwner)
	input.TriggerKind = strings.ToLower(strings.TrimSpace(input.TriggerKind))
	if input.ID == "" || len(input.ID) > 128 || input.PublishedModelID <= 0 || input.LeaseOwner == "" {
		return errors.New("probe run ID, published model ID, and lease owner are required")
	}
	if input.TriggerKind != "scheduled" && input.TriggerKind != "manual" {
		return errors.New("probe trigger must be scheduled or manual")
	}
	if input.TargetCount < 0 || input.StartedAt.IsZero() {
		return errors.New("probe target count and start time are invalid")
	}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO model_probe_runs(
id,published_model_id,published_model_revision,public_model_snapshot,trigger_kind,status,target_count,
success_count,failure_count,skipped_count,started_at,finished_at)
SELECT ?,m.id,m.revision,m.public_name,?,'running',?,0,0,0,?,NULL
FROM published_models m
JOIN model_monitor_settings ms ON ms.published_model_id=m.id
WHERE m.id=? AND m.enabled=1 AND ms.enabled=1 AND ms.lease_owner=?`, input.ID, input.TriggerKind, input.TargetCount,
		input.StartedAt.UTC().UnixMilli(), input.PublishedModelID, input.LeaseOwner)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrModelMonitorLeaseLost
	}
	return nil
}

func (s *Store) SaveModelProbeTargetResult(ctx context.Context, input ModelProbeTargetWrite) error {
	input = normalizeModelProbeTargetWrite(input)
	if err := validateModelProbeTargetWrite(input); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var historyLimit int
	err = tx.QueryRowContext(ctx, `SELECT ms.history_limit
FROM model_probe_runs r
JOIN model_monitor_settings ms ON ms.published_model_id=r.published_model_id
WHERE r.id=? AND r.published_model_id=? AND r.status='running'`, input.RunID, input.PublishedModelID).Scan(&historyLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrModelProbeRunNotFound
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO model_probe_results(
run_id,published_model_id,published_model_target_id,published_model_target_revision,
provider_model_target_id,provider_model_target_revision,target_position,
site_id,endpoint_id,site_name_snapshot,endpoint_name_snapshot,source_model_snapshot,wire_protocol,api_surface,
outcome,permit_mode,permit_reason,http_status,failure_kind,error_code,latency_ms,first_output_ms,started_at,finished_at,
health_applied,health_apply_reason,health_error_code)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, input.RunID, input.PublishedModelID,
		input.PublishedModelTargetID, input.PublishedModelTargetRevision,
		input.ProviderModelTargetID, input.ProviderModelTargetRevision, input.TargetPosition,
		input.SiteID, input.EndpointID, strings.TrimSpace(input.SiteName), strings.TrimSpace(input.EndpointName),
		strings.TrimSpace(input.SourceModel), strings.TrimSpace(input.WireProtocol), strings.TrimSpace(input.Surface),
		input.Outcome, nullableString(input.PermitMode), nullableString(input.PermitReason), input.HTTPStatus,
		nullableString(input.FailureKind), nullableString(input.ErrorCode), input.LatencyMS, input.FirstOutputMS,
		input.StartedAt.UTC().UnixMilli(), input.FinishedAt.UTC().UnixMilli(), boolInt(input.HealthApplied),
		nullableString(input.HealthApplyReason), nullableString(input.HealthErrorCode))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_probe_results
WHERE published_model_id=? AND provider_model_target_id=? AND id NOT IN (
  SELECT id FROM model_probe_results
  WHERE published_model_id=? AND provider_model_target_id=?
  ORDER BY finished_at DESC,id DESC LIMIT ?
)`, input.PublishedModelID, input.ProviderModelTargetID, input.PublishedModelID, input.ProviderModelTargetID, historyLimit); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_probe_runs
WHERE published_model_id=? AND status<>'running'
  AND NOT EXISTS (SELECT 1 FROM model_probe_results p WHERE p.run_id=model_probe_runs.id)`, input.PublishedModelID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinishModelProbeRun(ctx context.Context, runID, status string, finishedAt time.Time) (ModelProbeRun, error) {
	runID = strings.TrimSpace(runID)
	status = strings.ToLower(strings.TrimSpace(status))
	if runID == "" || finishedAt.IsZero() {
		return ModelProbeRun{}, errors.New("probe run ID and finish time are required")
	}
	switch status {
	case "completed", "cancelled", "internal_error":
	default:
		return ModelProbeRun{}, errors.New("probe run finish status is invalid")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE model_probe_runs
SET status=?,
    success_count=(SELECT COUNT(*) FROM model_probe_results p WHERE p.run_id=model_probe_runs.id AND p.outcome='success'),
    failure_count=(SELECT COUNT(*) FROM model_probe_results p WHERE p.run_id=model_probe_runs.id AND p.outcome='failure'),
    skipped_count=(SELECT COUNT(*) FROM model_probe_results p WHERE p.run_id=model_probe_runs.id AND p.outcome='skipped'),
    finished_at=?
WHERE id=? AND status='running'`, status, finishedAt.UTC().UnixMilli(), runID)
	if err != nil {
		return ModelProbeRun{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ModelProbeRun{}, err
	}
	if changed != 1 {
		return ModelProbeRun{}, ErrModelProbeRunNotFound
	}
	return s.GetModelProbeRun(ctx, runID)
}

func (s *Store) GetModelProbeRun(ctx context.Context, runID string) (ModelProbeRun, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ModelProbeRun{}, errors.New("probe run ID is required")
	}
	item, err := scanModelProbeRun(s.DB.QueryRowContext(ctx, modelProbeRunSelect+` WHERE id=?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return ModelProbeRun{}, ErrModelProbeRunNotFound
	}
	return item, err
}

// ListModelProbeTargetResults returns newest-first real probe points. It never
// manufactures missing intervals and never expands credential attempts into
// extra target history points.
func (s *Store) ListModelProbeTargetResults(
	ctx context.Context,
	publishedModelID, providerModelTargetID int64,
	limit int,
) ([]ModelProbeTargetResult, error) {
	if publishedModelID <= 0 || providerModelTargetID <= 0 {
		return nil, errors.New("route and provider target IDs must be positive")
	}
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("probe history limit must be between 1 and 1000")
	}
	rows, err := s.DB.QueryContext(ctx, modelProbeTargetResultSelect+`
WHERE published_model_id=? AND provider_model_target_id=?
ORDER BY finished_at DESC,id DESC LIMIT ?`, publishedModelID, providerModelTargetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ModelProbeTargetResult, 0)
	for rows.Next() {
		item, scanErr := scanModelProbeTargetResult(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const modelMonitorSettingSelect = `SELECT published_model_id,enabled,
(SELECT probe_interval_ms FROM runtime_settings WHERE singleton_id=1),history_limit,
next_probe_at,last_probe_started_at,last_probe_finished_at,lease_owner,lease_until,revision,created_at,updated_at
FROM model_monitor_settings`

func scanModelMonitorSetting(row scanner) (ModelMonitorSetting, error) {
	var item ModelMonitorSetting
	var enabled int
	var intervalMS, nextProbeAt, createdAt, updatedAt int64
	var lastStarted, lastFinished, leaseUntil sql.NullInt64
	var leaseOwner sql.NullString
	err := row.Scan(&item.PublishedModelID, &enabled, &intervalMS, &item.HistoryLimit,
		&nextProbeAt, &lastStarted, &lastFinished, &leaseOwner, &leaseUntil, &item.Revision, &createdAt, &updatedAt)
	if err != nil {
		return ModelMonitorSetting{}, err
	}
	item.Enabled = enabled == 1
	item.Interval = time.Duration(intervalMS) * time.Millisecond
	item.NextProbeAt = time.UnixMilli(nextProbeAt).UTC()
	item.CreatedAt = time.UnixMilli(createdAt).UTC()
	item.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	item.LeaseOwner = leaseOwner.String
	item.LastProbeStartedAt = nullableTime(lastStarted)
	item.LastProbeFinishedAt = nullableTime(lastFinished)
	item.LeaseUntil = nullableTime(leaseUntil)
	return item, nil
}

func loadModelMonitorJob(ctx context.Context, tx *sql.Tx, publishedModelID int64, owner string, nowMS int64) (ModelMonitorJob, error) {
	var job ModelMonitorJob
	var enabled int
	var intervalMS, nextProbeAt, createdAt, updatedAt int64
	var lastStarted, lastFinished, leaseUntil sql.NullInt64
	var leaseOwner sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT ms.published_model_id,ms.enabled,
(SELECT probe_interval_ms FROM runtime_settings WHERE singleton_id=1),
ms.history_limit,ms.next_probe_at,ms.last_probe_started_at,ms.last_probe_finished_at,ms.lease_owner,ms.lease_until,
ms.revision,ms.created_at,ms.updated_at,m.public_name,m.revision
FROM model_monitor_settings ms
JOIN published_models m ON m.id=ms.published_model_id
WHERE ms.published_model_id=? AND ms.enabled=1 AND m.enabled=1 AND ms.lease_owner=?`, publishedModelID, owner).Scan(
		&job.Setting.PublishedModelID, &enabled, &intervalMS,
		&job.Setting.HistoryLimit, &nextProbeAt, &lastStarted, &lastFinished, &leaseOwner, &leaseUntil,
		&job.Setting.Revision, &createdAt, &updatedAt, &job.PublicModel, &job.PublishedModelRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelMonitorJob{}, ErrModelMonitorLeaseLost
	}
	if err != nil {
		return ModelMonitorJob{}, err
	}
	job.Setting.Enabled = enabled == 1
	job.Setting.Interval = time.Duration(intervalMS) * time.Millisecond
	job.Setting.NextProbeAt = time.UnixMilli(nextProbeAt).UTC()
	job.Setting.LastProbeStartedAt = nullableTime(lastStarted)
	job.Setting.LastProbeFinishedAt = nullableTime(lastFinished)
	job.Setting.LeaseOwner = leaseOwner.String
	job.Setting.LeaseUntil = nullableTime(leaseUntil)
	job.Setting.CreatedAt = time.UnixMilli(createdAt).UTC()
	job.Setting.UpdatedAt = time.UnixMilli(updatedAt).UTC()

	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.revision,t.provider_model_target_id,p.revision,t.position,
p.site_id,s.name,p.endpoint_id,e.name,e.base_url,e.wire_protocol,e.surface,e.adapter_kind,e.auth_scheme,
e.header_template_json,CASE WHEN e.secret_headers_cipher IS NOT NULL THEN 1 ELSE 0 END,e.cipher_version,p.source_model
FROM published_model_targets t
JOIN provider_model_targets p ON p.id=t.provider_model_target_id
JOIN sites s ON s.id=p.site_id
JOIN site_endpoints e ON e.id=p.endpoint_id AND e.site_id=p.site_id
WHERE t.published_model_id=? AND p.enabled=1 AND s.enabled=1 AND e.enabled=1
ORDER BY t.position,t.id`, publishedModelID)
	if err != nil {
		return ModelMonitorJob{}, err
	}
	for rows.Next() {
		var target ModelMonitorTarget
		var headerTemplate []byte
		var secretConfigured int
		if err := rows.Scan(&target.PublishedModelTargetID, &target.PublishedModelTargetRevision,
			&target.ProviderModelTargetID, &target.ProviderModelTargetRevision, &target.Position,
			&target.SiteID, &target.SiteName, &target.EndpointID, &target.EndpointName, &target.BaseURL,
			&target.WireProtocol, &target.Surface, &target.AdapterKind, &target.AuthScheme, &headerTemplate,
			&secretConfigured, &target.SecretHeadersCipherVersion, &target.SourceModel); err != nil {
			rows.Close()
			return ModelMonitorJob{}, err
		}
		target.HeaderTemplate = append(json.RawMessage(nil), headerTemplate...)
		target.SecretHeadersConfigured = secretConfigured == 1
		target.CredentialIDs = make([]int64, 0)
		job.Targets = append(job.Targets, target)
	}
	if err := rows.Close(); err != nil {
		return ModelMonitorJob{}, err
	}
	if err := rows.Err(); err != nil {
		return ModelMonitorJob{}, err
	}
	for index := range job.Targets {
		target := &job.Targets[index]
		credentialRows, queryErr := tx.QueryContext(ctx, `SELECT b.credential_id
FROM credential_endpoint_bindings b
JOIN site_credentials c ON c.id=b.credential_id AND c.site_id=b.site_id
JOIN credential_runtime_state rs ON rs.credential_id=c.id
LEFT JOIN credential_target_access a ON a.credential_id=c.id
  AND a.provider_model_target_id=? AND a.endpoint_id=b.endpoint_id AND a.site_id=b.site_id
WHERE b.site_id=? AND b.endpoint_id=? AND b.enabled=1 AND c.enabled=1
  AND (rs.state='active' OR (rs.state='cooling' AND rs.cooling_until<=?))
  AND (a.availability IS NULL OR a.availability IN ('unknown','supported'))
ORDER BY b.position,b.credential_id`, target.ProviderModelTargetID, target.SiteID, target.EndpointID, nowMS)
		if queryErr != nil {
			return ModelMonitorJob{}, queryErr
		}
		for credentialRows.Next() {
			var credentialID int64
			if scanErr := credentialRows.Scan(&credentialID); scanErr != nil {
				credentialRows.Close()
				return ModelMonitorJob{}, scanErr
			}
			target.CredentialIDs = append(target.CredentialIDs, credentialID)
		}
		if closeErr := credentialRows.Close(); closeErr != nil {
			return ModelMonitorJob{}, closeErr
		}
		if rowsErr := credentialRows.Err(); rowsErr != nil {
			return ModelMonitorJob{}, rowsErr
		}
	}
	return job, nil
}

func scanClaimedMonitorIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func normalizeMonitorHistoryLimit(input ModelMonitorSettingWrite) (int, error) {
	historyLimit := input.HistoryLimit
	if historyLimit == 0 {
		historyLimit = DefaultModelMonitorHistoryLimit
	}
	if historyLimit < 1 || historyLimit > 10000 {
		return 0, errors.New("monitor history limit must be between 1 and 10000")
	}
	return historyLimit, nil
}

func validateMonitorLease(owner string, now time.Time, duration time.Duration) (string, time.Time, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" || len(owner) > 128 {
		return "", time.Time{}, errors.New("monitor lease owner must contain between 1 and 128 characters")
	}
	if now.IsZero() || duration < time.Millisecond || duration%time.Millisecond != 0 {
		return "", time.Time{}, errors.New("monitor lease requires a time and positive whole-millisecond duration")
	}
	leaseUntil := now.UTC().Add(duration)
	if !leaseUntil.After(now) {
		return "", time.Time{}, errors.New("monitor lease deadline overflowed")
	}
	return owner, leaseUntil, nil
}

func validateModelProbeTargetWrite(input ModelProbeTargetWrite) error {
	if input.RunID == "" || input.PublishedModelID <= 0 || input.PublishedModelTargetID <= 0 ||
		input.PublishedModelTargetRevision <= 0 || input.ProviderModelTargetID <= 0 ||
		input.ProviderModelTargetRevision <= 0 || input.TargetPosition < 0 ||
		input.SiteID <= 0 || input.EndpointID <= 0 {
		return errors.New("probe result identity is invalid")
	}
	if strings.TrimSpace(input.SiteName) == "" || strings.TrimSpace(input.EndpointName) == "" ||
		strings.TrimSpace(input.SourceModel) == "" || strings.TrimSpace(input.WireProtocol) == "" ||
		strings.TrimSpace(input.Surface) == "" {
		return errors.New("probe result snapshots are required")
	}
	if input.StartedAt.IsZero() || input.FinishedAt.IsZero() || input.FinishedAt.Before(input.StartedAt) || input.LatencyMS < 0 {
		return errors.New("probe result timing is invalid")
	}
	if input.FirstOutputMS != nil && (*input.FirstOutputMS < 0 || *input.FirstOutputMS > input.LatencyMS) {
		return errors.New("first output latency must fit within total latency")
	}
	if input.HTTPStatus != nil && (*input.HTTPStatus < 100 || *input.HTTPStatus > 599) {
		return errors.New("probe HTTP status must be between 100 and 599")
	}
	if input.PermitMode != "" && input.PermitMode != "normal" && input.PermitMode != "half_open" {
		return errors.New("probe permit mode must be normal or half_open")
	}
	switch input.Outcome {
	case "success":
		if input.FailureKind != "" {
			return errors.New("successful probe cannot contain a failure kind")
		}
	case "failure":
		if input.FailureKind == "" {
			return errors.New("failed probe requires a failure kind")
		}
	case "skipped":
		if input.PermitReason == "" {
			return errors.New("skipped probe requires a permit reason")
		}
	default:
		return fmt.Errorf("unsupported probe outcome %q", input.Outcome)
	}
	return nil
}

func normalizeModelProbeTargetWrite(input ModelProbeTargetWrite) ModelProbeTargetWrite {
	input.RunID = strings.TrimSpace(input.RunID)
	input.SiteName = strings.TrimSpace(input.SiteName)
	input.EndpointName = strings.TrimSpace(input.EndpointName)
	input.SourceModel = strings.TrimSpace(input.SourceModel)
	input.WireProtocol = strings.ToLower(strings.TrimSpace(input.WireProtocol))
	input.Surface = strings.ToLower(strings.TrimSpace(input.Surface))
	input.Outcome = strings.ToLower(strings.TrimSpace(input.Outcome))
	input.PermitMode = strings.ToLower(strings.TrimSpace(input.PermitMode))
	input.PermitReason = strings.ToLower(strings.TrimSpace(input.PermitReason))
	input.FailureKind = strings.ToLower(strings.TrimSpace(input.FailureKind))
	input.ErrorCode = strings.ToLower(strings.TrimSpace(input.ErrorCode))
	input.HealthApplyReason = strings.ToLower(strings.TrimSpace(input.HealthApplyReason))
	input.HealthErrorCode = strings.ToLower(strings.TrimSpace(input.HealthErrorCode))
	return input
}

const modelProbeRunSelect = `SELECT id,published_model_id,published_model_revision,public_model_snapshot,trigger_kind,status,
target_count,success_count,failure_count,skipped_count,started_at,finished_at FROM model_probe_runs`

func scanModelProbeRun(row scanner) (ModelProbeRun, error) {
	var item ModelProbeRun
	var startedAt int64
	var finishedAt sql.NullInt64
	if err := row.Scan(&item.ID, &item.PublishedModelID, &item.PublishedModelRevision, &item.PublicModelSnapshot,
		&item.TriggerKind, &item.Status, &item.TargetCount, &item.SuccessCount, &item.FailureCount,
		&item.SkippedCount, &startedAt, &finishedAt); err != nil {
		return ModelProbeRun{}, err
	}
	item.StartedAt = time.UnixMilli(startedAt).UTC()
	item.FinishedAt = nullableTime(finishedAt)
	return item, nil
}

const modelProbeTargetResultSelect = `SELECT id,run_id,published_model_id,published_model_target_id,
published_model_target_revision,provider_model_target_id,provider_model_target_revision,target_position,site_id,endpoint_id,
site_name_snapshot,endpoint_name_snapshot,source_model_snapshot,wire_protocol,api_surface,outcome,
permit_mode,permit_reason,http_status,failure_kind,error_code,latency_ms,first_output_ms,started_at,finished_at,
health_applied,health_apply_reason,health_error_code FROM model_probe_results`

func scanModelProbeTargetResult(row scanner) (ModelProbeTargetResult, error) {
	var item ModelProbeTargetResult
	var permitMode, permitReason, failureKind, errorCode, healthReason, healthError sql.NullString
	var httpStatus, firstOutput sql.NullInt64
	var startedAt, finishedAt int64
	var healthApplied int
	err := row.Scan(&item.ID, &item.RunID, &item.PublishedModelID, &item.PublishedModelTargetID,
		&item.PublishedModelTargetRevision, &item.ProviderModelTargetID,
		&item.ProviderModelTargetRevision, &item.TargetPosition,
		&item.SiteID, &item.EndpointID, &item.SiteNameSnapshot, &item.EndpointNameSnapshot,
		&item.SourceModelSnapshot, &item.WireProtocol, &item.Surface, &item.Outcome,
		&permitMode, &permitReason, &httpStatus, &failureKind, &errorCode, &item.LatencyMS,
		&firstOutput, &startedAt, &finishedAt, &healthApplied, &healthReason, &healthError)
	if err != nil {
		return ModelProbeTargetResult{}, err
	}
	item.PermitMode = permitMode.String
	item.PermitReason = permitReason.String
	item.FailureKind = failureKind.String
	item.ErrorCode = errorCode.String
	item.HealthApplied = healthApplied == 1
	item.HealthApplyReason = healthReason.String
	item.HealthErrorCode = healthError.String
	item.StartedAt = time.UnixMilli(startedAt).UTC()
	item.FinishedAt = time.UnixMilli(finishedAt).UTC()
	if httpStatus.Valid {
		value := int(httpStatus.Int64)
		item.HTTPStatus = &value
	}
	if firstOutput.Valid {
		value := firstOutput.Int64
		item.FirstOutputMS = &value
	}
	return item, nil
}

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := time.UnixMilli(value.Int64).UTC()
	return &result
}
