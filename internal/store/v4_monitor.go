package store

import (
	"context"
	"database/sql"

	"github.com/LuTianTian001/JieShan/internal/inferenceprotocol"
)

// ExpireStaleProbeRuns prevents an interrupted process from leaving a model
// permanently unschedulable. The grace period is longer than the model-level
// probe deadline used by the gateway.
func (s *Store) ExpireStaleProbeRuns(ctx context.Context, nowMS int64) (int64, error) {
	if nowMS <= 0 {
		nowMS = NowMS()
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE probe_runs
SET status='cancelled',error_message='probe worker stopped before completion',finished_at=?
WHERE status='running' AND started_at + (
  SELECT (p.request_deadline_seconds+30)*1000 FROM published_models p WHERE p.id=probe_runs.published_model_id
) <= ?`, nowMS, nowMS)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ListDuePublishedModels returns selected V3 models whose own monitoring
// interval has elapsed. A running probe suppresses duplicate scheduler work.
func (s *Store) ListDuePublishedModels(ctx context.Context, nowMS int64, limit int) ([]PublishedModel, error) {
	if nowMS <= 0 {
		nowMS = NowMS()
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, publishedModelSelect+`
WHERE enabled=1 AND monitor_enabled=1
  AND NOT EXISTS (
    SELECT 1 FROM probe_runs running
    WHERE running.published_model_id=published_models.id AND running.status='running'
  )
  AND COALESCE((
    SELECT MAX(previous.started_at) FROM probe_runs previous
    WHERE previous.published_model_id=published_models.id
  ),0) + monitor_interval_seconds*1000 <= ?
ORDER BY COALESCE((
  SELECT MAX(previous.started_at) FROM probe_runs previous
  WHERE previous.published_model_id=published_models.id
),0),id LIMIT ?`, nowMS, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PublishedModel, 0)
	for rows.Next() {
		item, err := scanPublishedModel(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListPublishedModelMonitorMatrix builds the selected-model matrix with three
// bulk queries, so the monitor page does not need one query per model/target.
func (s *Store) ListPublishedModelMonitorMatrix(ctx context.Context) ([]PublishedModelMonitor, error) {
	models, err := listMonitoredPublishedModels(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return []PublishedModelMonitor{}, nil
	}

	targetRows, err := s.DB.QueryContext(ctx, routeSiteTargetSelect+`
WHERE t.published_model_id IN (SELECT id FROM published_models WHERE enabled=1 AND monitor_enabled=1)
ORDER BY t.published_model_id,t.position,t.id`)
	if err != nil {
		return nil, err
	}
	targets := make(map[int64][]RouteSiteTargetMonitor, len(models))
	for targetRows.Next() {
		target, err := scanRouteSiteTarget(targetRows)
		if err != nil {
			return nil, err
		}
		targets[target.PublishedModelID] = append(targets[target.PublishedModelID], RouteSiteTargetMonitor{
			RouteSiteTarget: target,
			Health: RouteSiteTargetHealth{
				TargetID: target.ID, CircuitPhase: "closed", CapabilityState: "unknown",
			},
		})
	}
	if err := targetRows.Err(); err != nil {
		targetRows.Close()
		return nil, err
	}
	if err := targetRows.Close(); err != nil {
		return nil, err
	}

	healthRows, err := s.DB.QueryContext(ctx, routeSiteTargetHealthSelect+`
WHERE t.published_model_id IN (SELECT id FROM published_models WHERE enabled=1 AND monitor_enabled=1)
ORDER BY t.published_model_id,t.position,t.id`)
	if err != nil {
		return nil, err
	}
	healthByTarget := make(map[int64]RouteSiteTargetHealth)
	for healthRows.Next() {
		health, err := scanRouteSiteTargetHealth(healthRows)
		if err != nil {
			healthRows.Close()
			return nil, err
		}
		healthByTarget[health.TargetID] = health
	}
	if err := healthRows.Close(); err != nil {
		return nil, err
	}

	probeRows, err := s.DB.QueryContext(ctx, `SELECT a.id,a.probe_run_id,a.attempt_index,a.route_site_target_id,a.site_id,a.endpoint_id,
a.inference_credential_id,a.site_model_id,a.site_name,a.endpoint_name,a.credential_name,a.source_model,a.status,
a.http_status,a.latency_ms,a.first_output_ms,a.error_class,a.error_message,a.started_at,a.finished_at
FROM probe_attempts a JOIN route_site_targets t ON t.id=a.route_site_target_id
JOIN published_models p ON p.id=t.published_model_id
WHERE p.enabled=1 AND p.monitor_enabled=1
  AND a.id=(SELECT latest.id FROM probe_attempts latest
    WHERE latest.route_site_target_id=a.route_site_target_id
    ORDER BY latest.finished_at DESC,latest.id DESC LIMIT 1)
ORDER BY t.published_model_id,t.position,t.id`)
	if err != nil {
		return nil, err
	}
	latestByTarget := make(map[int64]ProbeAttempt)
	for probeRows.Next() {
		attempt, err := scanProbeAttempt(probeRows)
		if err != nil {
			probeRows.Close()
			return nil, err
		}
		if attempt.RouteSiteTargetID != nil {
			latestByTarget[*attempt.RouteSiteTargetID] = attempt
		}
	}
	if err := probeRows.Close(); err != nil {
		return nil, err
	}

	items := make([]PublishedModelMonitor, 0, len(models))
	for _, model := range models {
		modelTargets := targets[model.ID]
		for index := range modelTargets {
			targetID := modelTargets[index].ID
			if health, ok := healthByTarget[targetID]; ok {
				modelTargets[index].Health = health
			}
			if !inferenceprotocol.For(modelTargets[index].WireProtocol).RouteEligible {
				modelTargets[index].Health.CapabilityState = "unsupported"
				modelTargets[index].Health.LastErrorClass = "unsupported_protocol"
				modelTargets[index].Health.LastErrorMessage = "native protocol supports model discovery only; OpenAI gateway routing is unavailable"
			}
			if attempt, ok := latestByTarget[targetID]; ok {
				copy := attempt
				modelTargets[index].LastProbe = &copy
			}
		}
		items = append(items, PublishedModelMonitor{PublishedModel: model, Targets: modelTargets})
	}
	return items, nil
}

type publishedModelScanner interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listMonitoredPublishedModels(ctx context.Context, queryer publishedModelScanner) ([]PublishedModel, error) {
	rows, err := queryer.QueryContext(ctx, publishedModelSelect+` WHERE enabled=1 AND monitor_enabled=1 ORDER BY public_name COLLATE NOCASE,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PublishedModel, 0)
	for rows.Next() {
		item, err := scanPublishedModel(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
